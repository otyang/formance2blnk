# Getting started

## Install

=== "Go"

    ```bash
    go install github.com/otyang/formance2blnk/cmd/formance2blnk@latest
    ```

=== "Binary"

    Download an archive for your platform from the
    [releases page](https://github.com/otyang/formance2blnk/releases), extract it and put
    `formance2blnk` on your `PATH`.

=== "Docker"

    ```bash
    docker run --rm -v "$PWD:/work" ghcr.io/otyang/formance2blnk:latest help
    ```

=== "From source"

    ```bash
    git clone https://github.com/otyang/formance2blnk && cd formance2blnk
    make build   # → bin/formance2blnk
    ```

Check it works:

```bash
formance2blnk version
```

## Try it without servers

The repository includes in-memory fakes of both APIs. This runs a full replay and reconciliation in a second:

```bash
go run ./examples/quickstart
```

```text
Replayed 5 Formance transactions into 7 Blnk transactions (2 ledgers, 3 balances)
Reconciliation: ok=true checked=5 matched=5
Second run wrote 0 transactions
```

## Your first real migration

### 1. Create a configuration

```bash
formance2blnk init > formance2blnk.json
```

Edit the URLs, the list of Formance ledgers and the [mapping rules](mapping.md). Secrets can come from the environment:

```json
{
  "formance": { "url": "http://localhost:3068", "ledgers": ["main"], "token": "${FORMANCE_TOKEN:-}" },
  "blnk":     { "url": "http://localhost:5001", "api_key": "${BLNK_API_KEY:-}" },
  "mapping":  { "rules": [ { "match": "users:*:wallet", "ledger": "Customer Wallets" } ] },
  "migration": { "mode": "replay" }
}
```

### 2. Validate

```bash
formance2blnk validate
```

This checks required fields, unknown keys, mapping rule syntax and mode settings without contacting either server, and prints the configuration with secrets redacted.

### 3. Plan (dry run)

```bash
formance2blnk plan
```

`plan` reads everything from Formance and computes exactly what would be written — ledgers, balances, transactions and skipped postings — **without writing to Blnk or the state file**.

### 4. Migrate

```bash
formance2blnk migrate
```

Progress is logged periodically. If the run stops for any reason, run the same command again; it resumes from the checkpoint.

### 5. Reconcile

```bash
formance2blnk reconcile -volumes
```

Exit code `0` means every balance matches. See [Reconciliation](reconciliation.md) for what is compared.

!!! tip "Run against staging first"
    Point the tool at a staging Blnk instance and a copy (or read replica) of your Formance data, migrate, reconcile, and only then plan the production [cutover](cutover.md).
