# CLI reference

```text
formance2blnk <command> [flags]
```

| Command | Description |
| --- | --- |
| `init` | Print a starter configuration to stdout. |
| `validate` | Check the configuration and print it with secrets redacted. |
| `plan` | Dry run of the configured mode. Reads Formance, writes nothing. |
| `migrate` | Run the migration. Resumable and safe to re-run. |
| `reconcile` | Compare Formance and Blnk. Exit code 2 on mismatch. |
| `version` | Print version, commit and build date. Also `-version`, `--version`. |
| `help` | Print usage. Also `-h`, `--help`. Running with no command prints usage to stderr and exits `64`. |

## Common flags

`validate`, `plan`, `migrate` and `reconcile` accept every flag below (run `formance2blnk <command> -h` to list them); a flag only has an effect where documented. `-mode`, `-cutoff` and `-state` are applied before validation, so they also affect `validate`.

| Flag | Default | Description |
| --- | --- | --- |
| `-config path` | `formance2blnk.json` | Configuration file. |
| `-state path` | from config | Override `state_file`. |
| `-log-level level` | `info` | `debug`, `info`, `warn`, `error`. |
| `-log-format fmt` | `text` | `text` or `json` (for log shipping). |

## `plan` and `migrate`

| Flag | Description |
| --- | --- |
| `-mode m` | Override `migration.mode`: `replay`, `snapshot` or `hybrid`. |
| `-cutoff t` | Override `migration.cutoff` (RFC 3339) for hybrid mode. |
| `-json` | Print the summary as JSON. |

The summary title is the phase (`replay`, `snapshot` or `hybrid`), suffixed with `(plan)` for `plan`, which also ends with `plan only: nothing was written to Blnk or the state file`. Example summary:

```text
replay summary
  Formance transactions read   125034
  Blnk transactions written    188211
  Already migrated (skipped)   0
  Blnk ledgers created         3
  Blnk balances created        40112
  Skipped: zero_amount         4
  Checkpoint main              tx 125033
  Elapsed                      41m12.4s
```

Skip reasons: `before_cutoff`, `reverted`, `revert`, `zero_amount`, `self_posting`, `same_endpoint`, `skipped_account`, `transform`, `world`, `zero_balance`.

## `reconcile`

| Flag | Default | Description |
| --- | --- | --- |
| `-volumes` | `false` | Also compare credit/debit volumes (after a full replay). |
| `-skip-indicators` | `false` | Ignore `@indicator` balances. |
| `-format f` | `text` | `text`, `json` or `csv`. |
| `-out file` | stdout | Write the report to a file; a one-line summary is still printed to stdout. |
| `-workers n` | `8` | Concurrent Blnk balance lookups. |

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Error, including an invalid configuration and interruption (progress is saved) |
| `2` | Reconciliation mismatch |
| `64` | Usage error: no or unknown command, bad flag, invalid `-log-level` or `-log-format` |

## Signals

`SIGINT` (Ctrl-C) and `SIGTERM` stop the run gracefully: in-flight requests are cancelled, the state file is flushed and unlocked. Re-run the same command to resume.
