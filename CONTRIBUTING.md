# Contributing

Thanks for helping improve formance2blnk!

## Development

Requirements: Go 1.22+. Optional: `golangci-lint`, Python 3 for docs (`pip install -r docs/requirements.txt`).

```bash
make fmt          # gofmt -s
make check        # vet, lint (if installed), race tests, gofmt check
make cover        # coverage report
make build        # bin/formance2blnk
make docs         # mkdocs build --strict
make docs-serve   # live docs at http://127.0.0.1:8000
go run ./examples/quickstart
```

The test suite runs entirely against in-memory fakes in `internal/fakes`, which model Formance pagination
and Blnk double-entry semantics (duplicate references, atomic bulk rollback, lost responses).
When you change behaviour, add a scenario to `migrator/migrator_test.go`.

## Guidelines

- Keep the module dependency-free (standard library only) unless there is a compelling reason.
- Money is always `amount.Amount` — never `float64`.
- Every write to Blnk must carry a deterministic reference.
- Public APIs need doc comments; user-visible changes need a docs update and a `CHANGELOG.md` entry.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:` …).

## Releasing

Tag `vX.Y.Z` on `main`. The release workflow builds binaries and multi-arch container images with GoReleaser.
`make release-check` validates `.goreleaser.yaml` and `make snapshot` builds a local release without publishing.
