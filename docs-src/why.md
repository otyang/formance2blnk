# Why formance2blnk

`formance2blnk` was built to move one line of business out of a shared Formance ledger and into its own Blnk instance, without losing a single unit of history along the way.

## The problem

A single Formance deployment held both **fiat** and **crypto** transactions. That caused two problems:

- **Shared blast radius.** A spike in crypto activity competes with core banking for the same ledger and database. Heavy trading volume could slow the fiat rails, or lock them up entirely.
- **Regulatory exposure.** Regulators increasingly expect fiat and digital-asset activity to be kept apart. Holding both in one ledger widens the audit scope and pulls the fiat rails into crypto-related scrutiny.

## The aim

Separate the two businesses completely: **no shared ledger, no shared infrastructure**. Fiat stays on Formance. Crypto, including all of its historical ledger data, moves to a dedicated, standalone environment.

## The solution

The crypto business moves to [Blnk](https://www.blnkfinance.com/). A data pipeline extracts the crypto accounts and their history from Formance and writes them into a standalone Blnk instance sized for that workload alone. Each side then scales, deploys and gets audited on its own terms.

That pipeline is `formance2blnk`. A migration like this has to meet a few non-negotiable requirements, and each one maps to a feature:

| Requirement | How formance2blnk meets it |
| --- | --- |
| Move **only** the crypto side | [Mapping rules](mapping.md) route crypto addresses into Blnk and `skip` everything else. |
| Never lose or double-count value | Exact integer amounts (`BTC/8`, `ETH/18`, …), deterministic references and idempotent writes. See [Concepts](concepts.md). |
| Keep the audit trail, or start clean | Full history [replay](strategies.md#replay), an opening-balance [snapshot](strategies.md#snapshot), or a [hybrid](strategies.md#hybrid) of the two. |
| Survive a long-running pipeline | Checkpointed and resumable: interrupt it and run the same command again. |
| Prove the split is correct | [Reconciliation](reconciliation.md) compares every migrated balance and exits non-zero on drift, ready for a [cutover](cutover.md) gate. |

## Carving crypto out of a shared ledger

Skip rules match on the account **address**, so the carve-out works best when crypto accounts share an address prefix. In this example that prefix is `crypto:`:

```json
{
  "mapping": {
    "rules": [
      { "match": "world", "indicator": "@World" },
      { "match": "fiat:settlement:crypto", "indicator": "@FiatSettlement" },
      { "match": "crypto:users:*:wallet", "ledger": "Crypto Wallets" },
      { "match": "crypto:treasury", "indicator": "@CryptoTreasury" },
      { "match": "crypto:**", "ledger": "Crypto Operations" },
      { "match": "**", "skip": true }
    ]
  },
  "migration": { "on_skipped_account": "error" }
}
```

Three details matter:

1. **Map `world` before the catch-all.** Rules are checked before the built-in `world` default, so without its own rule `world` would match `**` and be skipped.
2. **Give cross-rail flows a boundary account.** A transaction that moves value between a crypto account and a fiat account, such as a fiat on-ramp, touches both sides. Map the fiat side of those flows to an indicator like `@FiatSettlement` so the crypto balances stay complete in Blnk.
3. **Keep `on_skipped_account: error` for the first run.** Any remaining transaction that crosses into a skipped (fiat) account aborts the run and names the account. You can then map it, or knowingly switch to `drop`. With `drop`, the crypto counter-party's balance in Blnk will differ from Formance by the dropped postings.

If crypto and fiat can't be told apart by address, for example one account holding both `USD/2` and `BTC/8`, use the Go library's [`Transform`](library.md#transform) hook to drop postings by asset:

```go
opts.Transform = func(req *blnk.TransactionRequest, tx migrator.TxContext) (bool, error) {
	return !strings.HasPrefix(tx.Posting.Asset, "USD/"), nil // keep crypto only
}
```

Reconciliation still compares every asset on each migrated account. Fiat balances left behind on those accounts will therefore show up as differences, so filter or review them in the report.

## Is this your situation?

The same approach works for any split, not only fiat and crypto: carving out a product line, a region, or a legal entity, or moving off Formance entirely. If you need to move ledger data from Formance to Blnk and **prove** afterwards that nothing changed, start with [Getting started](getting-started.md).
