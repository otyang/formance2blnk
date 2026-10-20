# Reconciliation

`reconcile` reads current volumes from Formance and compares them with Blnk balances.

```bash
formance2blnk reconcile                  # net balances
formance2blnk reconcile -volumes         # + credit/debit volumes (after a full replay)
formance2blnk reconcile -format csv -out drift.csv
```

With `-out`, the report goes to the file and a one-line summary is printed to stdout. `-workers` (default 8) sets the number of concurrent Blnk balance lookups.

## What is compared

For each (Formance ledger, account, asset):

| Mapped to | Compared against |
| --- | --- |
| A balance | The Blnk balance recorded in the state file for that account and asset. |
| An indicator | The Blnk indicator balance for that currency. Several Formance accounts mapped to the same indicator — including across ledgers — are **summed**. An indicator that does not exist in Blnk counts as zero. |
| Skip | Not compared (counted as skipped). Indicators are skipped too with `-skip-indicators`. |

| Field | Formance | Blnk |
| --- | --- | --- |
| `balance` | `balance` | `balance` |
| `credit_balance` (with `-volumes`) | `input` | `credit_balance` |
| `debit_balance` (with `-volumes`) | `output` | `debit_balance` |

A Formance account mapped to a balance but with no Blnk balance in the state file is reported with field `missing` if its balance (or, with `-volumes`, its input/output) is non-zero; otherwise it is counted as skipped. A failed Blnk lookup is reported with field `error`.

## When to use `-volumes`

Volume checks only hold after a full **replay** with `reverts: replay`. Snapshot, hybrid and `reverts: skip` produce the same net balances but different credit/debit totals, so use net-balance reconciliation for those.

Formance postings from an account to itself — or between two accounts mapped to the same Blnk balance or indicator — are skipped (Blnk rejects them); they add equally to Formance input and output, so they can show up as volume differences but never as balance differences.

## Output

=== "text"

    ```text
    reconciliation MISMATCH: checked 1204, matched 1203, skipped 3, mismatches 1

    FIELD    EXPECTED  ACTUAL  TARGET          ACCOUNTS
    balance  15000     14700   bln_0a1b2c...   main:users:2:wallet (USD/2)
    ```

=== "json"

    ```json
    {
      "generated_at": "2026-10-20T18:00:00Z",
      "checked": 1204,
      "matched": 1203,
      "skipped": 3,
      "mismatches": [
        {
          "target": "bln_0a1b2c",
          "accounts": "main:users:2:wallet (USD/2)",
          "field": "balance",
          "expected": "15000",
          "actual": "14700",
          "detail": "difference -300"
        }
      ]
    }
    ```

=== "csv"

    ```text
    target,accounts,field,expected,actual,detail
    bln_0a1b2c,main:users:2:wallet (USD/2),balance,15000,14700,difference -300
    ```

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Everything matches |
| `1` | Error (connectivity, configuration…) |
| `2` | At least one mismatch |
| `64` | Usage error (unknown command or flag) |

This makes reconciliation easy to use as a gate in CI or a cutover script:

```bash
formance2blnk reconcile -volumes -format json -out report.json || {
  echo "ledgers differ — see report.json"; exit 1;
}
```

## Common causes of drift

- **Writes to Formance after the last migrate run** — run `migrate` again (replay catches up incrementally).
- **Writes to Blnk outside the migration** — reconcile before opening Blnk to traffic.
- **Changed mapping rules** between runs.
- **Dropped postings** with `on_skipped_account: drop` or a `Transform` that returns `keep=false`.
