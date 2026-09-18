#!/usr/bin/env bash
set -euo pipefail
profile=centos
# shellcheck source=install-scripts/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
prepare "$@"
