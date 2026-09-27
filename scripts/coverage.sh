#!/usr/bin/env bash
# Coverage gate, and the ratchet that raises it. Runs in CI and locally:
#
#   make cover           tests with -race; fails when any package is below
#                        its floor in scripts/coverage.floors
#   make cover-ratchet   the same, then raises every floor to the coverage
#                        just measured (rounded down), adds packages that
#                        gained tests, and never lowers anything
#
# Per-package floors rather than one global number: the global figure is
# dominated by cmd/ and internal/ari, which are thin glue over Asterisk and
# are covered by scripts/smoke.sh on real hardware instead of unit tests. A
# single total would let coverage rot in the state machine while some
# untested glue grew.
#
# The policy is a ratchet: floors only go up. Add tests, run the ratchet,
# commit the floors with them; CI refuses a push that lowers one
# (scripts/coverage-floors-check.sh). Never edit a floor down to make a push
# go through — delete the test that lost coverage, or write the missing one.
set -euo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

PROFILE=${PROFILE:-coverage.out}
FLOORS_FILE=${FLOORS_FILE:-scripts/coverage.floors}
ratchet=0
[ "${1:-}" = "--ratchet" ] && ratchet=1

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

# Every package the profile knows, so a package that gained its first test
# is ratcheted in without anyone remembering to list it.
profile_packages() {
	awk -F: 'NR > 1 { n = split($1, parts, "/"); sub("/" parts[n] "$", "", $1); print $1 }' "$PROFILE" | sort -u
}

total_coverage() {
	go tool cover -func="$PROFILE" | awk '/^total:/ { gsub(/%/, "", $NF); print $NF }'
}

below() { awk -v g="$1" -v f="$2" 'BEGIN { exit !(g + 0 < f + 0) }'; }
floor_of() { awk -v pkg="$1" '$1 == pkg { print $2; found = 1 } END { if (!found) print "" }' "$FLOORS_FILE"; }

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
	case "$pkg" in \#*) continue ;; esac
	if [ "$pkg" = TOTAL ]; then
		report TOTAL "$(total_coverage)" "$floor"
	else
		report "$pkg" "$(pkg_coverage "$pkg")" "$floor"
	fi
done < "$FLOORS_FILE"

if [ "$ratchet" = 1 ]; then
	echo
	echo "→ ratchet: raising floors to what was just measured"
	tmp=$(mktemp)
	{
		echo "# Coverage floors, in whole percent — scripts/coverage.sh reads these and CI"
		echo "# fails below them. Raised by \`make cover-ratchet\`, never edited down."
		for pkg in $(profile_packages); do
			cov=$(pkg_coverage "$pkg")
			[ "$cov" != "n/a" ] || continue
			new=${cov%.*}
			old=$(floor_of "$pkg")
			if [ -n "$old" ] && [ "$old" -ge "$new" ]; then new=$old; fi
			[ "$old" = "$new" ] || printf '    %-40s %s → %s\n' "$pkg" "${old:-—}" "$new" >&2
			printf '%-40s %s\n' "$pkg" "$new"
		done
		total=$(total_coverage); new=${total%.*}; old=$(floor_of TOTAL)
		if [ -n "$old" ] && [ "$old" -ge "$new" ]; then new=$old; fi
		[ "$old" = "$new" ] || printf '    %-40s %s → %s\n' TOTAL "${old:-—}" "$new" >&2
		printf '%-40s %s\n' TOTAL "$new"
	} > "$tmp"
	mv "$tmp" "$FLOORS_FILE"
	echo "  wrote $FLOORS_FILE"
	exit 0
fi

if [ "$fails" -gt 0 ]; then
	printf '\n\033[31m✗ coverage below floor in %d place(s)\033[0m\n' "$fails" >&2
	exit 1
fi
printf '\n\033[32m✓ coverage floors met\033[0m\n'
