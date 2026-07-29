#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
# Deployment verification. Run ON THE PI.
#
#   ./scripts/smoke.sh
#
# Walks the verification ladder from docs/RUNBOOK.md section 3, bottom to top.
# Each rung depends on the ones below it, so fix the first failure and re-run
# rather than chasing all of them at once.
#
# Exit 0 = every check passed. Exit 1 = at least one FAIL.
# WARN does not fail the run; it flags things that are probably fine but worth
# a look.
# ─────────────────────────────────────────────────────────────
set -uo pipefail

REPO="${REPO:-/opt/call-me-maybe}"
PASS=0; FAIL=0; WARN=0

if [ -t 1 ]; then
  G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; B=$'\033[1m'; N=$'\033[0m'
else
  G=""; R=""; Y=""; B=""; N=""
fi

pass() { printf '  %s✓%s %s\n' "$G" "$N" "$1"; PASS=$((PASS+1)); }
fail() { printf '  %s✗%s %s\n' "$R" "$N" "$1"; [ $# -gt 1 ] && printf '      → %s\n' "$2"; FAIL=$((FAIL+1)); }
warn() { printf '  %s!%s %s\n' "$Y" "$N" "$1"; [ $# -gt 1 ] && printf '      → %s\n' "$2"; WARN=$((WARN+1)); }
rung() { printf '\n%s%s%s\n' "$B" "$1" "$N"; }

# Degrade quietly on machines without sudo or systemd rather than spraying
# stderr — this script is often the first thing run on a half-built box.
if command -v sudo >/dev/null; then SUDO=sudo; else SUDO=""; fi
ast() { $SUDO asterisk -rx "$1" 2>/dev/null; }
svc_active() { systemctl is-active --quiet "$1" 2>/dev/null; }

# Pull ARI settings out of .env without sourcing it (it may contain anything).
envval() {
  [ -f "$REPO/.env" ] || return 1
  $SUDO grep -E "^$1=" "$REPO/.env" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"'"'"''
}

printf '%sCall Me Maybe — smoke test%s\n' "$B" "$N"
printf 'repo: %s   host: %s   %s\n' "$REPO" "$(hostname)" "$(date -Is)"

# ── Rung 0: prerequisites ────────────────────────────────────
rung "0. Prerequisites"

command -v asterisk >/dev/null && pass "asterisk installed ($(asterisk -V 2>/dev/null))" \
  || fail "asterisk not installed" "sudo apt install asterisk"
DOORMAN="$REPO/bin/doorman"
[ -x "$DOORMAN" ] && pass "doorman binary present ($($DOORMAN version 2>/dev/null))" \
  || fail "doorman binary missing at $DOORMAN" "make cross on a workstation, scp to the Pi (RUNBOOK §2)"
[ -d "$REPO" ] && pass "repo present at $REPO" || fail "repo not found at $REPO"

# ── Rung 1: asterisk running ─────────────────────────────────
rung "1. Asterisk"

if svc_active asterisk; then
  pass "asterisk service active"
else
  fail "asterisk not running" "sudo systemctl start asterisk"
fi

# ── Rung 2: trunk registration ───────────────────────────────
rung "2. VoIP.ms trunk"

REGS="$(ast 'pjsip show registrations')"
if [ -z "$REGS" ]; then
  fail "could not query registrations" "is asterisk up? does your user have CLI access?"
elif grep -qi 'Registered' <<<"$REGS"; then
  pass "trunk registered"
elif grep -qi 'Rejected' <<<"$REGS"; then
  fail "registration rejected" "wrong sub-account username/password in pjsip.conf"
elif grep -qi 'Unregistered\|Stopped' <<<"$REGS"; then
  fail "trunk not registered" "cannot reach the POP — check DNS and egress firewall"
else
  warn "unrecognised registration state" "$(tail -2 <<<"$REGS" | tr '\n' ' ')"
fi

# The pairing that makes inbound work without port forwarding.
PJSIP_CONF=/etc/asterisk/pjsip.conf
if $SUDO test -r "$PJSIP_CONF" 2>/dev/null; then
  if $SUDO grep -q '^line=yes' "$PJSIP_CONF" && $SUDO grep -q '^endpoint=' "$PJSIP_CONF"; then
    pass "registration has line=yes + endpoint= (inbound will match)"
  else
    fail "registration missing line=yes and/or endpoint=" \
         "without these, inbound calls hit the anonymous endpoint and vanish"
  fi
  # The single most common silent failure in this whole system.
  if $SUDO grep -q 'dtmf_mode=rfc4733' "$PJSIP_CONF"; then
    DTMF_N=$($SUDO grep -c 'dtmf_mode=rfc4733' "$PJSIP_CONF")
    pass "dtmf_mode=rfc4733 present ($DTMF_N endpoints)"
  else
    fail "dtmf_mode=rfc4733 not found" \
         "the lobby will never hear an extension; every stranger gets dismissed"
  fi
else
  warn "cannot read $PJSIP_CONF" "re-run with sudo for config checks"
fi

# ── Rung 3: handsets ─────────────────────────────────────────
rung "3. Handsets"

CONTACTS="$(ast 'pjsip show contacts')"
AVAIL=$(grep -ci 'Avail' <<<"$CONTACTS" || true)
if [ "${AVAIL:-0}" -gt 0 ]; then
  pass "$AVAIL handset contact(s) available"
else
  fail "no handsets registered" "check the phones' own SIP config, not asterisk"
fi

# ── Rung 4: ARI ──────────────────────────────────────────────
rung "4. ARI"

ARI_USER="$(envval ARI_USERNAME || echo doorman)"
ARI_PASS="$(envval ARI_PASSWORD || echo '')"
ARI_URL="$(envval ARI_BASE_URL || echo http://127.0.0.1:8088)"

if [ -z "$ARI_PASS" ]; then
  warn "no ARI_PASSWORD readable from .env" "skipping live ARI checks"
else
  CODE=$(curl -s -o /tmp/.cmm-ari -w '%{http_code}' -u "$ARI_USER:$ARI_PASS" \
           "$ARI_URL/ari/asterisk/info" 2>/dev/null || echo 000)
  case "$CODE" in
    200) pass "ARI responded 200 as user '$ARI_USER'" ;;
    401) fail "ARI rejected credentials" "ari.conf and .env disagree on the password" ;;
    000) fail "ARI unreachable at $ARI_URL" "check http.conf: sudo asterisk -rx 'http show status'" ;;
    *)   fail "ARI returned HTTP $CODE" ;;
  esac
  rm -f /tmp/.cmm-ari
fi

if ast 'http show status' | grep -q '127.0.0.1'; then
  pass "ARI http bound to loopback"
else
  warn "ARI http may not be loopback-bound" "it should never be reachable off-box"
fi

# ── Rung 5: doorman ──────────────────────────────────────────
rung "5. doorman"

if svc_active doorman; then
  pass "doorman service active"
else
  fail "doorman not running" "sudo systemctl status doorman; journalctl -u doorman -n 50"
fi

if ast 'ari show apps' | grep -q 'doorman'; then
  pass "stasis app 'doorman' registered"
else
  fail "stasis app not registered" \
       "doorman's websocket is not connected; check ARI_APP matches Stasis() in extensions.conf"
fi

for gen in pjsip_handsets.conf extensions_handsets.conf; do
  if $SUDO test -f "/etc/asterisk/$gen" 2>/dev/null; then
    pass "generated $gen installed"
  else
    fail "/etc/asterisk/$gen missing" "doorman render, then copy per its instructions"
  fi
done

if ast 'dialplan show inbound-trunk' | grep -q 'Stasis'; then
  pass "inbound-trunk dialplan hands off to Stasis"
else
  fail "inbound-trunk context missing or not calling Stasis" \
       "sudo cp $REPO/asterisk/extensions.conf /etc/asterisk/ && sudo asterisk -rx 'dialplan reload'"
fi

# ── Rung 6: prompts ──────────────────────────────────────────
rung "6. Prompts"

PREFIX="$(envval PROMPT_MEDIA_PREFIX || echo call-me-maybe)"
SOUNDS="/var/lib/asterisk/sounds/$PREFIX"
REQUIRED=(welcome-known lobby-greeting invalid-extension good-day no-answer connecting)

if [ -d "$SOUNDS" ]; then
  MISSING=()
  for name in "${REQUIRED[@]}"; do
    [ -f "$SOUNDS/$name.wav" ] || MISSING+=("$name")
  done
  if [ ${#MISSING[@]} -eq 0 ]; then
    pass "all ${#REQUIRED[@]} prompts present in $SOUNDS"
  else
    fail "missing prompts: ${MISSING[*]}" "bun run prompts:build, then copy to $SOUNDS"
  fi

  OWNER=$(stat -c '%U' "$SOUNDS" 2>/dev/null)
  [ "$OWNER" = "asterisk" ] && pass "prompts owned by asterisk" \
    || fail "prompts owned by '$OWNER'" "sudo chown -R asterisk:asterisk $SOUNDS"

  if command -v soxi >/dev/null && [ -f "$SOUNDS/good-day.wav" ]; then
    RATE=$(soxi -r "$SOUNDS/good-day.wav" 2>/dev/null)
    [ "$RATE" = "8000" ] && pass "prompts at 8 kHz" \
      || warn "good-day.wav is ${RATE} Hz, expected 8000" "asterisk will transcode on every call"
  fi
else
  fail "prompt directory $SOUNDS not found" "see RUNBOOK section 2"
fi

# ── Rung 7: policy ───────────────────────────────────────────
rung "7. Policy"

if [ -f "$REPO/policy.toml" ]; then
  if "$DOORMAN" check "$REPO/policy.toml" >/tmp/.cmm-policy 2>&1; then
    pass "policy.toml valid"
    sed 's/^/      /' /tmp/.cmm-policy | grep -E 'allow-listed|extensions|pin length' || true
  else
    fail "policy.toml invalid" "$(head -3 /tmp/.cmm-policy | tr '\n' ' ')"
  fi
  rm -f /tmp/.cmm-policy

  PERM=$(stat -c '%a' "$REPO/policy.toml" 2>/dev/null)
  [ "$PERM" = "600" ] && pass "policy.toml is 0600" \
    || warn "policy.toml is $PERM, expected 600" "it contains PINs: sudo chmod 600 $REPO/policy.toml"
else
  fail "policy.toml not found" "cp policy.example.toml policy.toml"
fi

if [ -f "$REPO/handsets.toml" ]; then
  pass "handsets.toml present (validated by doorman check above)"
else
  warn "no handsets.toml" "legacy single-file layout; see RUNBOOK: Config interfaces"
fi

if [ -f "$REPO/.env" ]; then
  PERM=$(stat -c '%a' "$REPO/.env" 2>/dev/null)
  [ "$PERM" = "600" ] && pass ".env is 0600" \
    || warn ".env is $PERM, expected 600" "sudo chmod 600 $REPO/.env"
else
  fail ".env not found" "cp .env.example .env"
fi

# ── Rung 8: recent behaviour ─────────────────────────────────
rung "8. Last 24h"

if command -v journalctl >/dev/null; then
  ERRS=$(journalctl -u doorman --since '24 hours ago' 2>/dev/null | grep -c '"level":"error"' || true)
  [ "${ERRS:-0}" -eq 0 ] && pass "no errors logged" \
    || warn "$ERRS error line(s) in the last 24h" "journalctl -u doorman --since '24 hours ago' | grep error"

  DISMISS=$(journalctl -u doorman --since '24 hours ago' 2>/dev/null | grep -c 'dismissing caller' || true)
  RATED=$(journalctl -u doorman --since '24 hours ago' 2>/dev/null | grep -c 'rate limited' || true)
  printf '      dismissed: %s   rate-limited: %s\n' "${DISMISS:-0}" "${RATED:-0}"
  [ "${RATED:-0}" -gt 10 ] && warn "high rate-limit activity" "someone may be probing; consider rotating PINs"
fi

# ── Summary ──────────────────────────────────────────────────
printf '\n%s─────────────────────────────%s\n' "$B" "$N"
printf '%s%d passed%s   %s%d failed%s   %s%d warnings%s\n' \
  "$G" "$PASS" "$N" "$R" "$FAIL" "$N" "$Y" "$WARN" "$N"

if [ "$FAIL" -gt 0 ]; then
  printf '\nFix the FIRST failure and re-run — the rungs depend on each other.\n'
  printf 'Troubleshooting: docs/RUNBOOK.md section 4\n'
  exit 1
fi

printf '\nAll checks passed. Place a test call from a number NOT in policy.toml\n'
printf 'and confirm you hear the lobby greeting.\n'
exit 0
