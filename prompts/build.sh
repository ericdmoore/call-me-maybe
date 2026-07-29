#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
# Render the lobby's voice.
#
# Prompts are built ONCE on a workstation and committed as WAVs, not
# synthesised at call time. The Pi does zero TTS work, which is why the
# lobby answers and starts speaking in well under a second — and why a
# broken TTS service can never make the house phone unreachable.
#
# Requires: piper (or PIPER_CMD pointing at one) and ffmpeg.
#   brew install ffmpeg
#   pipx install piper-tts
#
# Two formats are produced per prompt:
#   <name>.wav    8 kHz  mono 16-bit  -> played on ulaw calls
#   <name>.wav16  16 kHz mono 16-bit  -> played on g722 (wideband) calls
# Asterisk picks whichever needs less transcoding.
# ─────────────────────────────────────────────────────────────
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${HERE}/build"
MANIFEST="${HERE}/manifest.json"

PIPER_CMD="${PIPER_CMD:-piper}"
PIPER_VOICE="${PIPER_VOICE:-${HOME}/.local/share/piper/en_GB-alba-medium.onnx}"

command -v "${PIPER_CMD}" >/dev/null || { echo "✗ piper not found (set PIPER_CMD)"; exit 1; }
command -v ffmpeg      >/dev/null || { echo "✗ ffmpeg not found"; exit 1; }
[ -f "${PIPER_VOICE}" ] || { echo "✗ voice model not found: ${PIPER_VOICE}"; exit 1; }

mkdir -p "${OUT}"

# Read name/text pairs out of manifest.json without needing jq.
python3 - "$MANIFEST" <<'PY' > /tmp/cmm-prompts.tsv
import json, sys
for name, text in json.load(open(sys.argv[1]))["prompts"].items():
    print(f"{name}\t{text}")
PY

while IFS=$'\t' read -r name text; do
  [ -z "${name}" ] && continue
  echo "→ ${name}: ${text}"
  raw="${OUT}/${name}.raw.wav"

  printf '%s' "${text}" | "${PIPER_CMD}" --model "${PIPER_VOICE}" --output_file "${raw}"

  # 8 kHz narrowband. -af loudnorm keeps every prompt at a consistent level;
  # nothing is worse than a greeting you can barely hear followed by a
  # "Good day" that peaks the line.
  ffmpeg -loglevel error -y -i "${raw}" \
    -af "loudnorm=I=-18:TP=-2:LRA=7" \
    -ar 8000 -ac 1 -acodec pcm_s16le "${OUT}/${name}.wav"

  # 16 kHz wideband for g722.
  ffmpeg -loglevel error -y -i "${raw}" \
    -af "loudnorm=I=-18:TP=-2:LRA=7" \
    -ar 16000 -ac 1 -acodec pcm_s16le "${OUT}/${name}.wav16"

  rm -f "${raw}"
done < /tmp/cmm-prompts.tsv

rm -f /tmp/cmm-prompts.tsv

echo
echo "✓ built into ${OUT}"
echo
echo "Install on the Pi:"
echo "  PREFIX=\${PROMPT_MEDIA_PREFIX:-call-me-maybe}"
echo "  sudo mkdir -p /var/lib/asterisk/sounds/\$PREFIX"
echo "  rsync -av ${OUT}/ pi@raspberrypi:/tmp/cmm-prompts/"
echo "  sudo cp /tmp/cmm-prompts/* /var/lib/asterisk/sounds/\$PREFIX/"
echo "  sudo chown -R asterisk:asterisk /var/lib/asterisk/sounds/\$PREFIX"
