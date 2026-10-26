#!/usr/bin/env bash
# ci-audio-tools.sh DIR — put ffmpeg and espeak-ng on a GitHub runner's PATH
# without letting a stalled Ubuntu mirror hang the job.
#
# The Books audio integration tests need both, and without them they skip and
# the coverage floor fails. A plain `apt-get update && install` hung for hours
# when Ubuntu's mirrors stalled (2026-10-22). So:
#
#   ffmpeg     a pinned, checksum-verified static build, kept in DIR (which the
#              workflow caches); apt only if that is unavailable.
#   espeak-ng  .deb files kept in DIR from the last successful download and
#              installed with APT in no-download mode, which needs no mirror;
#              otherwise a bounded, retried apt download that refills DIR.
#
# Every network step has a timeout and a retry limit, and the workflow step
# has timeout-minutes on top. BtbN prunes old autobuild releases: when the
# cache and the pinned asset are both gone, bump the pin (and its checksum).
set -euo pipefail

dir=${1:?usage: ci-audio-tools.sh DIR}
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)

FFMPEG_RELEASE=autobuild-2026-10-22-13-06
FFMPEG_ASSET=ffmpeg-n8.1.3-23-g5c4f5744cd-linux64-gpl-8.1.tar.xz
FFMPEG_SHA256=1aa0c5526768bf4db3e62cba954169e3e0be38327c9cbbb6c5675794df6c03df

apt=(sudo apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30)
updated=""

# apt_bounded CMD... — run an apt command with a time limit, three times at most,
# refreshing the package lists once first.
apt_bounded() {
	for attempt in 1 2 3; do
		if [ -z "$updated" ]; then
			if timeout 90 "${apt[@]}" update -qq; then
				updated=1
			else
				echo "apt update attempt $attempt failed"
				sleep 10
				continue
			fi
		fi
		timeout 120 "$@" && return 0
		echo "apt attempt $attempt failed; retrying"
		sleep 10
	done
	return 1
}

add_path() {
	export PATH="$1:$PATH"
	if [ -n "${GITHUB_PATH:-}" ]; then
		echo "$1" >>"$GITHUB_PATH"
	fi
}

install_ffmpeg() {
	if command -v ffmpeg >/dev/null && command -v ffprobe >/dev/null; then
		echo "ffmpeg preinstalled: $(ffmpeg -version | head -1)"
		return
	fi
	local bin=$dir/ffmpeg
	if [ ! -x "$bin/ffmpeg" ]; then
		rm -rf "$bin" && mkdir -p "$bin"
		local tarball
		tarball=$(mktemp)
		if curl -fsSL --retry 3 --connect-timeout 20 --max-time 180 -o "$tarball" \
			"https://github.com/BtbN/FFmpeg-Builds/releases/download/$FFMPEG_RELEASE/$FFMPEG_ASSET" &&
			echo "$FFMPEG_SHA256  $tarball" | sha256sum -c - &&
			tar -xJf "$tarball" -C "$bin" --strip-components=2 --wildcards '*/bin/ffmpeg' '*/bin/ffprobe'; then
			echo "ffmpeg from the pinned static build"
		else
			rm -rf "$bin"
		fi
		rm -f "$tarball"
	fi
	if [ -x "$dir/ffmpeg/ffmpeg" ]; then
		add_path "$dir/ffmpeg"
		return
	fi
	echo "static ffmpeg unavailable; falling back to apt"
	apt_bounded "${apt[@]}" install -y -qq --no-install-recommends ffmpeg
}

# Let APT solve the whole local package set before changing the runner. Direct
# dpkg -i can partially install packages from another image and leave apt broken.
install_cached_espeak() {
	local debs=${1:?cached package directory required}
	compgen -G "$debs/*.deb" >/dev/null || return 1
	timeout 120 "${apt[@]}" install -y -qq --no-install-recommends \
		--no-download --no-remove "$debs"/*.deb >/dev/null
}

install_espeak() {
	if command -v espeak-ng >/dev/null; then
		echo "espeak-ng preinstalled"
		return
	fi
	local debs=$dir/espeak-debs
	if install_cached_espeak "$debs"; then
		echo "espeak-ng from cached packages"
		return
	fi
	# Download into the cache first, so the next run needs no mirror, then
	# install from what was downloaded.
	rm -rf "$debs" && mkdir -p "$debs/partial"
	if apt_bounded "${apt[@]}" install -y --no-install-recommends --download-only \
		-o Dir::Cache::archives="$debs" espeak-ng &&
		install_cached_espeak "$debs"; then
		sudo chown -R "$(id -u):$(id -g)" "$debs"
		rm -rf "$debs/partial" "$debs/lock"
		echo "espeak-ng from apt (packages cached)"
		return
	fi
	rm -rf "$debs"
	echo "cached install failed; plain bounded apt install"
	apt_bounded "${apt[@]}" install -y -qq --no-install-recommends --fix-broken espeak-ng
}

install_ffmpeg
install_espeak

ffmpeg -version | head -1
ffprobe -version | head -1
espeak-ng --version
