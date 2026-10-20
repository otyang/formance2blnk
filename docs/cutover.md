# Cutover runbook

A checklist for moving production traffic from Formance to Blnk with minimal downtime, using replay's incremental catch-up. The `-volumes` checks assume the default `reverts: replay`; with `reverts: skip`, use plain `reconcile` (see [Reconciliation](reconciliation.md#when-to-use-volumes)).

## Rehearse in staging

- [ ] Restore a recent copy of Formance data (or use a read replica) and a fresh Blnk instance.
- [ ] `formance2blnk validate` and `formance2blnk plan` — review ledgers, balance counts and every skip reason.
- [ ] `formance2blnk migrate` — note throughput (`tx_per_sec` in logs) to estimate production duration.
- [ ] `formance2blnk reconcile -volumes` returns `0`.
- [ ] Interrupt a run with Ctrl-C and re-run it; confirm it resumes and reconciliation still passes.
- [ ] Spot-check a few Blnk transactions against Formance (`meta_data.formance_tx_id`).

## Bulk migration (Formance still live)

- [ ] Point the config at production; use a **new** state file.
- [ ] Set `requests_per_second` on the Formance side if you need to protect it.
- [ ] `formance2blnk migrate` — this may take hours for large ledgers; it can be stopped and resumed at any time.

## Cutover window

- [ ] Freeze writes to Formance (maintenance mode / feature flag).
- [ ] `formance2blnk migrate` — catch-up processes only the transactions since the last run, usually seconds.
- [ ] `formance2blnk reconcile -volumes -format json -out cutover-report.json` returns `0`.
- [ ] Switch application writes to Blnk.
- [ ] Unfreeze.

## Afterwards

- [ ] Archive the state file and the reconciliation report with your change record.
- [ ] Keep Formance read-only for an agreed period.
- [ ] Don't run `migrate` again once Blnk receives live traffic.

!!! tip "Snapshot or hybrid cutovers"
    Snapshot has no incremental catch-up, so run it **inside** the freeze window. For hybrid, run the snapshot and most of the replay beforehand, then a final `migrate` (which only replays new transactions) during the freeze.
