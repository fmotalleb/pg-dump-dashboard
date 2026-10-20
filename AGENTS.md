# Repository Guidelines

## Project Structure & Module Organization

`cmd/pg-dump-dashboard/` contains the executable entry point. Keep application
composition and lifecycle wiring in `internal/app/`, while feature code belongs
to focused `internal/` packages: `config`, `httpapi`, `postgres`, `dump`,
`session`, and `execution`. Execution backends live below
`internal/execution/` (`local`, `docker`, `osprocess`, `pgclient`, and `fake`).

The frontend source is in `web/src/`; its build writes embedded assets to
`internal/web/dist/`. Do not hand-edit generated files in that directory.
Unit tests sit beside their packages as `*_test.go`; real PostgreSQL coverage is
in `test/integration/`.

## Build, Test, and Development Commands

Use the Makefile rather than re-creating build steps:

```bash
make build                         # build Tailwind assets and bin/pg-dump-dashboard
make run ARGS="-pg-host db -pg-port 5432" # build and run locally
make test                          # all Go tests with the race detector
make test-integration              # PostgreSQL-backed integration tests
make check                         # gofmt, vet, golangci-lint, and tests
```

Run `make deps` after cloning or when `web/package-lock.json` changes. Set
`PGDD_TEST_PG_*` variables for integration tests instead of placing credentials
in source files.

## Coding Style & Naming Conventions

Write idiomatic Go, formatted with `gofmt`; imports are organized by
`goimports` with the module prefix `github.com/fmotalleb/pg-dump-dashboard`.
Use tabs for Go indentation, concise package names, exported Go-style
identifiers, and lowercase test names such as `"path traversal"`. Keep
interfaces near their consumers, propagate `context.Context`, and wrap errors
when callers need to classify them. Run `make lint` before submitting: the
repository enables security, context, error-handling, and formatting linters.

Use the existing vanilla JavaScript and Tailwind setup in `web/src/`; rebuild
with `make frontend` after UI changes.

## Testing Guidelines

Add focused table-driven unit tests with the implementation; call `t.Parallel()`
when state is not shared. Test failure paths, especially credential handling,
command construction, HTTP responses, and streamed dump cancellation. Use
`internal/execution/fake` for process-dependent tests. Run integration tests
only against a disposable PostgreSQL instance.

## Commit & Pull Request Guidelines

Use short, imperative commit subjects consistent with history, for example:
`Harden docker credential forwarding` or `Document dependency policy`.
Keep each commit narrowly scoped. Pull requests should explain the behavior
change, identify configuration or security implications, link relevant issues,
and include UI screenshots when frontend behavior changes. Confirm `make check`
passes; note any integration test environment that reviewers must provide.

## Security & Configuration

Never commit database passwords, connection strings, or generated archives.
Preserve the project’s boundary: credentials remain in memory, client commands
are constructed as argument lists, and dumps stream rather than being buffered
or written to disk.
