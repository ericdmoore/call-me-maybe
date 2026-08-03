#!/usr/bin/env bash
# Coverage gate. Runs in CI and locally via `make cover`.
#
# Per-package floors rather than one global number: the global figure is
# dominated by cmd/ and internal/ari, which are thin glue over Asterisk and
# are covered by scripts/smoke.sh on real hardware instead of unit tests.
# A single total would let coverage rot in the state machine while still
# looking fine because some untested glue grew.
#
# Floors are set just under current coverage. Raise them when you add tests;
# never lower one to make a push go through.
set -euo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

PROFILE=${PROFILE:-coverage.out}
TOTAL_FLOOR=${TOTAL_FLOOR:-49}

# package                          floor%
FLOORS="
callmemaybe/internal/lobby         80
callmemaybe/internal/policy        60
callmemaybe/internal/render        88
callmemaybe/internal/schema        90
callmemaybe/internal/config        70
callmemaybe/internal/setup         80
callmemaybe/internal/tmpl          70
callmemaybe/internal/lsp           35
callmemaybe/internal/awssig        95
callmemaybe/internal/calls         85
callmemaybe/internal/voice         80
callmemaybe/internal/story         85
callmemaybe/internal/pack          80
"

echo "→ go test -race -coverprofile=$PROFILE"
go test -race -covermode=atomic -coverprofile="$PROFILE" ./...
echo

# Statement-weighted coverage for one package, read from the raw profile.
# Profile lines look like:
#   callmemaybe/internal/lobby/session.go:100.20,102.3 2 1
#                                         ^start,end   ^stmts ^hits
pkg_coverage() {
	awk -v pkg="$1/" -F'[ :]' '
		index($0, pkg) == 1 {
			stmts = $(NF - 1); hits = $NF
			total += stmts
			if (hits > 0) covered += stmts
		}
		END {
			if (total == 0) print "n/a"
			else printf "%.1f", covered * 100 / total
		}
	' "$PROFILE"
}

below() { awk -v g="$1" -v f="$2" 'BEGIN { exit !(g + 0 < f + 0) }'; }

fails=0
report() { # name coverage floor
	if [ "$2" = "n/a" ]; then
		printf '  \033[31m%-34s   no coverage data\033[0m\n' "$1"
		fails=$((fails + 1))
	elif below "$2" "$3"; then
		printf '  \033[31m%-34s %5s%%  (floor %s%%)\033[0m\n' "$1" "$2" "$3"
		fails=$((fails + 1))
	else
		printf '  \033[32m%-34s %5s%%\033[0m  (floor %s%%)\n' "$1" "$2" "$3"
	fi
}

while read -r pkg floor; do
	[ -n "$pkg" ] || continue
	report "$pkg" "$(pkg_coverage "$pkg")" "$floor"
done <<EOF
$FLOORS
EOF

total=$(go tool cover -func="$PROFILE" | awk '/^total:/ { gsub(/%/, "", $NF); print $NF }')
echo
report TOTAL "$total" "$TOTAL_FLOOR"

if [ "$fails" -gt 0 ]; then
	printf '\n\033[31m✗ coverage below floor in %d place(s)\033[0m\n' "$fails" >&2
	exit 1
fi
printf '\n\033[32m✓ coverage floors met\033[0m\n'
