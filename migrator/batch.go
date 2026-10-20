package migrator

import (
	"context"
	"fmt"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/state"
)

// group is the set of Blnk transactions produced from one Formance
// transaction (or one opening balance). A group is always written atomically.
type group struct {
	label string
	reqs  []blnk.TransactionRequest
}

// writer accumulates groups into atomic bulk calls.
type writer struct {
	m      *Migrator
	st     *Stats
	groups []group
	size   int
}

func (m *Migrator) newWriter(st *Stats) *writer { return &writer{m: m, st: st} }

// add queues a group, flushing first if it would not fit.
func (w *writer) add(ctx context.Context, g group) error {
	if len(g.reqs) == 0 {
		return nil
	}
	if w.size > 0 && w.size+len(g.reqs) > w.m.opts.BatchSize {
		if err := w.flush(ctx); err != nil {
			return err
		}
	}
	w.groups = append(w.groups, g)
	w.size += len(g.reqs)
	if w.size >= w.m.opts.BatchSize {
		return w.flush(ctx)
	}
	return nil
}

func (w *writer) pending() bool { return w.size > 0 }

// flush writes all queued groups.
func (w *writer) flush(ctx context.Context) error {
	if w.size == 0 {
		return nil
	}
	groups := w.groups
	w.groups, w.size = nil, 0

	if w.m.opts.DryRun {
		for _, g := range groups {
			w.st.BlnkTxs += int64(len(g.reqs))
		}
		return nil
	}

	if len(groups) == 1 {
		return w.sendGroup(ctx, groups[0])
	}
	var all []blnk.TransactionRequest
	for _, g := range groups {
		all = append(all, g.reqs...)
	}
	err := w.send(ctx, all)
	if err == nil {
		w.st.BlnkTxs += int64(len(all))
		return nil
	}
	// Either a reference was already migrated by an earlier (interrupted)
	// run, or one group in the batch is bad. Retry group by group: groups
	// are atomic, so a duplicate means the whole group already exists.
	w.m.log.Debug("batch failed, retrying per group", "groups", len(groups), "err", err)
	for _, g := range groups {
		if err := w.sendGroup(ctx, g); err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) sendGroup(ctx context.Context, g group) error {
	if len(g.reqs) <= w.m.opts.BatchSize {
		err := w.send(ctx, g.reqs)
		switch {
		case err == nil:
			w.st.BlnkTxs += int64(len(g.reqs))
			return nil
		case blnk.IsDuplicateReference(err) && len(g.reqs) == 1:
			w.st.AlreadyMigrated++
			return nil
		case blnk.IsDuplicateReference(err):
			// An earlier run with a smaller batch size may have written only
			// part of this group, so check each transaction on its own.
			for i := range g.reqs {
				if err := w.sendGroup(ctx, group{label: g.label, reqs: g.reqs[i : i+1]}); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("%s: %w", g.label, err)
		}
	}
	// Oversized group: atomicity cannot span calls, write in chunks.
	w.m.log.Warn("transaction has more postings than the atomic batch limit; writing in chunks", "group", g.label, "postings", len(g.reqs))
	for i := 0; i < len(g.reqs); i += w.m.opts.BatchSize {
		end := min(i+w.m.opts.BatchSize, len(g.reqs))
		chunk := group{label: g.label, reqs: g.reqs[i:end]}
		if err := w.sendGroup(ctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) send(ctx context.Context, reqs []blnk.TransactionRequest) error {
	if len(reqs) == 1 {
		_, err := w.m.dst.CreateTransaction(ctx, reqs[0])
		return err
	}
	_, err := w.m.dst.CreateBulk(ctx, blnk.BulkRequest{Transactions: reqs, Atomic: true, RunAsync: false, SkipQueue: true})
	return err
}

// overlayStore reads through to a base store but keeps writes in memory.
// It backs dry runs so that they never modify the real state file.
type overlayStore struct {
	base state.Store
	mem  *state.Memory
}

func (o *overlayStore) Ledger(name string) (string, bool) {
	if id, ok := o.mem.Ledger(name); ok {
		return id, ok
	}
	return o.base.Ledger(name)
}
func (o *overlayStore) PutLedger(name, id string) error { return o.mem.PutLedger(name, id) }
func (o *overlayStore) Balance(k state.BalanceKey) (state.BalanceRecord, bool) {
	if r, ok := o.mem.Balance(k); ok {
		return r, ok
	}
	return o.base.Balance(k)
}
func (o *overlayStore) PutBalance(r state.BalanceRecord) error { return o.mem.PutBalance(r) }
func (o *overlayStore) Balances() []state.BalanceRecord {
	seen := map[string]bool{}
	var out []state.BalanceRecord
	for _, r := range o.mem.Balances() {
		seen[r.BalanceKey.String()] = true
		out = append(out, r)
	}
	for _, r := range o.base.Balances() {
		if !seen[r.BalanceKey.String()] {
			out = append(out, r)
		}
	}
	return out
}
func (o *overlayStore) Checkpoint(l, p string) (uint64, bool) {
	if id, ok := o.mem.Checkpoint(l, p); ok {
		return id, ok
	}
	return o.base.Checkpoint(l, p)
}
func (o *overlayStore) SetCheckpoint(l, p string, id uint64) error {
	return o.mem.SetCheckpoint(l, p, id)
}
func (o *overlayStore) MarkSkippedTx(l string, id uint64) error {
	return o.mem.MarkSkippedTx(l, id)
}
func (o *overlayStore) SkippedTx(l string, id uint64) bool {
	return o.mem.SkippedTx(l, id) || o.base.SkippedTx(l, id)
}
func (o *overlayStore) Flush() error { return nil }
func (o *overlayStore) Close() error { return nil }
