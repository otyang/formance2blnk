// Quickstart runs a complete migration against in-memory fakes of Formance
// and Blnk, so you can see the whole flow without any servers:
//
//	go run ./examples/quickstart
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/internal/fakes"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/migrator"
	"github.com/otyang/formance2blnk/state"
)

func main() {
	ctx := context.Background()
	fm, bk := fakes.NewFormance(), fakes.NewBlnk()
	defer fm.Close()
	defer bk.Close()

	// 1. A small Formance history: deposits, a payment with a fee, a
	//    refund (revert) and a withdrawal.
	t0 := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	post := func(h int, ps ...formance.Posting) uint64 {
		return fm.Post("main", t0.Add(time.Duration(h)*time.Hour), ps, map[string]string{"description": "demo"})
	}
	usd := func(src, dst string, v int64) formance.Posting {
		return formance.Posting{Source: src, Destination: dst, Amount: fakes.Amt(v), Asset: "USD/2"}
	}
	post(0, usd("world", "users:alice:wallet", 50_00))
	post(1, usd("world", "users:bob:wallet", 20_00))
	pay := post(2, usd("users:alice:wallet", "merchants:acme:main", 12_50), usd("users:alice:wallet", "platform:fees", 50))
	fm.Revert("main", pay, t0.Add(3*time.Hour))
	post(4, usd("users:bob:wallet", "world", 5_00))

	// 2. Wire up clients, mapping and a state store.
	src, _ := formance.New(formance.Options{BaseURL: fm.URL()})
	dst, _ := blnk.New(blnk.Options{BaseURL: bk.URL()})
	mapper, err := mapping.New(mapping.Config{Rules: []mapping.Rule{
		{Match: "platform:fees", Indicator: "@Fees"},
		{Match: "users:*:wallet", Ledger: "Customer Wallets"},
		{Match: "merchants:**", Ledger: "Merchants"},
	}})
	if err != nil {
		panic(err)
	}
	m, err := migrator.New(src, dst, migrator.Options{
		Ledgers: []string{"main"},
		Mapper:  mapper,
		Store:   state.NewMemory(),
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		panic(err)
	}

	// 3. Plan-free replay, then reconcile.
	st, err := m.Replay(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Replayed %d Formance transactions into %d Blnk transactions (%d ledgers, %d balances)\n",
		st.FormanceTxs, st.BlnkTxs, st.LedgersCreated, st.BalancesCreated)

	rep, err := m.Reconcile(ctx, migrator.ReconcileOptions{CheckVolumes: true})
	if err != nil {
		panic(err)
	}
	fmt.Printf("Reconciliation: ok=%v checked=%d matched=%d\n", rep.OK(), rep.Checked, rep.Matched)

	// 4. Re-running is safe: nothing is written twice.
	again, _ := m.Replay(ctx)
	fmt.Printf("Second run wrote %d transactions\n", again.BlnkTxs)
}
