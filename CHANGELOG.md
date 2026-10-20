# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-10-20

### Added
- Replay, snapshot and hybrid migration strategies.
- Rule-based account mapping (ledgers, indicators, skip) with templates, and a pluggable `Mapper` interface.
- Asset translation (`USD/2` → `USD` / precision 100) with overrides.
- Deterministic references, checkpointing and a locked JSON state file for safe resumption.
- Atomic bulk writes with duplicate-reference recovery.
- Reconciliation of balances and credit/debit volumes with text, JSON and CSV reports.
- Formance client with bearer and OAuth2 client-credentials auth; descending-order fallback.
- JSON configuration with `${ENV}` / `${ENV:-default}` expansion and secret redaction.
- CLI: `init`, `validate`, `plan`, `migrate`, `reconcile`, `version`, `help`.
