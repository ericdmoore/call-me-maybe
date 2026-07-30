#!/usr/bin/env bash
# Call Me Maybe — doorman installer.
#
#   curl -fsSL https://raw.githubusercontent.com/ericdmoore/call-me-maybe/main/install.sh | bash
#
# Or, with options:
#   ./install.sh --version v0.5.0 --prefix ~/.local/bin
#
# Downloads the release binary for this host, verifies its SHA-256 against
# the published checksums file, and installs it. Does not touch Asterisk,
# systemd, or any config — see docs/RUNBOOK.md for provisioning.
set -euo pipefail

REPO="ericdmoore/call-me-maybe"
BIN="doorman"
VERSION="${VERSION:-latest}"
PREFIX="${PREFIX:-}"
FORCE=0

# ── Output ───────────────────────────────────────────────────────────
if [ -t 2 ]; then
	RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; DIM=$'\033[2m'; OFF=$'\033[0m'
else
	RED=""; GRN=""; YEL=""; DIM=""; OFF=""
fi
info() { printf '%s→%s %s\n' "$DIM" "$OFF" "$1" >&2; }
warn() { printf '%s!%s %s\n' "$YEL" "$OFF" "$1" >&2; }
die()  { printf '%s✗%s %s\n' "$RED" "$OFF" "$1" >&2; exit 1; }
ok()   { printf '%s✓%s %s\n' "$GRN" "$OFF" "$1" >&2; }

usage() {
	cat <<EOF
doorman installer

Usage: install.sh [options]

Options:
  --version <tag>   release to install (default: latest)
  --prefix <dir>    install directory (default: /usr/local/bin, or
                    ~/.local/bin when /usr/local/bin is not writable)
  --force           reinstall even if this version is already present
  --help            this text

Environment: VERSION, PREFIX, and GITHUB_TOKEN (for API rate limits) are
honoured as defaults.
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
		--version) VERSION="${2:?--version needs a tag}"; shift 2 ;;
		--prefix)  PREFIX="${2:?--prefix needs a directory}"; shift 2 ;;
		--force)   FORCE=1; shift ;;
		--help|-h) usage; exit 0 ;;
		*) die "unknown option: $1 (try --help)" ;;
	esac
done

# ── Dependencies ─────────────────────────────────────────────────────
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }
need uname
need mktemp
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL ${GITHUB_TOKEN:+-H "Authorization: Bearer $GITHUB_TOKEN"} "$1"; }
	fetch_to() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO- "$1"; }
	fetch_to() { wget -q -O "$2" "$1"; }
else
	die "need curl or wget"
fi

# ── Host detection ───────────────────────────────────────────────────
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
	linux|darwin) ;;
	*) die "unsupported OS: $os (doorman runs on Linux; macOS is for check/render/lsp)" ;;
esac

machine=$(uname -m)
case "$machine" in
	aarch64|arm64)   arch="arm64" ;;
	armv7l|armv7|armv6l) arch="armv7" ;;
	x86_64|amd64)    arch="amd64" ;;
	*) die "unsupported architecture: $machine" ;;
esac

# A 64-bit Pi kernel running a 32-bit userland reports aarch64 but cannot
# execute an arm64 binary. Check the C library's actual bitness.
if [ "$os" = "linux" ] && [ "$arch" = "arm64" ] && command -v getconf >/dev/null 2>&1; then
	if [ "$(getconf LONG_BIT 2>/dev/null || echo 64)" = "32" ]; then
		warn "64-bit kernel with a 32-bit userland — using the armv7 build"
		arch="armv7"
	fi
fi

if [ "$os" = "darwin" ] && [ "$arch" = "armv7" ]; then
	die "no armv7 build for macOS"
fi

asset="${BIN}-${os}-${arch}"
info "host: ${os}/${machine} → ${asset}"

# ── Resolve the version ──────────────────────────────────────────────
if [ "$VERSION" = "latest" ]; then
	info "resolving latest release"
	VERSION=$(fetch "https://api.github.com/repos/${REPO}/releases/latest" \
		| sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
		| head -1)
	[ -n "$VERSION" ] || die "could not resolve the latest release (no releases published yet?)"
fi
case "$VERSION" in v*) ;; *) VERSION="v${VERSION}" ;; esac
info "version: $VERSION"

# ── Where to install ─────────────────────────────────────────────────
if [ -z "$PREFIX" ]; then
	if [ -w /usr/local/bin ] 2>/dev/null; then
		PREFIX=/usr/local/bin
	elif [ "$(id -u)" = "0" ]; then
		PREFIX=/usr/local/bin
	else
		PREFIX="$HOME/.local/bin"
		info "/usr/local/bin is not writable, using $PREFIX"
	fi
fi
mkdir -p "$PREFIX" || die "cannot create $PREFIX"
[ -w "$PREFIX" ] || die "$PREFIX is not writable (re-run with sudo, or --prefix ~/.local/bin)"

target="${PREFIX}/${BIN}"

# ── Already installed? ───────────────────────────────────────────────
if [ "$FORCE" = "0" ] && [ -x "$target" ]; then
	current=$("$target" version 2>/dev/null | awk '{print $2}' || true)
	if [ "v${current:-none}" = "$VERSION" ]; then
		ok "doorman $VERSION is already installed at $target"
		exit 0
	fi
	[ -n "$current" ] && info "upgrading $current → ${VERSION#v}"
fi

# ── Download and verify ──────────────────────────────────────────────
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

base="https://github.com/${REPO}/releases/download/${VERSION}"
info "downloading $asset"
fetch_to "${base}/${asset}" "${tmp}/${asset}" \
	|| die "download failed — does $VERSION publish a $asset asset?"

info "verifying checksum"
if fetch_to "${base}/checksums.txt" "${tmp}/checksums.txt" 2>/dev/null; then
	want=$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "${tmp}/checksums.txt" | head -1)
	if [ -z "$want" ]; then
		warn "no checksum listed for $asset — skipping verification"
	else
		if command -v sha256sum >/dev/null 2>&1; then
			got=$(sha256sum "${tmp}/${asset}" | awk '{print $1}')
		elif command -v shasum >/dev/null 2>&1; then
			got=$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')
		else
			got=""
			warn "no sha256sum or shasum — skipping verification"
		fi
		if [ -n "$got" ]; then
			[ "$got" = "$want" ] || die "checksum mismatch: expected $want, got $got"
			ok "checksum verified"
		fi
	fi
else
	warn "no checksums.txt in $VERSION — skipping verification"
fi

# ── Install ──────────────────────────────────────────────────────────
chmod +x "${tmp}/${asset}"

# Sanity-check before replacing anything. Skipped when the binary cannot run
# here, which is the normal case for a cross-arch install.
if "${tmp}/${asset}" version >/dev/null 2>&1; then
	:
else
	warn "the downloaded binary does not run on this host — installing anyway"
fi

# mv within the same filesystem is atomic, so a concurrent doorman is never
# left reading a half-written file.
mv -f "${tmp}/${asset}" "$target" || die "could not install to $target"
ok "installed $target"

# ── PATH ─────────────────────────────────────────────────────────────
case ":${PATH}:" in
	*":${PREFIX}:"*) ;;
	*)
		warn "$PREFIX is not on your PATH. Add it:"
		printf '\n    echo '\''export PATH="%s:$PATH"'\'' >> ~/.profile\n\n' "$PREFIX" >&2
		;;
esac

if "$target" version >/dev/null 2>&1; then
	printf '\n'
	"$target" version
fi

cat >&2 <<EOF

Next: doorman needs Asterisk, a policy file, and prompts.

    doorman check          validate policy.toml + handsets.toml
    doorman help           every subcommand

Provisioning is in docs/RUNBOOK.md:
https://github.com/${REPO}/blob/main/docs/RUNBOOK.md
EOF
