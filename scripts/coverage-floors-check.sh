#!/usr/bin/env bash
# The ratchet's pawl: no floor in scripts/coverage.floors may be lower than
# it was at <base-ref>, and no package may vanish from the file. Run by CI
# against the pull request's base (or the previous commit on main).
#
#   bash scripts/coverage-floors-check.sh origin/main
set -euo pipefail
base=${1:?usage: coverage-floors-check.sh <base-ref>}
file=scripts/coverage.floors
old=$(git show "$base:$file" 2>/dev/null || true)
if [ -z "$old" ]; then
	echo "no $file at $base — nothing to compare"
	exit 0
fi
fails=0
while read -r pkg floor; do
	[ -n "$pkg" ] || continue
	case "$pkg" in \#*) continue ;; esac
	now=$(awk -v p="$pkg" '$1 == p { print $2 }' "$file")
	if [ -z "$now" ]; then
		printf '\033[31m✗ %s was removed from %s (floor was %s)\033[0m\n' "$pkg" "$file" "$floor"
		fails=$((fails + 1))
	elif [ "$now" -lt "$floor" ]; then
		printf '\033[31m✗ %s floor lowered %s → %s — floors only go up\033[0m\n' "$pkg" "$floor" "$now"
		fails=$((fails + 1))
	fi
done <<EOF_OLD
$old
EOF_OLD
if [ "$fails" -gt 0 ]; then
	echo "Add the missing tests, or delete the test that lost coverage; never lower a floor." >&2
	exit 1
fi
echo "✓ no coverage floor lowered since $base"
