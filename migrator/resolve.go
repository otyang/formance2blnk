package migrator

import (
	"context"
	"fmt"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/state"
)

// errSkipped signals that a posting touches a skipped account.
type errSkipped struct{ address string }

func (e errSkipped) Error() string { return fmt.Sprintf("account %q is mapped to skip", e.address) }

// endpoint returns the Blnk source/destination identifier for an account and
// asset: a balance ID or an "@Indicator".
func (m *Migrator) endpoint(ctx context.Context, st *Stats, ledger, address string, asset mapping.Asset) (string, error) {
	t, err := m.opts.Mapper.Account(ledger, address)
	if err != nil {
		return "", fmt.Errorf("map account %q: %w", address, err)
	}
	switch t.Kind {
	case mapping.KindIndicator:
		return t.Indicator, nil
	case mapping.KindSkip:
		return "", errSkipped{address}
	}
	rec, err := m.ensureBalance(ctx, st, ledger, address, asset, t.Ledger)
	if err != nil {
		return "", err
	}
	return rec.BalanceID, nil
}

func (m *Migrator) ensureBalance(ctx context.Context, st *Stats, ledger, address string, asset mapping.Asset, blnkLedger string) (state.BalanceRecord, error) {
	key := state.BalanceKey{SourceLedger: ledger, Address: address, Asset: asset.Source}
	if rec, ok := m.opts.Store.Balance(key); ok {
		return rec, nil
	}
	m.balMu.Lock()
	defer m.balMu.Unlock()
	if rec, ok := m.opts.Store.Balance(key); ok {
		return rec, nil
	}
	ledgerID, err := m.ensureLedger(ctx, st, blnkLedger)
	if err != nil {
		return state.BalanceRecord{}, err
	}
	meta := map[string]any{
		"formance_ledger":  ledger,
		"formance_address": address,
		"formance_asset":   asset.Source,
		"migrated_by":      "formance2blnk",
	}
	if m.opts.CopyAccountMetadata {
		acc, err := m.src.GetAccount(ctx, ledger, address)
		if err != nil {
			return state.BalanceRecord{}, fmt.Errorf("read account %q metadata: %w", address, err)
		}
		if len(acc.Metadata) > 0 {
			meta["formance_metadata"] = map[string]string(acc.Metadata)
		}
	}
	rec := state.BalanceRecord{BalanceKey: key, LedgerID: ledgerID}
	if m.opts.DryRun {
		m.dryIDs++
		rec.BalanceID = fmt.Sprintf("dry_bln_%d", m.dryIDs)
	} else {
		b, err := m.dst.CreateBalance(ctx, blnk.BalanceRequest{LedgerID: ledgerID, Currency: asset.Currency, MetaData: meta})
		if err != nil {
			return state.BalanceRecord{}, fmt.Errorf("create balance for %s %s: %w", address, asset.Source, err)
		}
		rec.BalanceID = b.BalanceID
	}
	st.BalancesCreated++
	if err := m.opts.Store.PutBalance(rec); err != nil {
		return state.BalanceRecord{}, fmt.Errorf("persist balance: %w", err)
	}
	m.log.Debug("balance created", "address", address, "asset", asset.Source, "balance_id", rec.BalanceID, "ledger", blnkLedger)
	return rec, nil
}

func (m *Migrator) ensureLedger(ctx context.Context, st *Stats, name string) (string, error) {
	if id, ok := m.opts.Store.Ledger(name); ok {
		return id, nil
	}
	m.ledgerMu.Lock()
	defer m.ledgerMu.Unlock()
	if id, ok := m.opts.Store.Ledger(name); ok {
		return id, nil
	}
	var id string
	if m.opts.DryRun {
		id = "dry_ldg_" + name
	} else {
		l, err := m.dst.CreateLedger(ctx, name, map[string]any{"migrated_by": "formance2blnk"})
		if err != nil {
			return "", fmt.Errorf("create ledger %q: %w", name, err)
		}
		id = l.LedgerID
	}
	st.LedgersCreated++
	if err := m.opts.Store.PutLedger(name, id); err != nil {
		return "", fmt.Errorf("persist ledger: %w", err)
	}
	m.log.Info("ledger created", "name", name, "ledger_id", id)
	return id, nil
}

func stateKey(ledger, address, asset string) state.BalanceKey {
	return state.BalanceKey{SourceLedger: ledger, Address: address, Asset: asset}
}
