# Contributing

Thanks for looking at tjek. It is a small, opinionated app; the notes below are
the things that are easy to get wrong rather than a process to follow.

## Getting set up

```sh
go build -o tjek .   # or: go run .
go test ./...
go vet ./...
golangci-lint run ./...
```

Those are what CI runs, on Linux, Windows and macOS, plus `go test -race`, the
per-package coverage floors (`scripts/coverage.sh`) and `govulncheck`.
`scripts/check.sh` runs them together (gofmt, vet, golangci-lint, the shuffled
suite with its coverage floors, govulncheck; `--race` adds the race run), and
it can run before every push:

```sh
git config core.hooksPath .githooks   # once per clone
```

A lint finding costs one line to fix locally and a red `main` for everyone
after it once it is pushed, so the hook is worth the minute it takes.
govulncheck can still fail with no change of ours, when Go publishes new
advisories; the fix is then moving go.mod's `toolchain` line to the Go
release that patches them.

Tests must never touch your real task store. `TestMain` redirects the home
directory for the whole test binary; `TestStorageStaysInsideTheTestHome` fails
loudly if that ever stops working on a platform.

## Conventions that matter

- **Match the file you are editing.** No blanket reformatting, and keep any
  formatting-only change in its own commit.
- **The architecture notes are in [ARCHITECTURE.md](ARCHITECTURE.md)**: the
  cache invalidation rules, the keymap registry, the rendering width contracts.
  Read the section for the area you are touching; most review comments would
  otherwise just be quotes from it.
- **Adding a field to `todo.Todo` needs a migration** (`internal/app/migrations/NNN_*.sql`)
  plus the `Save` upsert and the load scan. A field with only a struct tag is
  silently dropped on the first save/load round trip.
- **New keys go in the keymap registry** (`keymap.go`), which generates the
  footer hints, the help overlay and the command palette. A key that dispatches
  but isn't registered is invisible; a key registered but not dispatched fails
  the keymap tests.
- **Small terminals are a supported size.** Width budgets derive from the
  window and go negative on a narrow one, so use the shared
  `truncate`/`padRight` helpers, which clamp.

## Tests

The suite leans on a few structural tests that keep documentation and code in
step: the keymap registry against dispatch, the help overlay against the
parsers, the completion table against each command's own flags, and the
architecture notes against the symbols they name. If one of them fails, it is
usually telling you that something you changed is now described wrongly
somewhere else. That is what they are for.

For anything in `tasksync/`, add tests. It is the one package where a bug loses
tasks instead of mis-rendering them.

## Commits and pull requests

Explain *why* in the commit message; the diff already says what. Keep unrelated
changes in separate commits.
