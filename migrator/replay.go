package migrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
)

// Replay re-records every Formance transaction in Blnk, oldest first,
// resuming from the stored checkpoint.
func (m *Migrator) Replay(ctx context.Context) (Stats, error) {
	return m.replay(ctx, time.Time{})
}

// ReplaySince replays only transactions whose effective timestamp is at or
// after since. It is the second half of a hybrid migration.
func (m *Migrator) ReplaySince(ctx context.Context, since time.Time) (Stats, error) {
	return m.replay(ctx, since)
}

func (m *Migrator) replay(ctx context.Context, since time.Time) (Stats, error) {
	st := newStats(PhaseReplay, m.opts.DryRun)
	for _, ledger := range m.opts.Ledgers {
		if err := m.replayLedger(ctx, st, ledger, since); err != nil {
			return st.finish(), fmt.Errorf("replay ledger %q: %w", ledger, err)
		}
	}
	return st.finish(), nil
}

func (m *Migrator) replayLedger(ctx context.Context, st *Stats, ledger string, since time.Time) error {
	var after *uint64
	if cp, ok := m.opts.Store.Checkpoint(ledger, PhaseReplay); ok {
		after = &cp
		st.LastTxID[ledger] = cp
		m.log.Info("resuming replay", "ledger", ledger, "after_tx", cp)
	} else {
		m.log.Info("starting replay", "ledger", ledger, "since", since)
	}

	w := m.newWriter(st)
	var (
		lastSeen   uint64
		seenAny    bool
		saved      uint64
		haveSaved  = after != nil
		sinceSaved int
	)
	if after != nil {
		saved = *after
	}
	save := func(id uint64) error {
		if haveSaved && id <= saved {
			return nil
		}
		if err := m.opts.Store.SetCheckpoint(ledger, PhaseReplay, id); err != nil {
			return fmt.Errorf("save checkpoint: %w", err)
		}
		saved, haveSaved, sinceSaved = id, true, 0
		st.LastTxID[ledger] = id
		return nil
	}

	err := m.src.Transactions(ctx, ledger, after, func(tx formance.Transaction) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		st.FormanceTxs++
		lastSeen, seenAny = tx.ID, true
		reqs, err := m.buildReplay(ctx, st, ledger, tx, since)
		if err != nil {
			return fmt.Errorf("transaction %d: %w", tx.ID, err)
		}
		flushedBefore := st.BlnkTxs + st.AlreadyMigrated
		if err := w.add(ctx, group{label: fmt.Sprintf("formance tx %d", tx.ID), reqs: reqs}); err != nil {
			return err
		}
		sinceSaved++
		if !w.pending() {
			// Everything up to and including tx is durable in Blnk.
			flushed := st.BlnkTxs+st.AlreadyMigrated != flushedBefore
			if flushed || sinceSaved >= 500 {
				if err := save(tx.ID); err != nil {
					return err
				}
			}
		}
		if st.FormanceTxs%int64(m.opts.ProgressEvery) == 0 {
			m.report(st, ledger)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := w.flush(ctx); err != nil {
		return err
	}
	if seenAny {
		if err := save(lastSeen); err != nil {
			return err
		}
	}
	m.report(st, ledger)
	return m.opts.Store.Flush()
}

func (m *Migrator) report(st *Stats, ledger string) {
	elapsed := time.Since(st.Started).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(st.FormanceTxs) / elapsed
	}
	m.log.Info("progress", "phase", st.Phase, "ledger", ledger, "formance_txs", st.FormanceTxs,
		"blnk_txs", st.BlnkTxs, "already_migrated", st.AlreadyMigrated, "tx_per_sec", fmt.Sprintf("%.1f", rate))
	if m.opts.Progress != nil {
		cp := *st
		cp.Ledger = ledger
		cp.Elapsed = time.Since(st.Started)
		m.opts.Progress(cp)
	}
}

// Reference returns the deterministic Blnk reference for a Formance posting.
func Reference(prefix, ledger string, txID uint64, posting int) string {
	return fmt.Sprintf("%s:%s:%d:%d", prefix, ledger, txID, posting)
}

func (m *Migrator) buildReplay(ctx context.Context, st *Stats, ledger string, tx formance.Transaction, since time.Time) ([]blnk.TransactionRequest, error) {
	if !since.IsZero() && tx.Timestamp.Before(since) {
		st.skip("before_cutoff")
		return nil, nil
	}
	if m.opts.Reverts == RevertSkip {
		// A revert is dropped only if its original was. The original is in
		// Blnk if it predates a hybrid cutoff (the snapshot holds it) or was
		// reverted after it was migrated, possibly in an earlier run.
		if origID, ok := tx.RevertsID(); ok && m.opts.Store.SkippedTx(ledger, origID) {
			// Not marked itself: if this revert is reverted in turn, that
			// re-applies the dropped original and must be replayed.
			st.skip("revert")
			return nil, nil
		}
		if tx.Reverted {
			if err := m.opts.Store.MarkSkippedTx(ledger, tx.ID); err != nil {
				return nil, fmt.Errorf("record skipped transaction: %w", err)
			}
			st.skip("reverted")
			return nil, nil
		}
	}

	description := tx.Metadata["description"]
	if description == "" {
		description = fmt.Sprintf("Formance %s tx %d", ledger, tx.ID)
	}
	var eff *time.Time
	if !tx.Timestamp.IsZero() {
		ts := tx.Timestamp.UTC()
		eff = &ts
	}

	var reqs []blnk.TransactionRequest
	for i, p := range tx.Postings {
		if p.Amount.Sign() <= 0 {
			st.skip("zero_amount")
			continue
		}
		if p.Source == p.Destination {
			st.skip("self_posting")
			continue
		}
		asset, err := m.opts.Mapper.Asset(p.Asset)
		if err != nil {
			return nil, err
		}
		src, err := m.endpoint(ctx, st, ledger, p.Source, asset)
		if err == nil {
			var dst string
			dst, err = m.endpoint(ctx, st, ledger, p.Destination, asset)
			if err == nil {
				if src == dst {
					st.skip("same_endpoint")
					continue
				}
				meta := map[string]any{
					"formance_ledger":      ledger,
					"formance_tx_id":       tx.ID,
					"formance_posting":     i,
					"formance_source":      p.Source,
					"formance_destination": p.Destination,
					"formance_asset":       p.Asset,
				}
				if tx.Reference != "" {
					meta["formance_reference"] = tx.Reference
				}
				if len(tx.Metadata) > 0 {
					meta["formance_metadata"] = map[string]string(tx.Metadata)
				}
				req := blnk.TransactionRequest{
					PreciseAmount:  p.Amount,
					Precision:      asset.Precision,
					Reference:      Reference(m.opts.ReferencePrefix, ledger, tx.ID, i),
					Currency:       asset.Currency,
					Source:         src,
					Destination:    dst,
					Description:    description,
					AllowOverdraft: !m.opts.EnforceBalances,
					SkipQueue:      true,
					EffectiveDate:  eff,
					MetaData:       meta,
				}
				if m.opts.Transform != nil {
					keep, terr := m.opts.Transform(&req, TxContext{SourceLedger: ledger, Transaction: tx, PostingIndex: i, Posting: p})
					if terr != nil {
						return nil, fmt.Errorf("transform posting %d: %w", i, terr)
					}
					if !keep {
						st.skip("transform")
						continue
					}
				}
				reqs = append(reqs, req)
				continue
			}
		}
		var sk errSkipped
		if errors.As(err, &sk) && m.opts.OnSkippedAccount == SkippedAccountDrop {
			st.skip("skipped_account")
			continue
		}
		return nil, fmt.Errorf("posting %d: %w", i, err)
	}
	return reqs, nil
}
