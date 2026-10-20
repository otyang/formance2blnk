# Mapping

Mapping decides where each Formance account and asset lands in Blnk. It lives in the `mapping` section of the configuration.

## Targets

Each Formance account resolves to exactly one target:

| Target | Rule field | Result in Blnk |
| --- | --- | --- |
| Balance | `"ledger": "Name"` | A regular balance in the named ledger, created on first use (one per asset). |
| Indicator | `"indicator": "@Name"` | An internal balance addressed by indicator. Blnk creates it on first use. |
| Skip | `"skip": true` | Not migrated. See `on_skipped_account`. |

## Rules

Rules are evaluated **in order**; the first match wins. If nothing matches, `world` maps to `world_indicator` (default `@World`) and every other account maps to a balance in `default_ledger` (default `{ledger}`, the Formance ledger's own name). Rules are checked before the `world` default, so a rule that matches `world` (for example `*:**` or `**`) overrides `world_indicator`.

Each rule must set exactly one of `ledger`, `indicator` or `skip`, and a non-empty `match`. Indicators (including `world_indicator`) must start with `@`. Invalid rules are rejected when the mapper is created.

```json
{
  "mapping": {
    "world_indicator": "@World",
    "default_ledger": "Formance {ledger}",
    "rules": [
      { "match": "platform:fees",    "indicator": "@Fees" },
      { "match": "banks:*",          "indicator": "@Bank-{1}" },
      { "match": "users:*:wallet",   "ledger": "Customer Wallets" },
      { "match": "merchants:**",     "ledger": "Merchants" },
      { "match": "sandbox:**",       "skip": true }
    ]
  }
}
```

| Address | Result |
| --- | --- |
| `world` | `@World` |
| `platform:fees` | `@Fees` |
| `banks:gtbank` | `@Bank-gtbank` |
| `users:42:wallet` | balance in **Customer Wallets** |
| `users:42:savings` | balance in **Formance main** |
| `merchants:9:pending:eur` | balance in **Merchants** |
| `sandbox:anything` | skipped |

### Pattern syntax

Patterns match the `:`-separated address segment by segment.

| Pattern | Matches |
| --- | --- |
| `literal` | exactly that segment |
| `*` | any single segment |
| `user_*`, `acc-??` | glob within one segment ([`path.Match`](https://pkg.go.dev/path#Match) syntax) |
| `**` | zero or more trailing segments — only allowed as the last segment |

### Templates

`ledger` and `indicator` values may use:

| Placeholder | Value |
| --- | --- |
| `{ledger}` | the source Formance ledger name |
| `{0}` | the full address |
| `{1}`, `{2}`, … | the segments captured by wildcards, in order (a glob segment captures the whole segment; `**` captures the `:`-joined remainder, possibly empty) |

Unknown placeholders are left unchanged.

## Skipped accounts

`on_skipped_account` controls postings that touch a skipped account:

- `error` (default) — stop the migration. Safe: you notice before balances diverge.
- `drop` — drop the posting and count it under `skipped_account`. The counter-party's balance in Blnk will differ from Formance by those postings.

In snapshot mode, a skipped account with a non-zero balance is an error unless the policy is `drop`.

## Assets

Formance assets are `CODE` or `CODE/DECIMALS` (the code starts with a letter and may contain letters, digits and `_`; at most 30 decimals). They become a Blnk currency and a precision multiplier of `10^DECIMALS`:

| Formance | Blnk currency | Precision |
| --- | --- | --- |
| `USD/2` | `USD` | `100` |
| `BTC/8` | `BTC` | `100000000` |
| `JPY` | `JPY` | `1` |

Override per asset, keyed by the full Formance asset (`USD/2`, `COIN`); `decimals` also changes the precision:

```json
{ "mapping": { "assets": { "COIN": { "currency": "NGN", "decimals": 2 } } } }
```

## Custom mappers (Go)

In the library, anything implementing `mapping.Mapper` can be passed as `Options.Mapper`:

```go
type Mapper interface {
	Account(sourceLedger, address string) (Target, error)
	Asset(asset string) (Asset, error)
}
```

See [examples/custom-mapper](examples.md#custom-mapper) for a mapper that routes accounts to a ledger per country and falls back to the rules mapper.

!!! warning "Mappings must be stable"
    Don't change mapping rules between runs of the same migration. Existing balances are looked up in the state file by Formance ledger, address and asset, so an account that already has a Blnk balance keeps it as long as it still maps to a balance (even if the rule now names another ledger). Indicator and skip targets are not stored, so changes to them take effect immediately, splitting the account's history.
