# Troubleshooting

??? question "`state: file is locked by another run`"
    Another migrator is using the same state file, or a previous run was killed with `SIGKILL`. Check no other process is running, then delete the `<state_file>.lock` file next to the state file (the error names it).

??? question "`account "x" is mapped to skip`"
    A posting touches an account your rules skip. Either change the rule or set `on_skipped_account: drop` if dropping those postings is intended.

??? question "`mapping: invalid asset`"
    Asset names must be `CODE` or `CODE/DECIMALS`: a letter followed by letters, digits or underscores, with at most 30 decimals. Names that don't fit cannot be migrated with the built-in mapper; `mapping.assets` only changes the Blnk currency and decimals of valid assets. Use a custom `mapping.Mapper` through the library instead.

??? question "`already completed with a different as-of time (cutoff)`"
    A hybrid run's snapshot finished with one cutoff and you re-ran it with another. Resuming would double-count or drop the transactions between the two cutoffs, so it is refused. Re-run with the original cutoff, or start again with a new state file against an empty Blnk instance.

??? question "`page has more results but no next cursor`"
    The Formance server reported more pages but gave no cursor, or returned the same cursor twice. The migrator stops rather than checkpoint past transactions it never read. Check the Formance version and any proxy in between; see [Compatibility](compatibility.md).

??? question "Transactions rejected for insufficient funds"
    You set `enforce_balances: true`, and Formance allowed an overdraft that Blnk refuses. Remove the option (the default trusts Formance's validated history) or configure overdraft rules in Blnk.

??? question "A re-run reports `Already migrated (skipped)`"
    Expected after an interruption: those writes reached Blnk before the checkpoint was saved, and their references were recognised. Nothing was written twice.

??? question "Duplicates are not recognised on resume"
    Your Blnk version may word its duplicate-reference error differently. Run with `-log-level debug` to see the exact message and open an issue; the detection lives in `blnk.IsDuplicateReference`.

??? question "Reconciliation shows credit/debit differences but balances match"
    You used snapshot, hybrid or `reverts: skip`, or the ledger contains self-transfers. Run `reconcile` without `-volumes`. See [Reconciliation](reconciliation.md#when-to-use-volumes).

??? question "Formance responds 429 or 5xx"
    Requests are retried automatically with exponential backoff (honouring `Retry-After`). Lower `formance.requests_per_second` to reduce load.

??? question "How do I start over?"
    Point the config at a fresh Blnk instance and delete the state file. To keep the same Blnk instance, also change `reference_prefix`, otherwise every write is recognised as already migrated.
