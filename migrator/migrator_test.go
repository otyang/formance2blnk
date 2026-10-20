package migrator_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/internal/fakes"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/migrator"
	"github.com/otyang/formance2blnk/state"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func p(src, dst string, amt int64, asset string) formance.Posting {
	return formance.Posting{Source: src, Destination: dst, Amount: fakes.Amt(amt), Asset: asset}
}

// seed builds a realistic ledger: deposits, transfers, a multi-posting
// payment with a fee, a second asset, a revert, a withdrawal and a long
// tail of random activity. It returns the number of Formance postings and
// the hybrid cutoff time.
func seed(f *fakes.Formance, ledger string, n int) (postings int, cutoff time.Time) {
	t := base
	next := func() time.Time { t = t.Add(time.Hour); return t }
	add := func(ps ...formance.Posting) uint64 {
		postings += len(ps)
		return f.Post(ledger, next(), ps, map[string]string{"description": "seed"})
	}
	add(p("world", "users:1:wallet", 10000, "USD/2"))
	add(p("world", "users:2:wallet", 5000, "USD/2"))
	add(p("users:1:wallet", "users:2:wallet", 2500, "USD/2"), p("users:1:wallet", "platform:fees", 100, "USD/2"))
	add(p("world", "users:1:wallet", 100000000, "BTC/8"))
	rev := add(p("users:2:wallet", "users:1:wallet", 300, "USD/2"))
	postings++
	f.Revert(ledger, rev, next())
	add(p("users:2:wallet", "world", 1000, "USD/2"))

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < n; i++ {
		a := fmt.Sprintf("users:%d:wallet", rng.Intn(20))
		b := fmt.Sprintf("users:%d:wallet", rng.Intn(20))
		if a == b {
			b = "platform:fees"
		}
		switch rng.Intn(4) {
		case 0:
			add(p("world", a, int64(rng.Intn(100000)+1), "USD/2"))
		case 1:
			add(p(a, b, int64(rng.Intn(5000)+1), "USD/2"), p(a, "platform:fees", 10, "USD/2"))
		case 2:
			add(p(a, b, int64(rng.Intn(5000)+1), "NGN/2"))
		default:
			add(p(a, "world", int64(rng.Intn(3000)+1), "USD/2"))
		}
		if i == n/2 {
			cutoff = t.Add(30 * time.Minute)
		}
	}
	return postings, cutoff
}

type env struct {
	f   *fakes.Formance
	b   *fakes.Blnk
	src *formance.Client
	dst *blnk.Client
	m   mapping.Mapper
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f, b := fakes.NewFormance(), fakes.NewBlnk()
	t.Cleanup(f.Close)
	t.Cleanup(b.Close)
	src, err := formance.New(formance.Options{BaseURL: f.URL(), PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := blnk.New(blnk.Options{BaseURL: b.URL(), MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mapping.New(mapping.Config{Rules: []mapping.Rule{
		{Match: "platform:fees", Indicator: "@Fees"},
		{Match: "users:*:wallet", Ledger: "Wallets"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &env{f: f, b: b, src: src, dst: dst, m: m}
}

func (e *env) migrator(t *testing.T, store state.Store, mut ...func(*migrator.Options)) *migrator.Migrator {
	t.Helper()
	opts := migrator.Options{Ledgers: []string{"main"}, Mapper: e.m, Store: store, BatchSize: 20}
	for _, f := range mut {
		f(&opts)
	}
	m, err := migrator.New(e.src, e.dst, opts)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustReconcile(t *testing.T, m *migrator.Migrator, volumes bool) migrator.Report {
	t.Helper()
	rep, err := m.Reconcile(context.Background(), migrator.ReconcileOptions{CheckVolumes: volumes})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		for _, mm := range rep.Mismatches {
			t.Errorf("mismatch: %+v", mm)
		}
		t.FailNow()
	}
	if rep.Checked == 0 {
		t.Fatal("reconcile checked nothing")
	}
	return rep
}

func TestReplayMirrorsFormanceExactly(t *testing.T) {
	e := newEnv(t)
	postings, _ := seed(e.f, "main", 300)
	m := e.migrator(t, state.NewMemory())

	st, err := m.Replay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.b.TransactionCount(); got != postings {
		t.Fatalf("blnk has %d transactions, want %d", got, postings)
	}
	if st.BlnkTxs != int64(postings) || st.LedgersCreated != 1 {
		t.Fatalf("unexpected stats %+v", st)
	}
	rep := mustReconcile(t, m, true)
	t.Logf("replayed %d postings, reconciled %d targets", postings, rep.Checked)

	// Original metadata and effective dates are carried over.
	tx, ok := e.b.Transaction(migrator.Reference("fm", "main", 2, 1))
	if !ok || tx.Destination != "@Fees" || tx.EffectiveDate == nil || !tx.EffectiveDate.Equal(base.Add(3*time.Hour)) {
		t.Fatalf("unexpected fee transaction %+v", tx)
	}
	if tx.Precision.String() != "100" || tx.MetaData["formance_tx_id"] != float64(2) && tx.MetaData["formance_tx_id"] != uint64(2) {
		t.Fatalf("unexpected precision/metadata %+v", tx)
	}

	// Running again is a no-op.
	st2, err := m.Replay(context.Background())
	if err != nil || st2.BlnkTxs != 0 || e.b.TransactionCount() != postings {
		t.Fatalf("second run not idempotent: %+v %v", st2, err)
	}

	// New Formance activity is picked up incrementally (catch-up pass).
	e.f.Post("main", base.Add(10000*time.Hour), []formance.Posting{p("world", "users:99:wallet", 7, "USD/2")}, nil)
	st3, err := m.Replay(context.Background())
	if err != nil || st3.BlnkTxs != 1 || st3.FormanceTxs != 1 {
		t.Fatalf("catch-up: %+v %v", st3, err)
	}
	mustReconcile(t, m, true)
}

func TestResumeAfterCrashWithFileState(t *testing.T) {
	e := newEnv(t)
	postings, _ := seed(e.f, "main", 200)
	path := filepath.Join(t.TempDir(), "state.json")

	store, err := state.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("simulated crash")
	m := e.migrator(t, store, func(o *migrator.Options) {
		o.Transform = func(_ *blnk.TransactionRequest, c migrator.TxContext) (bool, error) {
			if c.Transaction.ID == 120 {
				return false, boom
			}
			return true, nil
		}
	})
	if _, err := m.Replay(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("expected crash, got %v", err)
	}
	store.Close()
	partial := e.b.TransactionCount()
	if partial == 0 || partial >= postings {
		t.Fatalf("expected partial progress, got %d/%d", partial, postings)
	}

	store, err = state.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m = e.migrator(t, store)
	if _, err := m.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.b.TransactionCount(); got != postings {
		t.Fatalf("after resume blnk has %d, want %d", got, postings)
	}
	mustReconcile(t, m, true)
}

func TestLostResponseIsHandledAsAlreadyMigrated(t *testing.T) {
	e := newEnv(t)
	postings, _ := seed(e.f, "main", 120)
	e.b.LoseResponseAt = 3 // third successful write returns 502
	m := e.migrator(t, state.NewMemory())
	st, err := m.Replay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.AlreadyMigrated == 0 {
		t.Fatalf("expected duplicates to be detected, stats %+v", st)
	}
	if got := e.b.TransactionCount(); got != postings {
		t.Fatalf("blnk has %d, want %d", got, postings)
	}
	mustReconcile(t, m, true)
}

func TestDescendingServerFallback(t *testing.T) {
	e := newEnv(t)
	e.f.IgnoreReverse = true
	postings, _ := seed(e.f, "main", 80)
	m := e.migrator(t, state.NewMemory())
	if _, err := m.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.b.TransactionCount() != postings {
		t.Fatal("wrong transaction count")
	}
	mustReconcile(t, m, true)

	// Resume against a descending server stops paging at the checkpoint.
	e.f.Post("main", base.Add(9999*time.Hour), []formance.Posting{p("world", "users:1:wallet", 1, "USD/2")}, nil)
	before := e.f.Requests
	if _, err := m.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.f.Requests-before > 2 {
		t.Fatalf("resume paged %d times, expected early stop", e.f.Requests-before)
	}
	mustReconcile(t, m, true)
}

func TestSnapshot(t *testing.T) {
	e := newEnv(t)
	seed(e.f, "main", 150)
	m := e.migrator(t, state.NewMemory())
	st, err := m.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.BlnkTxs == 0 || st.Skipped["world"] == 0 {
		t.Fatalf("unexpected stats %+v", st)
	}
	// Net balances (including @World and @Fees) must match.
	mustReconcile(t, m, false)
	// Re-running is skipped via the completion marker.
	st2, _ := m.Snapshot(context.Background())
	if st2.BlnkTxs != 0 {
		t.Fatal("snapshot re-ran")
	}
}

func TestHybrid(t *testing.T) {
	e := newEnv(t)
	postings, cutoff := seed(e.f, "main", 200)
	m := e.migrator(t, state.NewMemory())
	st, err := m.Hybrid(context.Background(), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if st.Skipped["before_cutoff"] == 0 || st.BlnkTxs >= int64(postings) {
		t.Fatalf("hybrid did not cut history: %+v", st)
	}
	mustReconcile(t, m, false)
}

func TestHybridWithRevertSkip(t *testing.T) {
	e := newEnv(t)
	_, cutoff := seed(e.f, "main", 60)
	// A transaction before the cutoff, reverted after it: the snapshot holds
	// the original, so the revert must still be replayed.
	id := e.f.Post("main", cutoff.Add(-time.Minute), []formance.Posting{p("users:1:wallet", "users:2:wallet", 77, "USD/2")}, nil)
	e.f.Revert("main", id, base.Add(5000*time.Hour))
	m := e.migrator(t, state.NewMemory(), func(o *migrator.Options) { o.Reverts = migrator.RevertSkip })
	if _, err := m.Hybrid(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	mustReconcile(t, m, false)
}

func TestRevertSkipKeepsNetBalances(t *testing.T) {
	e := newEnv(t)
	postings, _ := seed(e.f, "main", 50)
	m := e.migrator(t, state.NewMemory(), func(o *migrator.Options) { o.Reverts = migrator.RevertSkip })
	st, err := m.Replay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Skipped["reverted"] != 1 || st.Skipped["revert"] != 1 || e.b.TransactionCount() != postings-2 {
		t.Fatalf("unexpected revert handling: %+v (blnk %d)", st, e.b.TransactionCount())
	}
	mustReconcile(t, m, false)
}

func TestRevertSkipCatchUpAfterLateRevert(t *testing.T) {
	e := newEnv(t)
	seed(e.f, "main", 20)
	id := e.f.Post("main", base.Add(9000*time.Hour), []formance.Posting{p("users:1:wallet", "users:2:wallet", 77, "USD/2")}, nil)
	store := state.NewMemory()
	skip := func(o *migrator.Options) { o.Reverts = migrator.RevertSkip }
	if _, err := e.migrator(t, store, skip).Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Reverted only after the original was migrated: the catch-up run must
	// replay the revert, or Blnk keeps the original.
	e.f.Revert("main", id, base.Add(9001*time.Hour))
	m := e.migrator(t, store, skip)
	st, err := m.Replay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Skipped["revert"] != 0 || st.BlnkTxs != 1 {
		t.Fatalf("revert of a migrated transaction not replayed: %+v", st)
	}
	mustReconcile(t, m, false)
}

func TestRevertSkipRevertOfRevert(t *testing.T) {
	e := newEnv(t)
	seed(e.f, "main", 20)
	id := e.f.Post("main", base.Add(9000*time.Hour), []formance.Posting{p("users:1:wallet", "users:2:wallet", 77, "USD/2")}, nil)
	rev := e.f.Revert("main", id, base.Add(9001*time.Hour))
	e.f.Revert("main", rev, base.Add(9002*time.Hour))
	m := e.migrator(t, state.NewMemory(), func(o *migrator.Options) { o.Reverts = migrator.RevertSkip })
	if _, err := m.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Formance nets to the original transaction, so Blnk must too.
	mustReconcile(t, m, false)
}

func TestDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	postings, _ := seed(e.f, "main", 40)
	store := state.NewMemory()
	m := e.migrator(t, store, func(o *migrator.Options) { o.DryRun = true })
	st, err := m.Replay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.BlnkTxs != int64(postings) || e.b.TransactionCount() != 0 || e.b.LedgerCount() != 0 {
		t.Fatalf("dry run wrote data or miscounted: %+v", st)
	}
	if len(store.Balances()) != 0 {
		t.Fatal("dry run modified the store")
	}
}

func TestSkippedAccountPolicies(t *testing.T) {
	e := newEnv(t)
	e.f.Post("main", base, []formance.Posting{p("world", "tmp:1", 5, "USD/2")}, nil)
	e.f.Post("main", base.Add(time.Hour), []formance.Posting{p("world", "users:1:wallet", 5, "USD/2")}, nil)
	m, _ := mapping.New(mapping.Config{Rules: []mapping.Rule{{Match: "tmp:*", Skip: true}}})
	e.m = m

	if _, err := e.migrator(t, state.NewMemory()).Replay(context.Background()); err == nil {
		t.Fatal("expected error for skipped account")
	}
	st, err := e.migrator(t, state.NewMemory(), func(o *migrator.Options) {
		o.OnSkippedAccount = migrator.SkippedAccountDrop
	}).Replay(context.Background())
	if err != nil || st.Skipped["skipped_account"] != 1 || st.BlnkTxs != 1 {
		t.Fatalf("drop policy: %+v %v", st, err)
	}
}

func TestCopyAccountMetadata(t *testing.T) {
	e := newEnv(t)
	e.f.Post("main", base, []formance.Posting{p("world", "users:1:wallet", 5, "USD/2")}, nil)
	e.f.SetAccountMetadata("main", "users:1:wallet", map[string]string{"kyc": "tier2"})
	store := state.NewMemory()
	m := e.migrator(t, store, func(o *migrator.Options) { o.CopyAccountMetadata = true })
	if _, err := m.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs := store.Balances()
	if len(recs) != 1 {
		t.Fatalf("expected 1 balance, got %d", len(recs))
	}
	b, err := e.dst.GetBalance(context.Background(), recs[0].BalanceID)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := b.MetaData["formance_metadata"].(map[string]any)
	if md["kyc"] != "tier2" || b.MetaData["formance_address"] != "users:1:wallet" {
		t.Fatalf("metadata not copied: %+v", b.MetaData)
	}
}

func TestReconcileDetectsDrift(t *testing.T) {
	e := newEnv(t)
	seed(e.f, "main", 20)
	m := e.migrator(t, state.NewMemory())
	if _, err := m.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Activity in Formance that was not migrated must be reported.
	e.f.Post("main", base.Add(9000*time.Hour), []formance.Posting{p("users:1:wallet", "users:2:wallet", 1, "USD/2")}, nil)
	e.f.Post("main", base.Add(9001*time.Hour), []formance.Posting{p("world", "users:777:wallet", 1, "USD/2")}, nil)
	rep, err := m.Reconcile(context.Background(), migrator.ReconcileOptions{CheckVolumes: true})
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]int{}
	for _, mm := range rep.Mismatches {
		fields[mm.Field]++
	}
	if fields["missing"] != 1 || fields["balance"] < 2 {
		t.Fatalf("drift not detected: %+v", rep.Mismatches)
	}
}

func TestOptionsValidation(t *testing.T) {
	e := newEnv(t)
	bad := []migrator.Options{
		{},
		{Ledgers: []string{"main"}, BatchSize: 51},
		{Ledgers: []string{"main"}, Reverts: "nope"},
		{Ledgers: []string{"main"}, OnSkippedAccount: "nope"},
	}
	for i, o := range bad {
		if _, err := migrator.New(e.src, e.dst, o); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

// failingDst fails CreateTransaction for one reference, simulating a crash
// in the middle of a Formance transaction written posting by posting.
type failingDst struct {
	migrator.Destination
	ref string
}

func (d failingDst) CreateTransaction(ctx context.Context, req blnk.TransactionRequest) (blnk.Transaction, error) {
	if req.Reference == d.ref {
		return blnk.Transaction{}, errors.New("simulated crash")
	}
	return d.Destination.CreateTransaction(ctx, req)
}

func TestResumeWithLargerBatchAfterPartialGroup(t *testing.T) {
	e := newEnv(t)
	postings, _ := seed(e.f, "main", 10)
	store := state.NewMemory()

	// First run sends postings one by one and dies after posting 0 of tx 2.
	m1, err := migrator.New(e.src, failingDst{Destination: e.dst, ref: migrator.Reference("fm", "main", 2, 1)},
		migrator.Options{Ledgers: []string{"main"}, Mapper: e.m, Store: store, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m1.Replay(context.Background()); err == nil {
		t.Fatal("expected simulated crash")
	}
	if _, ok := e.b.Transaction(migrator.Reference("fm", "main", 2, 0)); !ok {
		t.Fatal("posting 0 of tx 2 should have been written")
	}

	// Second run uses atomic batches: tx 2 is a partly written group.
	m2 := e.migrator(t, store)
	if _, err := m2.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.b.Transaction(migrator.Reference("fm", "main", 2, 1)); !ok {
		t.Fatal("posting 1 of tx 2 was lost")
	}
	if got := e.b.TransactionCount(); got != postings {
		t.Fatalf("blnk has %d transactions, want %d", got, postings)
	}
	mustReconcile(t, m2, true)
}

// cancelingDst cancels the run on the first balance lookup.
type cancelingDst struct {
	migrator.Destination
	cancel context.CancelFunc
}

func (d cancelingDst) GetBalance(ctx context.Context, id string) (blnk.Balance, error) {
	d.cancel()
	return blnk.Balance{}, ctx.Err()
}

func TestReconcileCancelledIsAnError(t *testing.T) {
	e := newEnv(t)
	seed(e.f, "main", 10)
	store := state.NewMemory()
	if _, err := e.migrator(t, store).Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := migrator.New(e.src, cancelingDst{Destination: e.dst, cancel: cancel},
		migrator.Options{Ledgers: []string{"main"}, Mapper: e.m, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reconcile(ctx, migrator.ReconcileOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestHybridRejectsChangedCutoff(t *testing.T) {
	e := newEnv(t)
	_, cutoff := seed(e.f, "main", 40)
	store := state.NewMemory()
	m := e.migrator(t, store)
	if _, err := m.Hybrid(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	// Same cutoff: resumable no-op.
	if _, err := m.Hybrid(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	// A later cutoff would drop everything between the two; an earlier one
	// would replay transactions already inside the snapshot.
	for _, c := range []time.Time{cutoff.Add(10 * time.Hour), cutoff.Add(-10 * time.Hour)} {
		before := e.b.TransactionCount()
		if _, err := m.Hybrid(context.Background(), c); err == nil {
			t.Fatalf("cutoff %s: expected an error", c)
		}
		if e.b.TransactionCount() != before {
			t.Fatalf("cutoff %s: transactions were written", c)
		}
	}
	mustReconcile(t, m, false)
}

func TestHybridRejectsFutureCutoff(t *testing.T) {
	e := newEnv(t)
	seed(e.f, "main", 5)
	m := e.migrator(t, state.NewMemory())
	if _, err := m.Hybrid(context.Background(), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expected an error for a cutoff in the future")
	}
	if e.b.TransactionCount() != 0 {
		t.Fatal("transactions were written")
	}
}
