#!/usr/bin/env bash
# coverage.sh runs the test suite with coverage and fails when a package drops
# below its floor. CI's coverage job and scripts/check.sh both run it, so the
# floors live here once and a push cannot meet one bar locally and another in
# CI. Extra arguments go to go test (check.sh passes -shuffle=on).
set -euo pipefail

cd "$(dirname "$0")/.."

declare -A floor=(
	["github.com/Iliorn/tjek/internal/app"]=78
	["github.com/Iliorn/tjek/tasksync"]=92
	["github.com/Iliorn/tjek/hlc"]=90
	["github.com/Iliorn/tjek/todo"]=96
)

out=$(mktemp)
trap 'rm -f "$out"' EXIT
# pipefail, so a test failure here is not swallowed by tee.
go test -cover -timeout 5m "$@" ./... | tee "$out"

rc=0
for pkg in "${!floor[@]}"; do
	pct=$(awk -v p="$pkg" '$2 == p { for (i = 1; i <= NF; i++) if ($i ~ /%$/) { sub(/%/, "", $i); print $i } }' "$out")
	if [ -z "$pct" ]; then
		echo "::error::no coverage reported for $pkg"
		rc=1
		continue
	fi
	if awk -v got="$pct" -v want="${floor[$pkg]}" 'BEGIN { exit !(got < want) }'; then
		echo "::error::$pkg coverage $pct% is below the ${floor[$pkg]}% floor"
		rc=1
	else
		echo "$pkg: $pct% (floor ${floor[$pkg]}%)"
	fi
done
exit $rc
