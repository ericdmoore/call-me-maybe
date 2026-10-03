#!/bin/bash
set -euo pipefail
: "${AI_JOB_DIR:?}" "${REVIEW_HEAD:?}" "${REVIEW_BASE:?}" "${REVIEW_MODEL:?}"
[[ "$(/opt/homebrew/bin/ocr --version)" == *'1.12.11 '* ]] || { echo 'OCR version mismatch'; exit 1; }
isolated() { node "$AI_POLICY/isolate.cjs" "$AI_JOB_DIR" "$@"; }
isolated /opt/homebrew/bin/ocr config set telemetry.enabled false
isolated /opt/homebrew/bin/ocr config set max_tokens 32768
isolated /opt/homebrew/bin/ocr config set llm.extra_body '{"reasoning_effort":"medium"}'
# Diff objects only: no PR scripts, dependency installation or head checkout.
git fetch origin "pull/$PR_NUMBER/head"
git cat-file -e "$REVIEW_HEAD^{commit}"
base=$(git merge-base "$REVIEW_BASE" "$REVIEW_HEAD")
set +e
isolated /opt/homebrew/bin/ocr review --from "$base" --to "$REVIEW_HEAD" \
  --format json --audience human --concurrency 1 --max-tokens 32768 \
  --effort "$REVIEW_EFFORT" --timeout 45 --max-tokens-budget "$REVIEW_BUDGET" \
  --background-file "$AI_JOB_DIR/background.md" --rule "$AI_POLICY/rule.json" \
  > "$AI_JOB_DIR/result.json" 2> >(tee "$AI_JOB_DIR/stderr.log" >&2)
status=$?
set -e
echo "exit_code=$status" >> "$GITHUB_OUTPUT"
exit "$status"
