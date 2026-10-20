# Configuration reference

The CLI reads a JSON file (default `formance2blnk.json`, override with `-config`). Generate a starter file with `formance2blnk init`.

Any JSON string value can reference environment variables as `${NAME}` or `${NAME:-default}`. A variable that is unset or empty expands to its default, or to an empty string if there is none. Numbers and booleans cannot be taken from the environment. Unknown keys are rejected, so typos fail fast.

`formance2blnk validate` checks the whole file, including `batch_size` (0–50), `reverts`, `on_skipped_account`, asset decimals and the hybrid cutoff, without contacting either server.

```json
--8<-- "examples/configs/hybrid-formance-cloud.json"
```

## `formance`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `url` | string | **required** | Ledger API root without `/v2`. Self-hosted: `http://localhost:3068`. Formance Cloud: `https://ORG.ENV.formance.cloud/api/ledger`. |
| `ledgers` | string[] | **required** | Formance ledgers to migrate, in order. |
| `token` | string | | Static bearer token. |
| `client_id` / `client_secret` | string | | OAuth2 client-credentials. Mutually exclusive with `token`. |
| `token_url` | string | `<origin>/api/auth/oauth/token` | OAuth2 token endpoint (used with `client_id`). |
| `scopes` | string[] | | OAuth2 scopes, e.g. `["ledger:read"]`. |
| `page_size` | int | `100` | Page size for list calls. |
| `requests_per_second` | number | unlimited | Client-side rate limit. |
| `ascending_params` | string | `reverse=true` | Query string that makes the transaction list oldest-first. |

## `blnk`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `url` | string | **required** | Blnk base URL, e.g. `http://localhost:5001`. |
| `api_key` | string | | Sent as `X-Blnk-Key`. Leave empty for unsecured local instances. |
| `requests_per_second` | number | unlimited | Client-side rate limit. |

## `mapping`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `rules` | rule[] | `[]` | Ordered rules; see [Mapping](mapping.md). Each rule has `match` and exactly one of `ledger`, `indicator` (must start with `@`), `skip`. |
| `world_indicator` | string | `@World` | Target for Formance's `world` account. Must start with `@`. |
| `default_ledger` | string | `{ledger}` | Blnk ledger for unmatched accounts. |
| `assets` | object | | Per-asset overrides keyed by Formance asset (e.g. `USD/2`): `{ "USD/2": { "currency": "...", "decimals": n } }` with `n` between 0 and 30. Both fields are optional. |

## `migration`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | string | `replay` | `replay`, `snapshot` or `hybrid`. See [Strategies](strategies.md). Override with `-mode`. |
| `cutoff` | RFC 3339 | | Required for `hybrid`; must not be in the future and cannot change once the snapshot completed. Override with `-cutoff`. |
| `reference_prefix` | string | `fm` | Prefix of every Blnk reference. Changing it creates an independent copy. |
| `batch_size` | int | `50` | Blnk transactions per atomic bulk call (1–50; `0` means the default). Use `1` to send one at a time. |
| `enforce_balances` | bool | `false` | Send `allow_overdraft: false` on replayed transactions. |
| `reverts` | string | `replay` | `replay` or `skip`. |
| `on_skipped_account` | string | `error` | `error` or `drop`. |
| `copy_account_metadata` | bool | `false` | Copy Formance account metadata onto new Blnk balances (one extra request per account). |
| `opening_indicator` | string | world indicator | Counter-party of opening balances in snapshot/hybrid. |
| `include_zero_balances` | bool | `false` | Snapshot: create balances for zero-balance accounts. |

## Top level

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `state_file` | string | `formance2blnk-state.json` | Progress and ID store. Missing parent directories are created. Override with `-state`. |
