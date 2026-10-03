#!/bin/bash
# Trusted copy runs against the repair checkout; mirror this repo's CI surface.
set -euo pipefail
: "${REPAIR_BASE_SHA:?}"
make check
make lint-test
make cover
bash scripts/coverage-floors-check.sh "$REPAIR_BASE_SHA"
while IFS= read -r policy; do
  dir=$(dirname "$policy")
  args=(--handsets "$dir/handsets.example.toml")
  [[ ! -f "$dir/trunks.example.toml" ]] || args+=(--trunks "$dir/trunks.example.toml")
  ./bin/doorman check --policy-only --allow-placeholders "${args[@]}" "$policy" >/dev/null
  if ./bin/doorman check --handsets "$dir/handsets.example.toml" "$policy" >/dev/null 2>&1; then
    echo "Example unexpectedly passes strict check: $policy" >&2; exit 1
  fi
done < <(find examples -name policy.example.toml | sort)
./bin/doorman template lint templates/*.toml
roundtrip=$(mktemp -d)
trap 'rm -rf -- "$roundtrip"' EXIT
mkdir -p "$roundtrip/examples"
cp examples/.env.example "$roundtrip/examples/"
cp bin/doorman "$roundtrip/"
(cd "$roundtrip" && ./doorman init --rooms 'Kitchen,Office,Kids Room' >/dev/null && ./doorman check >/dev/null)
for name in '' policy handsets trunks contacts env; do
  ./bin/doorman schema $name | python3 -m json.tool >/dev/null
done
make site-assets
git diff --exit-code -- site/public
mandoc -Tlint docs/doorman.1
(cd site && npm ci && npm run build)
make cross
shellcheck --severity=warning install.sh install-scripts/*.sh scripts/smoke.sh \
  scripts/coverage.sh prompts/build.sh .githooks/pre-push
node --test .github/ai/*.test.cjs
# Public repository: reject real configuration even if force-added past ignores.
secrets=$(git ls-files -- .env policy.toml handsets.toml trunks.toml contacts.toml messages.toml \
  asterisk/pjsip.conf asterisk/ari.conf 'asterisk/generated/*')
[[ -z "$secrets" ]] || { echo 'Private configuration is tracked; refusing push' >&2; exit 1; }
