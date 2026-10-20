# Go library

```bash
go get github.com/otyang/formance2blnk
```

The module has no third-party dependencies. API docs: [pkg.go.dev](https://pkg.go.dev/github.com/otyang/formance2blnk).

## Packages

| Package | Purpose |
| --- | --- |
| `migrator` | The engine: `Replay`, `ReplaySince`, `Snapshot`, `SnapshotAsOf`, `Hybrid`, `Reconcile`. |
| `mapping` | `Mapper` interface and the rules-based implementation. |
| `formance` | Read-only Formance Ledger v2 client. |
| `blnk` | Blnk client: ledgers, balances, transactions, bulk. |
| `state` | `Store` interface; `OpenFile` (durable, locked) and `NewMemory`. |
| `config` | Load the CLI's JSON configuration and build clients/options from it. |
| `amount` | Arbitrary-precision amounts that round-trip through JSON. |

## Minimal program

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/migrator"
	"github.com/otyang/formance2blnk/state"
)

func main() {
	ctx := context.Background()
	src, err := formance.New(formance.Options{BaseURL: "http://localhost:3068"})
	if err != nil {
		log.Fatal(err)
	}
	dst, err := blnk.New(blnk.Options{BaseURL: "http://localhost:5001", APIKey: os.Getenv("BLNK_API_KEY")})
	if err != nil {
		log.Fatal(err)
	}
	mapper, err := mapping.New(mapping.Config{Rules: []mapping.Rule{
		{Match: "users:*:wallet", Ledger: "Customer Wallets"},
	}})
	if err != nil {
		log.Fatal(err)
	}
	store, err := state.OpenFile("state.json")
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	m, err := migrator.New(src, dst, migrator.Options{
		Ledgers: []string{"main"},
		Mapper:  mapper,
		Store:   store,
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := m.Replay(ctx); err != nil {
		log.Fatal(err)
	}
	rep, err := m.Reconcile(ctx, migrator.ReconcileOptions{CheckVolumes: true})
	if err != nil || !rep.OK() {
		log.Fatalf("reconciliation failed: %v %+v", err, rep.Mismatches)
	}
}
```

## Reusing the CLI configuration

```go
cfg, _ := config.Load("formance2blnk.json")
src, _ := cfg.FormanceClient()
dst, _ := cfg.BlnkClient()
opts, _ := cfg.MigratorOptions(slog.Default())
store, _ := cfg.OpenState()
defer store.Close()
opts.Store = store
m, _ := migrator.New(src, dst, opts)
```

## Hooks

### Transform

Edit or drop every Blnk transaction before it is sent:

```go
opts.Transform = func(req *blnk.TransactionRequest, tx migrator.TxContext) (bool, error) {
	if tx.Transaction.Metadata["test"] == "true" {
		return false, nil // drop test traffic
	}
	req.Description = "Migrated: " + req.Description
	req.MetaData["migration_batch"] = "2026-10"
	return true, nil
}
```

`TxContext` gives the source ledger, the full Formance transaction, and the posting with its index. For snapshot opening balances, `PostingIndex` is `-1`, `Transaction` is empty and `Posting` is synthesised from the volume.

!!! warning
    Don't change `Reference`, amounts, source or destination in a `Transform` unless you fully understand the consequences: references are what make re-runs safe, and the others affect reconciliation.

### Progress

```go
opts.Progress = func(s migrator.Stats) {
	slog.Info("migration progress", "ledger", s.Ledger, "formance_txs", s.FormanceTxs, "blnk_txs", s.BlnkTxs)
}
opts.ProgressEvery = 5000
```

### Custom stores

Implement `state.Store` to keep progress in Postgres, Redis or a cloud object store. The interface is small: ledger and balance ID lookups, a `Balances` listing, checkpoints per Formance ledger and phase, the transactions dropped under `reverts: skip` (`MarkSkippedTx` / `SkippedTx`), `Flush` and `Close`. Implementations must be safe for concurrent use and should make `Put*` and `SetCheckpoint` durable before returning. A `MarkSkippedTx` must be durable no later than the next `SetCheckpoint`.

### Custom sources and destinations

`migrator.New` accepts any `migrator.Source` and `migrator.Destination`. Wrap the provided clients to add tracing, or substitute your own — for example a source that reads a Formance export file instead of the API.
