#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
# Render the arithmetic challenge's clips.
#
# "What is three times six" is spoken from parts — a frame, two digits, an
# operator word — so ten digit clips and a handful of phrases cover every
# question, and nothing is ever synthesised at call time (invariant 7).
#
# The voice is the one the Books menus use: macOS `say` (Samantha by
# default, rate 170), normalised with the same loudnorm as every other
# bundled clip. The clips are the house's, not a pack's — they live in the
# bundled pack's challenge/ directory, outside the six-name contract, and are
# played whatever [line] prompts says (docs/PACKS.md) — so one voice asks the
# question on every line.
#
# Requires: macOS (say) and ffmpeg.   brew install ffmpeg
#   SAY_VOICE=Daniel bash prompts/challenge.sh    # any `say -v '?'` voice
#
# Writes prompts/build/challenge/<name>.wav (8 kHz) and .wav16 (16 kHz),
# beside what prompts/build.sh writes, so the one install step copies both.
# ─────────────────────────────────────────────────────────────
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${HERE}/build/challenge"
MANIFEST="${HERE}/manifest.json"
VOICE="${SAY_VOICE:-Samantha}"

command -v say    >/dev/null || { echo "✗ say not found (macOS only)"; exit 1; }
command -v ffmpeg >/dev/null || { echo "✗ ffmpeg not found"; exit 1; }
# say silently substitutes the default voice for a name it does not know,
# which would publish clips in a voice nobody chose.
say -v '?' | sed 's/#.*//; s/[[:space:]]*[a-z][a-z]_[A-Z0-9]*[[:space:]]*$//' |
  grep -qixF "${VOICE}" || { echo "✗ voice ${VOICE} is not installed; say -v '?' lists them"; exit 1; }

mkdir -p "${OUT}"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

python3 - "$MANIFEST" <<'PY' > "${work}/clips.tsv"
import json, sys
for name, text in json.load(open(sys.argv[1]))["challenge"].items():
    # Brackets would let say read an embedded [[slnc]] command.
    print(f"{name}\t{text.replace('[', '[ ').replace(']', ' ]')}")
PY

while IFS=$'\t' read -r name text; do
  [ -z "${name}" ] && continue
  echo "→ challenge/${name}: ${text}"
  printf '%s' "${text}" | say -v "${VOICE}" -r 170 -o "${work}/${name}.aiff"
  # -nostdin: this loop reads the clip list on stdin, and ffmpeg without it
  # swallows the remaining lines.
  ffmpeg -nostdin -loglevel error -y -i "${work}/${name}.aiff" \
    -af "loudnorm=I=-18:TP=-2:LRA=7" \
    -ar 8000 -ac 1 -acodec pcm_s16le "${OUT}/${name}.wav"
  ffmpeg -nostdin -loglevel error -y -i "${work}/${name}.aiff" \
    -af "loudnorm=I=-18:TP=-2:LRA=7" \
    -ar 16000 -ac 1 -acodec pcm_s16le -f wav "${OUT}/${name}.wav16"
done < "${work}/clips.tsv"

echo
echo "✓ built into ${OUT} (voice ${VOICE})"
echo
echo "Install on the box, beside the bundled pack:"
echo "  sudo mkdir -p /var/lib/asterisk/sounds/call-me-maybe/challenge"
echo "  sudo cp ${OUT}/* /var/lib/asterisk/sounds/call-me-maybe/challenge/"
echo "  sudo chown -R asterisk:asterisk /var/lib/asterisk/sounds/call-me-maybe/challenge"
