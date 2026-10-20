package state

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestFileRoundTripAndLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	f, err := OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFile(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	k := BalanceKey{"main", "users:1", "USD/2"}
	must(t, f.PutLedger("Wallets", "ldg_1"))
	must(t, f.PutBalance(BalanceRecord{BalanceKey: k, BalanceID: "bln_1", LedgerID: "ldg_1"}))
	must(t, f.SetCheckpoint("main", "replay", 42))
	must(t, f.Close())

	g, err := OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if id, _ := g.Ledger("Wallets"); id != "ldg_1" {
		t.Fatalf("ledger id %q", id)
	}
	if r, ok := g.Balance(k); !ok || r.BalanceID != "bln_1" {
		t.Fatalf("balance %+v", r)
	}
	if c, ok := g.Checkpoint("main", "replay"); !ok || c != 42 {
		t.Fatalf("checkpoint %d", c)
	}
	if len(g.Balances()) != 1 {
		t.Fatal("balances listing")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSkippedTxPersistsWithCheckpoint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	f, err := OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	must(t, f.MarkSkippedTx("main", 7))
	must(t, f.SetCheckpoint("main", "replay", 7))
	must(t, f.Close())

	f, err = OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SkippedTx("main", 7) || f.SkippedTx("main", 8) || f.SkippedTx("other", 7) {
		t.Fatal("skipped transactions not persisted correctly")
	}
}
