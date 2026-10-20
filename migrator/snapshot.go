package migrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/mapping"
)

// Snapshot creates one opening-balance transaction per non-zero (account,
// asset) pair, using current Formance balances.
func (m *Migrator) Snapshot(ctx context.Context) (Stats, error) {
	return m.snapshot(ctx, time.Time{})
}

// SnapshotAsOf is like Snapshot but uses balances as of asOf (transactions
// effective strictly before asOf).
func (m *Migrator) SnapshotAsOf(ctx context.Context, asOf time.Time) (Stats, error) {
	return m.snapshot(ctx, asOf)
}

// Hybrid takes a snapshot as of cutoff and then replays every transaction
// effective at or after cutoff. Both halves are resumable.
func (m *Migrator) Hybrid(ctx context.Context, cutoff time.Time) (Stats, error) {
	if cutoff.IsZero() {
		return Stats{}, errors.New("migrator: hybrid mode requires a cutoff time")
	}
	// Transactions recorded after the snapshot but effective before a future
	// cutoff would be in neither half.
	if cutoff.After(time.Now()) {
		return Stats{}, fmt.Errorf("migrator: hybrid cutoff %s is in the future", cutoff.UTC().Format(time.RFC3339))
	}
	s1, err := m.snapshot(ctx, cutoff)
	if err != nil {
		return s1, err
	}
	s2, err := m.replay(ctx, cutoff)
	return merge("hybrid", s1, s2), err
}

func merge(phase string, a, b Stats) Stats {
	out := Stats{
		Phase: phase, Started: a.Started, Elapsed: a.Elapsed + b.Elapsed, DryRun: a.DryRun || b.DryRun,
		FormanceTxs: a.FormanceTxs + b.FormanceTxs, BlnkTxs: a.BlnkTxs + b.BlnkTxs,
		AlreadyMigrated: a.AlreadyMigrated + b.AlreadyMigrated,
		LedgersCreated:  a.LedgersCreated + b.LedgersCreated, BalancesCreated: a.BalancesCreated + b.BalancesCreated,
		Skipped: map[string]int64{}, LastTxID: map[string]uint64{},
	}
	for _, s := range []Stats{a, b} {
		for k, v := range s.Skipped {
			out.Skipped[k] += v
		}
		for k, v := range s.LastTxID {
			out.LastTxID[k] = v
		}
	}
	return out
}

func (m *Migrator) snapshot(ctx context.Context, asOf time.Time) (Stats, error) {
	st := newStats(PhaseSnapshot, m.opts.DryRun)
	marker := snapshotMarker(asOf)
	for _, ledger := range m.opts.Ledgers {
		if got, done := m.opts.Store.Checkpoint(ledger, PhaseSnapshot); done {
			if got != marker {
				return st.finish(), fmt.Errorf("snapshot ledger %q: already completed with a different as-of time (cutoff); "+
					"resuming with another one would double-count or drop transactions", ledger)
			}
			m.log.Info("snapshot already completed, skipping", "ledger", ledger)
			continue
		}
		if err := m.snapshotLedger(ctx, st, ledger, asOf); err != nil {
			return st.finish(), fmt.Errorf("snapshot ledger %q: %w", ledger, err)
		}
		if err := m.opts.Store.SetCheckpoint(ledger, PhaseSnapshot, marker); err != nil {
			return st.finish(), err
		}
	}
	return st.finish(), m.opts.Store.Flush()
}

// snapshotMarker is the value stored as the snapshot checkpoint: 1 for a
// snapshot of current balances, otherwise the as-of time in Unix
// nanoseconds, so that a resumed run cannot silently use another cutoff.
func snapshotMarker(asOf time.Time) uint64 {
	if asOf.IsZero() {
		return 1
	}
	return uint64(asOf.UnixNano())
}

// OpeningReference returns the deterministic reference of an opening balance.
func OpeningReference(prefix, ledger, address, asset string) string {
	return fmt.Sprintf("%s-open:%s:%s:%s", prefix, ledger, address, asset)
}

func (m *Migrator) snapshotLedger(ctx context.Context, st *Stats, ledger string, asOf time.Time) error {
	m.log.Info("starting snapshot", "ledger", ledger, "as_of", asOf)
	eff := time.Now().UTC()
	if !asOf.IsZero() {
		eff = asOf.UTC()
	}
	w := m.newWriter(st)
	err := m.src.Volumes(ctx, ledger, formance.VolumesOptions{EndTime: asOf}, func(v formance.Volume) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if v.Account == "world" {
			st.skip("world")
			return nil
		}
		target, err := m.opts.Mapper.Account(ledger, v.Account)
		if err != nil {
			return err
		}
		if target.Kind == mapping.KindSkip {
			if !v.Balance.IsZero() && m.opts.OnSkippedAccount == SkippedAccountError {
				return fmt.Errorf("account %q is mapped to skip but holds %s %s", v.Account, v.Balance, v.Asset)
			}
			st.skip("skipped_account")
			return nil
		}
		asset, err := m.opts.Mapper.Asset(v.Asset)
		if err != nil {
			return err
		}
		if v.Balance.IsZero() {
			if target.Kind == mapping.KindBalance && m.opts.IncludeZeroBalances {
				if _, err := m.ensureBalance(ctx, st, ledger, v.Account, asset, target.Ledger); err != nil {
					return err
				}
			}
			st.skip("zero_balance")
			return nil
		}
		ep, err := m.endpoint(ctx, st, ledger, v.Account, asset)
		if err != nil {
			return err
		}
		if ep == m.opts.OpeningIndicator {
			st.skip("same_endpoint")
			return nil
		}
		src, dst := m.opts.OpeningIndicator, ep
		amt := v.Balance
		if amt.Sign() < 0 {
			src, dst, amt = ep, m.opts.OpeningIndicator, amt.Abs()
		}
		ts := eff
		req := blnk.TransactionRequest{
			PreciseAmount:  amt,
			Precision:      asset.Precision,
			Reference:      OpeningReference(m.opts.ReferencePrefix, ledger, v.Account, v.Asset),
			Currency:       asset.Currency,
			Source:         src,
			Destination:    dst,
			Description:    fmt.Sprintf("Opening balance migrated from Formance %s", ledger),
			AllowOverdraft: true,
			SkipQueue:      true,
			EffectiveDate:  &ts,
			MetaData: map[string]any{
				"formance_ledger":  ledger,
				"formance_address": v.Account,
				"formance_asset":   v.Asset,
				"formance_input":   v.Input.String(),
				"formance_output":  v.Output.String(),
				"opening_balance":  true,
			},
		}
		if !asOf.IsZero() {
			req.MetaData["as_of"] = asOf.UTC().Format(time.RFC3339Nano)
		}
		if m.opts.Transform != nil {
			keep, err := m.opts.Transform(&req, TxContext{SourceLedger: ledger, PostingIndex: -1,
				Posting: formance.Posting{Source: src, Destination: v.Account, Amount: amt, Asset: v.Asset}})
			if err != nil {
				return err
			}
			if !keep {
				st.skip("transform")
				return nil
			}
		}
		return w.add(ctx, group{label: "opening " + v.Account + " " + v.Asset, reqs: []blnk.TransactionRequest{req}})
	})
	if err != nil {
		return err
	}
	if err := w.flush(ctx); err != nil {
		return err
	}
	m.report(st, ledger)
	return nil
}
