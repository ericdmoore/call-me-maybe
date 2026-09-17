#!/usr/bin/env bash
##
## Refresh one complete source; credentials belong to the exporter environment.
## UPDATE: This file may turn into a bullmoose CLI driver to pull down contacts nightly

set -euo pipefail
umask 077
if [[ $# -lt 5 || $4 != -- ]]; then
  echo 'Usage: fetchContacts.sh SOURCE OWNER OUTPUT -- EXPORTER [ARG ...]' >&2
  exit 2
fi
source_id=$1 owner=$2 output=$3
shift 4
[[ $source_id =~ ^[a-z0-9][a-z0-9_-]*$ && $owner =~ ^[a-z0-9][a-z0-9_-]*$ ]] || {
  echo 'Source and owner must be lowercase identifiers.' >&2; exit 2;
}
command -v jq >/dev/null
# Prefix relative paths so filenames beginning with a dash remain filenames.
[[ $output = /* ]] || output=./$output
mkdir -p "$(dirname "$output")"
lock="${output}.lock"
mkdir "$lock" 2>/dev/null || { echo 'Refresh already locked; retry later.' >&2; exit 1; }
work=''
cleanup() { [[ -z $work ]] || rm -rf "$work"; rmdir "$lock"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
work=$(mktemp -d "${output}.tmp.XXXXXX")
# Do not print exporter diagnostics: they can contain contact data or tokens.
if ! "$@" > "$work/raw" 2> "$work/export-error"; then
  echo 'Contact export failed; previous snapshot retained.' >&2; exit 1
fi
# Bullmoose contacts export --json already emits one JSContact object per line.
# Stable sorting avoids rewrites caused solely by export order or object keys.
if ! jq -csS --arg source "$source_id" --arg owner "$owner" '
  if all(.[]; type == "object" and ((.uid // .id) | type == "string" and length > 0))
  then sort_by(.uid // .id) |
    if (map(.uid // .id) | unique | length) != length then error("duplicate identity")
    else .[] | {version: 1, source: $source, owner: $owner, contact: .} end
  else error("expected contact objects with uid or id") end
' "$work/raw" > "$work/next" 2> "$work/validation-error"; then
  echo 'Invalid contact export; previous snapshot retained.' >&2; exit 1
fi
if [[ -f $output ]] && cmp -s "$work/next" "$output"; then
  echo unchanged
else
  mv -f "$work/next" "$output"
  echo updated
fi
