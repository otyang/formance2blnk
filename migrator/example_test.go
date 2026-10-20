package migrator_test

import (
	"context"
	"fmt"
	"time"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/internal/fakes"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/migrator"
	"github.com/otyang/formance2blnk/state"
)

// This example replays a small Formance ledger into Blnk and reconciles the
// result. In-memory fakes stand in for the real servers.
func Example() {
	fm, bk := fakes.NewFormance(), fakes.NewBlnk()
	defer fm.Close()
	defer bk.Close()
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	fm.Post("main", t0, []formance.Posting{{Source: "world", Destination: "users:1:wallet", Amount: fakes.Amt(10000), Asset: "USD/2"}}, nil)
	fm.Post("main", t0.Add(time.Hour), []formance.Posting{
		{Source: "users:1:wallet", Destination: "users:2:wallet", Amount: fakes.Amt(2500), Asset: "USD/2"},
		{Source: "users:1:wallet", Destination: "platform:fees", Amount: fakes.Amt(100), Asset: "USD/2"},
	}, nil)

	src, _ := formance.New(formance.Options{BaseURL: fm.URL()})
	dst, _ := blnk.New(blnk.Options{BaseURL: bk.URL()})
	mapper, _ := mapping.New(mapping.Config{Rules: []mapping.Rule{
		{Match: "platform:fees", Indicator: "@Fees"},
		{Match: "users:*:wallet", Ledger: "Customer Wallets"},
	}})

	m, _ := migrator.New(src, dst, migrator.Options{
		Ledgers: []string{"main"},
		Mapper:  mapper,
		Store:   state.NewMemory(), // use state.OpenFile in production
	})

	stats, err := m.Replay(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("blnk transactions:", stats.BlnkTxs)
	fmt.Println("balances created:", stats.BalancesCreated)

	report, _ := m.Reconcile(context.Background(), migrator.ReconcileOptions{CheckVolumes: true})
	fmt.Println("reconciled:", report.OK(), "targets:", report.Checked)
	// Output:
	// blnk transactions: 3
	// balances created: 2
	// reconciled: true targets: 4
}

// Transform lets you edit or drop each Blnk transaction before it is sent.
func ExampleOptions_transform() {
	opts := migrator.Options{
		Ledgers: []string{"main"},
		Transform: func(req *blnk.TransactionRequest, tx migrator.TxContext) (bool, error) {
			// Drop dust postings and tag the rest with a migration batch.
			if req.PreciseAmount.Cmp(fakes.Amt(1)) <= 0 {
				return false, nil
			}
			req.MetaData["migration_batch"] = "2026-10"
			return true, nil
		},
	}
	fmt.Println(opts.Transform != nil)
	// Output: true
}

// Reference shows the deterministic reference format used for idempotency.
func ExampleReference() {
	fmt.Println(migrator.Reference("fm", "main", 42, 1))
	fmt.Println(migrator.OpeningReference("fm", "main", "users:1:wallet", "USD/2"))
	// Output:
	// fm:main:42:1
	// fm-open:main:users:1:wallet:USD/2
}
