# Compatibility

formance2blnk targets the **Formance Ledger v2** REST API and the **Blnk Core** API. It uses a deliberately small surface of each, listed below, so you can check it against the versions you run.

## Formance (read-only)

| Endpoint | Used for |
| --- | --- |
| `GET /v2` | (library) listing ledgers |
| `GET /v2/{ledger}/transactions` | Replay — cursor pagination, `pageSize`, `reverse=true` |
| `GET /v2/{ledger}/transactions/{id}` | (library) `formance.Client.GetTransaction` |
| `GET /v2/{ledger}/volumes` | Snapshot (with `endTime`) and reconciliation; `insertionDate=true` is available to library users |
| `GET /v2/{ledger}/accounts/{address}` | `copy_account_metadata` |
| `POST /api/auth/oauth/token` | OAuth2 client credentials (Formance Cloud / secured stacks). Resolved against the origin of `formance.url`; override with `token_url`. |

Assumptions:

- Responses use the v2 cursor envelope: `{"cursor": {"hasMore", "next", "data": [...]}}`; single objects are wrapped in `{"data": ...}`.
- When `cursor` is set, no other query parameters are sent (Formance rejects them).
- Transaction IDs and amounts may be JSON numbers or strings and may exceed 64-bit floats; both are decoded exactly.
- Reverts are identified by the `com.formance.spec/state/reverts` metadata key, reverted originals by the `reverted` field.
- If `reverse=true` is not honoured, the client detects newest-first order and falls back to buffering. You can set another query string with `ascending_params`.

## Blnk (write)

| Endpoint | Used for |
| --- | --- |
| `POST /ledgers` | Creating ledgers |
| `POST /balances` | Creating balances |
| `GET /balances/{id}` | Reconciling balances |
| `GET /balances/indicator/{indicator}/currency/{currency}` | Reconciling indicator balances (404 counts as zero) |
| `POST /transactions` | Writes that contain exactly one transaction (always the case with `batch_size: 1`) |
| `POST /transactions/bulk` | Atomic batches (`atomic: true`, `run_async: false`, `skip_queue: true`) |

Assumptions:

- Authentication is the `X-Blnk-Key` header (omitted when `api_key` is empty).
- Amounts are sent as `precise_amount` (integer minor units) with `precision`.
- A duplicate reference is reported as a 400/409/422 error whose message mentions the reference being already used / existing. This is how resumed runs recognise already-migrated data. If your Blnk version words it differently, `blnk.IsDuplicateReference` is the single place to adjust.
- Atomic bulk batches are limited to 50 transactions. A Formance transaction with more postings than `batch_size` is written in several calls and is therefore not atomic (a warning is logged).
- Requests are retried on network errors, 429 and 5xx responses with backoff. If a write succeeded but its response was lost, the retry is recognised as a duplicate reference.

## Supported Go versions

Go 1.22 and the latest stable release are tested in CI.

!!! tip "Verify before production"
    APIs evolve. Before a production migration, run `plan`, then a full `migrate` + `reconcile -volumes` against staging copies of both systems. The reconciliation is independent of these assumptions: if it passes, the data matches.
