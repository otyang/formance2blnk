// Production shows how to embed the migrator in your own program against
// real servers, with a durable state file, graceful shutdown, a progress
// callback and a strict reconciliation gate.
//
//	FORMANCE_URL=https://ORG.ENV.formance.cloud/api/ledger \
//	FORMANCE_CLIENT_ID=... FORMANCE_CLIENT_SECRET=... \
//	BLNK_URL=https://blnk.internal BLNK_API_KEY=... \
//	go run ./examples/production -ledger main
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/migrator"
	"github.com/otyang/formance2blnk/state"
)

func main() {
	ledger := flag.String("ledger", "main", "Formance ledger to migrate")
	statePath := flag.String("state", "migration-state.json", "state file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger, *ledger, *statePath); err != nil {
		logger.Error("migration failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, ledger, statePath string) error {
	src, err := formance.New(formance.Options{
		BaseURL:           os.Getenv("FORMANCE_URL"),
		ClientID:          os.Getenv("FORMANCE_CLIENT_ID"),
		ClientSecret:      os.Getenv("FORMANCE_CLIENT_SECRET"),
		PageSize:          100,
		RequestsPerSecond: 20, // be gentle with the source
	})
	if err != nil {
		return err
	}
	dst, err := blnk.New(blnk.Options{BaseURL: os.Getenv("BLNK_URL"), APIKey: os.Getenv("BLNK_API_KEY")})
	if err != nil {
		return err
	}
	mapper, err := mapping.New(mapping.Config{
		Rules: []mapping.Rule{
			{Match: "platform:fees", Indicator: "@Fees"},
			{Match: "platform:float:*", Indicator: "@Float-{1}"},
			{Match: "users:*:wallet", Ledger: "Customer Wallets"},
			{Match: "merchants:**", Ledger: "Merchants"},
		},
	})
	if err != nil {
		return err
	}

	store, err := state.OpenFile(statePath)
	if err != nil {
		return err
	}
	defer store.Close() // flushes and releases the lock, also on Ctrl-C

	m, err := migrator.New(src, dst, migrator.Options{
		Ledgers:             []string{ledger},
		Mapper:              mapper,
		Store:               store,
		Logger:              logger,
		CopyAccountMetadata: true,
		Progress: func(s migrator.Stats) {
			logger.Info("progress", "ledger", s.Ledger, "formance_txs", s.FormanceTxs, "blnk_txs", s.BlnkTxs)
		},
	})
	if err != nil {
		return err
	}

	stats, err := m.Replay(ctx)
	logger.Info("replay finished", "stats", stats)
	if err != nil {
		return err // re-run the program to resume from the checkpoint
	}

	report, err := m.Reconcile(ctx, migrator.ReconcileOptions{CheckVolumes: true})
	if err != nil {
		return err
	}
	if !report.OK() {
		for _, mm := range report.Mismatches {
			logger.Error("mismatch", "target", mm.Target, "accounts", mm.Accounts, "field", mm.Field,
				"expected", mm.Expected, "actual", mm.Actual)
		}
		return fmt.Errorf("%d reconciliation mismatches", len(report.Mismatches))
	}
	logger.Info("ledgers match", "checked", report.Checked)
	return nil
}
