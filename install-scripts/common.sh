#!/usr/bin/env bash
# Shared host preparation. Source from a distro entrypoint; never run directly.
set -euo pipefail
profile=${profile:-}

fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
run() {
 if [ "$dry_run" = 1 ]; then printf '  '; printf '%q ' "$@"; printf '\n'; else "$@"; fi
}
major_version() {
 local value=${1#*:}
 [[ "$value" =~ ^([0-9]+)\. ]] || return 1
 printf '%s\n' "${BASH_REMATCH[1]}"
}
require_version() {
 local major
 major=$(major_version "$1") || fail "Cannot determine Asterisk version from '$1'."
 [ "$major" -ge 20 ] || fail "Asterisk $1 is too old; install Asterisk 20+ and rerun with --existing-asterisk."
}
usage() {
 cat <<EOF_USAGE
Usage: $profile.sh [--dry-run] [--existing-asterisk] --binary /path/to/doorman

Prepare a Linux/systemd host from this checkout. Run with sudo to apply.
--dry-run            print the plan without network, root, or host changes
--existing-asterisk  use an already installed Asterisk 20+ and asterisk.service
--binary PATH        Linux doorman binary built from the same checkout
--help               show this help

Installs packages, a doorman account, public assets under /opt/call-me-maybe,
the doorman CLI on the system PATH, and the systemd unit. Preserves configuration and existing binaries/units;
refuses a different binary or unit (use your normal upgrade procedure).
Does not configure calls, start doorman, change firewall rules or disable SELinux.
Distro package installation may start Asterisk according to distro defaults.
EOF_USAGE
}

packages() {
 case "$profile" in
 ubuntu|debian)
  run apt-get update
  if [ "$existing" = 0 ]; then
   if [ "$dry_run" = 0 ]; then
    # Read to END rather than `exit` on the match: under pipefail, awk quitting
    # early leaves apt-cache writing its version table into a closed pipe, and
    # the resulting SIGPIPE (141) aborts the whole script after apt-get update.
    candidate=$(apt-cache policy asterisk | awk '/Candidate:/ && !c {c=$2} END {print c}')
    [ -n "$candidate" ] && [ "$candidate" != '(none)' ] || fail 'No Asterisk candidate. On Ubuntu enable Universe; on Debian provision Asterisk 20+ separately, then use --existing-asterisk.'
    require_version "$candidate"
   fi
   run apt-get install -y asterisk
  fi
  run apt-get install -y ca-certificates rsync openssl sqlite3 acl
  ;;
 centos|fedora)
  if [ "$existing" = 0 ]; then
   if [ "$dry_run" = 0 ]; then
    candidate=$(dnf -q --refresh repoquery --latest-limit=1 --qf '%{version}' asterisk | tail -n 1)
    [ -n "$candidate" ] || fail 'No Asterisk package in enabled repositories. Provision Asterisk 20+ and asterisk.service, then use --existing-asterisk. No repositories are added automatically.'
    require_version "$candidate"
   fi
   run dnf install -y asterisk
  fi
  run dnf install -y ca-certificates rsync openssl sqlite shadow-utils
  ;;
 arch)
  # Arch does not support partial upgrades. AUR builds remain an unprivileged,
  # operator-reviewed step; never run an AUR helper from this root script.
  [ "$existing" = 1 ] || fail 'Arch requires Asterisk 20+ installed first (for example a reviewed AUR package), then --existing-asterisk.'
  run pacman -Syu --needed --noconfirm ca-certificates rsync openssl sqlite shadow
  ;;
 esac
}

check_host() {
 [ "$(uname -s)" = Linux ] || fail 'Apply mode requires Linux. Use --dry-run to preview on a workstation.'
 [ "$(id -u)" = 0 ] || fail 'Run with sudo, or use --dry-run.'
 [ -d /run/systemd/system ] || fail 'A running systemd host is required; a plain Docker container is not sufficient.'
 # /etc/os-release is the distribution-owned identification file.
 # shellcheck disable=SC1091
 . /etc/os-release
 case "$profile:${ID:-}:${VERSION_ID:-}" in
  ubuntu:ubuntu:24.04|ubuntu:ubuntu:26.04|debian:debian:12|debian:debian:13) ;;
  centos:centos:9|centos:centos:10)
   [[ "${NAME:-}" == *Stream* ]] || fail 'Use CentOS Stream; legacy CentOS Linux is unsupported.' ;;
  centos:rocky:9*|centos:rocky:10*|centos:almalinux:9*|centos:almalinux:10*|centos:rhel:9*|centos:rhel:10*) ;;
  fedora:fedora:*) ;;
  arch:arch:*) ;;
  *) fail "This entrypoint does not support ${PRETTY_NAME:-unknown distro}." ;;
 esac
}

# Existing configuration and service customisations always win. An installer
# must not quietly turn a rerun into an upgrade of a working phone system.
install_once() {
 local source=$1 target=$2 mode=$3
 if [ -e "$target" ] || [ -L "$target" ]; then
  cmp -s "$source" "$target" || fail "$target already exists with different content; review and upgrade it explicitly."
 else
  run install -m "$mode" "$source" "$target"
 fi
}

prepare() {
 dry_run=0; existing=0; binary=''
 local repo candidate file name target source
 repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
 while [ "$#" -gt 0 ]; do
  case "$1" in
   --dry-run) dry_run=1; shift ;;
   --existing-asterisk) existing=1; shift ;;
   --binary) [ "$#" -ge 2 ] && [ -n "$2" ] || fail '--binary requires a path'; binary=$2; shift 2 ;;
   --help|-h) usage; return ;;
   *) fail "Unknown option: $1" ;;
  esac
 done
 [ -n "$binary" ] || fail 'Supply --binary (build on the workstation with make cross, then copy the matching Linux binary to the hub).'
 [ "$dry_run" = 1 ] || check_host
 [ -f "$binary" ] || fail "Binary not found: $binary"
 if [ "$dry_run" = 0 ]; then
  [ -x "$binary" ] || fail 'The binary must be executable.'
  [[ "$("$binary" version)" == doorman\ * ]] || fail 'The binary cannot run on this host or is not doorman.'
  if [ "$existing" = 1 ]; then
   command -v asterisk >/dev/null || fail 'Asterisk is not installed.'
   require_version "$(asterisk -V | awk '{print $2}')"
  fi
  # Catch conflicts before spending time installing packages.
  for target in /opt/call-me-maybe/bin/doorman /usr/local/bin/doorman /etc/systemd/system/doorman.service /etc/systemd/system/doorman-directory.service; do
   source=$binary
   [ "$target" != /etc/systemd/system/doorman.service ] || source=$repo/scripts/doorman.service
   [ "$target" != /etc/systemd/system/doorman-directory.service ] || source=$repo/scripts/doorman-directory.service
   if [ -e "$target" ] || [ -L "$target" ]; then
    cmp -s "$source" "$target" || fail "$target differs; use the documented upgrade procedure."
   fi
  done
 fi
 printf 'Preparing %s host; Asterisk must be version 20 or newer.\n' "$profile"
 packages
 if [ "$dry_run" = 0 ]; then
  require_version "$(asterisk -V | awk '{print $2}')"
  [ "$(systemctl show asterisk.service -p LoadState --value)" = loaded ] || fail 'Install an asterisk.service unit before proceeding.'
 fi
 if [ "$dry_run" = 1 ] || ! getent passwd doorman >/dev/null; then
  run useradd --system --user-group --no-create-home --home-dir /opt/call-me-maybe --shell /usr/sbin/nologin doorman
 fi
 # Debian and Ubuntu ship sample dialplans in AEL and Lua that load beside
 # extensions.conf, adding contexts and startup warnings that are not ours.
 # Set them aside (kept as .distro, like every original) and tell Asterisk
 # not to load the two engines; extensions.conf is the only dialplan here.
 for sample in extensions.ael extensions.lua; do
  if [ -e "/etc/asterisk/$sample" ] && [ ! -e "/etc/asterisk/$sample.distro" ]; then
   run mv "/etc/asterisk/$sample" "/etc/asterisk/$sample.distro"
  fi
 done
 if [ -e /etc/asterisk/modules.conf ] && ! grep -qE '^noload => pbx_ael\.so' /etc/asterisk/modules.conf; then
  run sh -c 'printf "\n; call-me-maybe: extensions.conf is the only dialplan\nnoload => pbx_ael.so\nnoload => pbx_lua.so\n" >> /etc/asterisk/modules.conf'
 fi
 # 0755, not 0750: the directory holds nothing secret (the secrets are 0600
 # files owned by doorman, and asterisk/generated is 0700), and 0750 locked the
 # operator out of `cd /opt/call-me-maybe` — the first step of every next step.
 run install -d -o doorman -g doorman -m 0755 /opt/call-me-maybe
 run install -d -m 0755 /opt/call-me-maybe/bin
 # Copy only installation assets, never ignored .env/config/generated files
 # from the workstation checkout. Reruns preserve destination edits.
 run install -d -m 0755 /opt/call-me-maybe/examples /opt/call-me-maybe/templates /opt/call-me-maybe/docs
 for file in "$repo"/examples/*.example.toml "$repo/examples/.env.example"; do
  install_once "$file" "/opt/call-me-maybe/examples/${file##*/}" 0644
 done
 run rsync -rt --ignore-existing --include='*/' --include='*.md' --include='*.1' --exclude='*' --chmod=D755,F644 "$repo/docs/" /opt/call-me-maybe/docs/
 run rsync -rt --ignore-existing --include='*/' --include='*.toml' --exclude='*' --chmod=D755,F644 "$repo/templates/" /opt/call-me-maybe/templates/
 run install -d -m 0755 /opt/call-me-maybe/asterisk /opt/call-me-maybe/scripts
 run install -d -o doorman -g doorman -m 0700 /opt/call-me-maybe/asterisk/generated
 for name in ari.conf.example pjsip.conf.example voicemail.conf.example extensions.conf http.conf rtp.conf musiconhold.conf res_parking.conf cel.conf cel_sqlite3_custom.conf pjsip_notify.conf; do
  install_once "$repo/asterisk/$name" "/opt/call-me-maybe/asterisk/$name" 0644
 done
 install_once "$repo/install-scripts/README.md" /opt/call-me-maybe/docs/INSTALL-LINUX.md 0644
 install_once "$repo/scripts/smoke.sh" /opt/call-me-maybe/scripts/smoke.sh 0755
 install_once "$repo/scripts/cel-spool.sql" /opt/call-me-maybe/scripts/cel-spool.sql 0644
 install_once "$binary" /opt/call-me-maybe/bin/doorman 0755
 # The same binary on the system PATH — which is also sudo's secure_path — so
 # the operator can say `sudo -u doorman doorman init` instead of spelling out
 # /opt/call-me-maybe/bin/doorman. The unit keeps running the /opt copy.
 run install -d -m 0755 /usr/local/bin
 install_once "$binary" /usr/local/bin/doorman 0755
 install_once "$repo/scripts/doorman.service" /etc/systemd/system/doorman.service 0644
 install_once "$repo/scripts/doorman-directory.service" /etc/systemd/system/doorman-directory.service 0644
 # `doorman provision notify` sends a check-sync through the Asterisk console,
 # which needs asterisk.conf and the control socket. Exactly that one command,
 # nothing else, for the service account — narrower than the asterisk group.
 install_once "$repo/scripts/doorman-notify.sudoers" /etc/sudoers.d/doorman-notify 0440
 run install -d -o doorman -g doorman -m 0700 /var/lib/doorman /var/lib/doorman/journal /var/lib/doorman/provision
 run systemctl daemon-reload
 cat <<'EOF_NEXT'
Host preparation complete. No doorman service was started.
Next, from /opt/call-me-maybe/docs/INSTALL-LINUX.md "Finish configuration":
  cd /opt/call-me-maybe && sudo -u doorman doorman init
then RUNBOOK.md "Asterisk config": install matching ARI credentials, render
handsets/trunks, copy the rendered files and prerecorded prompts, validate
with doorman check, then enable the services and run scripts/smoke.sh.
`doorman` is on the PATH; `sudo -u doorman` is about ownership, not privilege —
the service account must be able to read (and rotate) what it writes.
Keep ARI bound to 127.0.0.1. Review SIP/RTP firewall access for your own LAN
and provider. SELinux/AppArmor remain enabled; check their logs during validation.
EOF_NEXT
}
