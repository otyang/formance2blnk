# Examples

All examples live in the [`examples/`](https://github.com/otyang/formance2blnk/tree/main/examples) directory and are built in CI; the quickstart and custom-mapper examples are also run there.

## Quickstart

A complete replay and reconciliation against in-memory fakes — no servers required.

```bash
go run ./examples/quickstart
```

```text
Replayed 5 Formance transactions into 7 Blnk transactions (2 ledgers, 3 balances)
Reconciliation: ok=true checked=5 matched=5
Second run wrote 0 transactions
```

```go
--8<-- "examples/quickstart/main.go"
```

## Production

Embedding the migrator in your own program: OAuth2 against Formance Cloud, a durable state file, graceful shutdown, structured logs, a progress callback and a strict reconciliation gate.

```go
--8<-- "examples/production/main.go"
```

## Custom mapper

Implementing `mapping.Mapper` to route accounts to a Blnk ledger per country, falling back to the rules mapper.

```go
--8<-- "examples/custom-mapper/main.go"
```

## Configuration files

=== "Replay (self-hosted)"

    ```json
    --8<-- "examples/configs/replay.json"
    ```

=== "Snapshot (two ledgers)"

    ```json
    --8<-- "examples/configs/snapshot.json"
    ```

=== "Hybrid (Formance Cloud)"

    ```json
    --8<-- "examples/configs/hybrid-formance-cloud.json"
    ```

## Recipes

### Migrate several Formance ledgers into one Blnk ledger

```json
{
  "formance": { "url": "...", "ledgers": ["eu", "us"] },
  "mapping": { "default_ledger": "Customers" }
}
```

References include the Formance ledger name, so transactions from different ledgers never collide. Balances stay separate per Formance ledger.

### One Blnk ledger per account prefix

```json
{
  "mapping": {
    "rules": [
      { "match": "world", "indicator": "@World" },
      { "match": "*:**",  "ledger": "{1}" }
    ]
  }
}
```

`users:1:wallet` → ledger `users`, `merchants:9` → ledger `merchants`. The explicit `world` rule is needed because `**` matches zero segments, so `*:**` would otherwise also catch `world` (rules are checked before the `world_indicator` default).

### Run in Docker with a mounted state directory

```bash
docker run --rm \
  -v "$PWD:/work" \
  -e BLNK_API_KEY \
  ghcr.io/otyang/formance2blnk:latest migrate -config /work/formance2blnk.json -state /work/state/state.json
```

### Gate a pipeline on reconciliation

```bash
formance2blnk migrate -log-format json
formance2blnk reconcile -volumes -format json -out report.json
```

`reconcile` exits with code 2 on any difference, failing the job.

### Re-run with one-at-a-time writes

If your Blnk deployment does not support atomic bulk writes, set `"migration": { "batch_size": 1 }`. Each Formance posting is then sent individually (multi-posting Formance transactions lose atomicity across postings, but references keep re-runs safe).
