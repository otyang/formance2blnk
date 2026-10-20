# Concepts

## Data model mapping

Both systems are double-entry ledgers that store money as integers in minor units, so the models map closely.

| Formance | Blnk | Notes |
| --- | --- | --- |
| Ledger | Ledger | Name taken from mapping rules (default: same name). |
| Account address (`users:42:wallet`) | Balance | One Blnk balance **per account per asset** — Blnk balances are single-currency. |
| `world` | `@World` | An internal (indicator) balance Blnk creates on first use. Configurable. |
| Asset `USD/2` | Currency `USD`, precision `100` | The number after `/` is the number of decimals. |
| Posting | Transaction | `precise_amount` carries the exact integer. |
| Transaction with N postings | N transactions in one atomic bulk call | All-or-nothing. |
| `timestamp` | `effective_date` | Preserves backdating. |
| Transaction metadata | `meta_data.formance_metadata` | Plus provenance keys, see below. |
| Account metadata | Balance `meta_data.formance_metadata` | With `copy_account_metadata: true`. |
| Revert | A reversal transaction | Or drop both with `reverts: skip`. |

### Provenance metadata

Every migrated object carries enough information to trace it back:

=== "Transaction"

    ```json
    {
      "formance_ledger": "main",
      "formance_tx_id": 1042,
      "formance_posting": 0,
      "formance_source": "users:1:wallet",
      "formance_destination": "platform:fees",
      "formance_asset": "USD/2",
      "formance_reference": "order-889",
      "formance_metadata": { "order_id": "889" }
    }
    ```

=== "Balance"

    ```json
    {
      "formance_ledger": "main",
      "formance_address": "users:1:wallet",
      "formance_asset": "USD/2",
      "migrated_by": "formance2blnk",
      "formance_metadata": { "kyc": "tier2" }
    }
    ```

=== "Opening balance"

    ```json
    {
      "formance_ledger": "main",
      "formance_address": "users:1:wallet",
      "formance_asset": "USD/2",
      "formance_input": "5000",
      "formance_output": "1250",
      "opening_balance": true,
      "as_of": "2026-01-01T00:00:00Z"
    }
    ```

    `as_of` is only present for `SnapshotAsOf` and hybrid runs.

## References and idempotency

Blnk requires a unique `reference` per transaction and refuses duplicates. formance2blnk builds references deterministically:

| Kind | Format | Example |
| --- | --- | --- |
| Replayed posting | `<prefix>:<ledger>:<txid>:<posting>` | `fm:main:1042:0` |
| Opening balance | `<prefix>-open:<ledger>:<address>:<asset>` | `fm-open:main:users:1:wallet:USD/2` |

The prefix defaults to `fm` (`reference_prefix`). Because a reference identifies its source posting, re-sending the same posting can never create a second transaction. When a write fails because the reference already exists, the migrator counts it as *already migrated* and continues.

## Checkpoints and the state file

The state file (`state_file`, default `formance2blnk-state.json`) records:

- the Blnk `ledger_id` created for each ledger name;
- the Blnk `balance_id` created for each (Formance ledger, address, asset);
- per Formance ledger, the last transaction ID whose postings are **all** durable in Blnk, and whether the snapshot phase completed (with its as-of time, so a hybrid run cannot be resumed with a different cutoff);
- with `reverts: skip`, the IDs of reverted transactions that were dropped, so their reverts are dropped too, even in a later run.

Writes are atomic (temp file + `fsync` + rename + directory `fsync`) and the file is protected by a `.lock` file, so two migrators can never share it.

!!! warning "Keep the state file"
    The state file is how the migrator knows which Blnk balance belongs to which Formance account. Losing it mid-migration does not cause double-posting (references prevent that) but new balances would be created for accounts that already have one. Back it up, and keep it for reconciliation.

## Ordering

Formance transaction IDs are monotonic. The migrator processes them strictly in ascending ID order and writes with `skip_queue: true`, so Blnk applies each batch synchronously, in order, before the next one is sent. Effective dates are preserved even when Formance transactions were backdated.

The Formance list endpoint returns newest-first by default. The client requests oldest-first (`reverse=true`, configurable via `ascending_params`); if the server ignores it, the client detects descending order and buffers — and when resuming, it stops paging as soon as it reaches the checkpoint.

## Batching and atomicity

Postings are collected into atomic bulk calls of up to `batch_size` Blnk transactions (Blnk's limit for atomic batches is 50). A Formance transaction is never split across calls unless it alone has more than `batch_size` postings, in which case it is written in chunks of `batch_size` and a warning is logged.

If a batch fails because one of its references already exists (typical after a crash between the write and the checkpoint), the migrator retries **group by group**. If a group with several postings still reports a duplicate, each of its postings is then sent on its own: an earlier run with a smaller `batch_size` may have written only part of it, and every posting already present is counted as already migrated while the missing ones are written.

## Overdrafts

Formance already validated every historical transaction, and its overdraft rules (Numscript `allowing unbounded overdraft`, etc.) have no direct Blnk equivalent. Replayed transactions are therefore sent with `allow_overdraft: true` by default. Set `enforce_balances: true` if you want Blnk to re-validate funds — expect rejections wherever Formance allowed overdrafts.
