#!/bin/bash
set -euo pipefail
: "${AI_JOB_DIR:?}" "${AI_POLICY:?}" "${REPAIR_HEAD_SHA:?}" "${REPAIR_HEAD_REF:?}"
: "${OPENCODE_BIN:?}" "${REPAIR_MODEL:?}"
[[ "$("$OPENCODE_BIN" --version)" == '1.18.34' ]] || { echo 'OpenCode version mismatch'; exit 1; }
[[ "$(git ls-remote origin "$REPAIR_HEAD_REF" | cut -f1)" == "$REPAIR_HEAD_SHA" ]] || {
  echo 'PR head changed before repair'; exit 1;
}
git fetch origin "$REPAIR_HEAD_REF"
git checkout --detach "$REPAIR_HEAD_SHA"
git config user.name 'github-actions[bot]'
git config user.email '41898282+github-actions[bot]@users.noreply.github.com'
git config core.hooksPath "$AI_POLICY"
node "$AI_POLICY/isolate.cjs" "$AI_JOB_DIR" "$OPENCODE_BIN" run --pure --auto \
  "$(cat "$AI_JOB_DIR/prompt.md")" \
  --model "$REPAIR_MODEL" --format json \
  | tee "$AI_JOB_DIR/opencode.jsonl"
# Fail closed on upstream error events (the CLI may exit zero after an API error).
node - "$AI_JOB_DIR/opencode.jsonl" "$AI_POLICY/repair-result.cjs" <<'JS'
require(process.argv[3])(require('fs').readFileSync(process.argv[2], 'utf8'));
JS
[[ "$(git rev-parse HEAD)" == "$REPAIR_HEAD_SHA" ]] || { echo 'Agent changed HEAD; refusing push'; exit 1; }
[[ "$(git ls-remote origin "$REPAIR_HEAD_REF" | cut -f1)" == "$REPAIR_HEAD_SHA" ]] || {
  echo 'PR head changed during repair'; exit 1;
}
git add -A
# Keep policy, hooks, model settings and validation thresholds outside agent edits.
if ! git diff --cached --quiet -- .github .githooks .opencodereview opencode.json opencode.jsonc .opencode scripts/coverage.floors; then
  echo 'Repair changed protected automation/configuration; human review required'; exit 1
fi
if git diff --cached --quiet; then
  echo 'No code changes; see finding replies and agent assessment.'
  exit 0
fi
git diff --cached --check
git commit -m "Repair validated OCR findings for PR #$PR_NUMBER"
# Reinstall trusted hook in case repository tooling modified core.hooksPath.
git -c core.hooksPath="$AI_POLICY" push origin "HEAD:$REPAIR_HEAD_REF"
git rev-parse HEAD > "$AI_JOB_DIR/pushed-sha"
