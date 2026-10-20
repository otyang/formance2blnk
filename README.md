# formance2blnk

[![CI](https://github.com/otyang/formance2blnk/actions/workflows/ci.yml/badge.svg)](https://github.com/otyang/formance2blnk/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/otyang/formance2blnk.svg)](https://pkg.go.dev/github.com/otyang/formance2blnk)
[![Release](https://img.shields.io/github/v/release/otyang/formance2blnk?sort=semver)](https://github.com/otyang/formance2blnk/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/otyang/formance2blnk)](https://goreportcard.com/report/github.com/otyang/formance2blnk)
[![Docs](https://img.shields.io/badge/docs-github%20pages-blue)](https://otyang.github.io/formance2blnk/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Migrate a [Formance Ledger](https://www.formance.com/) (v2 API) into [Blnk](https://www.blnkfinance.com/) — safely, resumably and verifiably.

`formance2blnk` is both a **CLI** and a **Go library** with zero third-party dependencies.

- **Three strategies** — full history **replay**, opening-balance **snapshot**, or **hybrid** (snapshot at a cutoff + replay after it).
- **Idempotent** — every Blnk transaction gets a deterministic reference; interrupted runs resume from a checkpoint without double-posting.
- **Atomic** — multi-posting Formance transactions are written as atomic Blnk bulk batches (up to `batch_size` postings).
- **Exact amounts** — arbitrary-precision integers end to end (`USD/2` → currency `USD`, precision `100`, `precise_amount`).
- **Flexible mapping** — glob rules route Formance addresses to Blnk ledgers, `@indicator` balances, or skip them; or plug in your own `Mapper`.
- **Reconciliation** — compares every balance (and optionally credit/debit volumes) and exits non-zero on drift, for CI gates and cutover checklists.

## Why

It was built to split a combined fiat and crypto Formance ledger in two. A surge in crypto activity could slow the fiat rails, and keeping both asset classes in one ledger widened the regulatory audit scope. The fix was full separation: fiat stays on Formance, and crypto moves with all its history to a standalone Blnk instance. `formance2blnk` is the pipeline that moves it and proves nothing was lost. See [Why formance2blnk](https://otyang.github.io/formance2blnk/why/) for the full story and a carve-out configuration.

## Documentation

📖 The full documentation lives at **[otyang.github.io/formance2blnk](https://otyang.github.io/formance2blnk/)**.

| Start here | Guides | Reference |
| --- | --- | --- |
| [Why formance2blnk](https://otyang.github.io/formance2blnk/docs/why/) | [Concepts](https://otyang.github.io/formance2blnk/docs/concepts/) | [Configuration](https://otyang.github.io/formance2blnk/docs/configuration/) |
| [Getting started](https://otyang.github.io/formance2blnk/docs/getting-started/) | [Strategies](https://otyang.github.io/formance2blnk/docs/strategies/) | [CLI](https://otyang.github.io/formance2blnk/docs/cli/) |
| [Examples](https://otyang.github.io/formance2blnk/docs/examples/) | [Mapping](https://otyang.github.io/formance2blnk/docs/mapping/) | [Go library](https://otyang.github.io/formance2blnk/docs/library/) |
| [Troubleshooting](https://otyang.github.io/formance2blnk/docs/troubleshooting/) | [Reconciliation](https://otyang.github.io/formance2blnk/docs/reconciliation/) | [Compatibility](https://otyang.github.io/formance2blnk/docs/compatibility/) |
| [Changelog](https://otyang.github.io/formance2blnk/docs/changelog/) | [Cutover runbook](https://otyang.github.io/formance2blnk/docs/cutover/) | [API on pkg.go.dev](https://pkg.go.dev/github.com/otyang/docs/formance2blnk) |

## Install

```bash
# Go toolchain (1.22+)
go install github.com/otyang/formance2blnk/cmd/formance2blnk@latest

# Docker (linux/amd64 and linux/arm64)
docker run --rm ghcr.io/otyang/formance2blnk:latest help

# Library
go get github.com/otyang/formance2blnk@latest
```

Prebuilt binaries for Linux, macOS and Windows (amd64/arm64) with checksums are attached to every
[GitHub release](https://github.com/otyang/formance2blnk/docs/releases).

## Quick start (CLI)

```bash
formance2blnk init > formance2blnk.json        # edit URLs, ledgers and mapping
formance2blnk validate                          # check the config
formance2blnk plan                              # dry run: what would be created
formance2blnk migrate                           # run it (Ctrl-C safe, re-runnable)
formance2blnk reconcile -volumes                # prove the ledgers match
```

## Quick start (library)

```go
package main

import (
	"context"
	"fmt"
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
	src, _ := formance.New(formance.Options{BaseURL: "http://localhost:3068"})
	dst, _ := blnk.New(blnk.Options{BaseURL: "http://localhost:5001", APIKey: os.Getenv("BLNK_API_KEY")})
	mapper, _ := mapping.New(mapping.Config{Rules: []mapping.Rule{
		{Match: "platform:fees", Indicator: "@Fees"},
		{Match: "users:*:wallet", Ledger: "Customer Wallets"},
	}})
	store, err := state.OpenFile("state.json")
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	m, _ := migrator.New(src, dst, migrator.Options{Ledgers: []string{"main"}, Mapper: mapper, Store: store})
	if _, err := m.Replay(ctx); err != nil {
		log.Fatal(err)
	}
	report, _ := m.Reconcile(ctx, migrator.ReconcileOptions{CheckVolumes: true})
	fmt.Println("ledgers match:", report.OK())
}
```

Try it without any servers: `go run ./examples/quickstart`.

## How Formance maps to Blnk

| Formance | Blnk |
| --- | --- |
| Ledger | Ledger (by name, or routed by rules) |
| Account × asset | Balance (Blnk balances are single-currency) |
| `world` | `@World` indicator balance (configurable) |
| Asset `USD/2` | Currency `USD`, precision `100` |
| Posting | Transaction with `precise_amount` |
| Multi-posting transaction | Atomic bulk batch |
| Transaction timestamp | `effective_date` |
| Metadata | `meta_data.formance_metadata` (+ `formance_*` provenance keys) |
| Revert | Replayed as a reversal, or both dropped (`reverts: skip`) |

## Project layout

| Package | Purpose |
| --- | --- |
| [`migrator`](migrator) | The engine: replay, snapshot, hybrid, reconcile |
| [`mapping`](mapping) | Account and asset mapping rules |
| [`formance`](formance) | Read-only Formance Ledger v2 client (bearer or OAuth2) |
| [`blnk`](blnk) | Blnk client (ledgers, balances, bulk transactions) |
| [`state`](state) | Resumable progress store (atomic JSON file with lock) |
| [`config`](config) | JSON config with `${ENV}` expansion |
| [`amount`](amount) | Big-integer amounts that survive JSON |
| [`internal/httpx`](internal/httpx) | Shared HTTP client: retries, backoff, rate limiting |
| [`internal/fakes`](internal/fakes) | In-memory Formance and Blnk fakes for tests and examples |
| [`cmd/formance2blnk`](cmd/formance2blnk) | The CLI |

## Compatibility

Built against the Formance Ledger **v2** REST API and the Blnk Core API. Both evolve — run `plan` and a reconciliation against a staging copy first. See [Compatibility](https://otyang.github.io/formance2blnk/docs/compatibility/) for the exact endpoints used.

## Contributing

Issues and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Run `make check` before submitting.

## License

[MIT](LICENSE). Not affiliated with Formance or Blnk Finance.
