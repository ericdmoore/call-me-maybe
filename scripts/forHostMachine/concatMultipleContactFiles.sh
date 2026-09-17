#!/usr/bin/env bash

##
## Combine snapshots, preserving ownership; this does NOT generate an allow-list.
##
##
##

set -euo pipefail
umask 077
if [[ $# -lt 2 ]]; then
  echo 'Usage: concatMultipleContactFiles.sh OUTPUT INPUT [INPUT ...]' >&2
  exit 2
fi
output=$1
shift
command -v jq >/dev/null
[[ $output = /* ]] || output=./$output
mkdir -p "$(dirname "$output")"
lock="${output}.lock"
mkdir "$lock" 2>/dev/null || { echo 'Merge already locked; retry later.' >&2; exit 1; }
work=''
cleanup() { [[ -z $work ]] || rm -rf "$work"; rmdir "$lock"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
work=$(mktemp -d "${output}.tmp.XXXXXX")
for input in "$@"; do
  [[ $input = /* ]] || input=./$input
  [[ -f $input && -r $input ]] || { echo 'Missing snapshot; previous merge retained.' >&2; exit 1; }
  [[ ! -e $output || ! $input -ef $output ]] || { echo 'Output cannot also be an input.' >&2; exit 2; }
  # Parse each file independently; an invalid trailing fragment cannot join the next file.
  if ! jq -csS '
    if all(.[]; type == "object" and .version == 1 and
      (.source | type == "string" and test("^[a-z0-9][a-z0-9_-]*$")) and
      (.owner | type == "string" and test("^[a-z0-9][a-z0-9_-]*$")) and
      (.contact | type == "object") and
      ((.contact.uid // .contact.id) | type == "string" and length > 0))
    then .[] else error("invalid snapshot") end
  ' "$input" >> "$work/records" 2> "$work/error"; then
    echo 'Invalid snapshot; previous merge retained.' >&2; exit 1
  fi
done
# Duplicate source/contact identities are ambiguous, not silently first-wins.
if ! jq -csS '
  sort_by([.source, (.contact.uid // .contact.id)]) |
  if (group_by([.source, (.contact.uid // .contact.id)]) | any(.[]; length > 1))
  then error("duplicate source/contact") else .[] end
' "$work/records" > "$work/next" 2> "$work/error"; then
  echo 'Conflicting snapshots; previous merge retained.' >&2; exit 1
fi
if [[ -f $output ]] && cmp -s "$work/next" "$output"; then
  echo unchanged
else
  mv -f "$work/next" "$output"
  echo updated
fi
