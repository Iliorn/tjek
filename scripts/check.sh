#!/bin/sh
# check.sh runs, locally, the checks CI fails a push on: formatting, vet,
# golangci-lint, the shuffled test suite with its coverage floors, and
# govulncheck. It exists because lint findings, a coverage drop, a CHANGELOG
# line over the cap and new Go advisories each reached main and turned CI red
# for every later push — each something a local run would have shown before
# the push.
#
#   scripts/check.sh           the fast set (what the pre-push hook runs)
#   scripts/check.sh --race    also the race-detector run CI does
#
# Enable it as a pre-push hook once per clone:
#
#   git config core.hooksPath .githooks
#
# golangci-lint and govulncheck are required rather than skipped when missing:
# a gate that quietly drops the check that failed is not a gate. They are
# looked for on PATH and in Go's own bin directory, where `go install` puts
# them. Install golangci-lint from https://golangci-lint.run/welcome/install/
# and govulncheck with `go install golang.org/x/vuln/cmd/govulncheck@latest`,
# or set TJEK_CHECK_NO_LINT=1 / TJEK_CHECK_NO_VULN=1 to skip one on purpose.
set -eu

cd "$(dirname "$0")/.."

step() { printf '==> %s\n' "$*"; }

# tool finds a command on PATH or in Go's bin directory.
tool() {
	if command -v "$1" >/dev/null 2>&1; then
		command -v "$1"
	elif [ -x "$(go env GOPATH)/bin/$1" ]; then
		echo "$(go env GOPATH)/bin/$1"
	fi
}

step gofmt
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "gofmt: these files need formatting (run gofmt -w on them):" >&2
	echo "$unformatted" >&2
	exit 1
fi

step "go vet"
go vet ./...

if [ "${TJEK_CHECK_NO_LINT:-}" = "1" ]; then
	step "golangci-lint (skipped: TJEK_CHECK_NO_LINT=1)"
elif lint=$(tool golangci-lint) && [ -n "$lint" ]; then
	step golangci-lint
	"$lint" run ./...
else
	echo "golangci-lint is not installed; CI runs it on every push." >&2
	echo "Install it (https://golangci-lint.run/welcome/install/) or set TJEK_CHECK_NO_LINT=1." >&2
	exit 1
fi

step "go test (shuffled, as CI runs it) with the coverage floors"
scripts/coverage.sh -shuffle=on

if [ "${TJEK_CHECK_NO_VULN:-}" = "1" ]; then
	step "govulncheck (skipped: TJEK_CHECK_NO_VULN=1)"
elif vuln=$(tool govulncheck) && [ -n "$vuln" ]; then
	step govulncheck
	"$vuln" ./...
else
	echo "govulncheck is not installed; CI runs it on every push." >&2
	echo "Install it (go install golang.org/x/vuln/cmd/govulncheck@latest) or set TJEK_CHECK_NO_VULN=1." >&2
	exit 1
fi

if [ "${1:-}" = "--race" ]; then
	step "go test -race"
	go test -race -timeout 10m ./...
fi

step "all checks passed"
