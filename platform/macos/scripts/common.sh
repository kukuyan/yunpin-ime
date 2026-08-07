#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

MACOS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "${MACOS_DIR}/../.." && pwd)"
SQUIRREL_COMMIT="876adebaf2f612951dcdca8a591de65401222b9a"
YUNPIN_BUNDLE_ID="io.github.kukuyan.inputmethod.YunPin"
YUNPIN_PRODUCT="YunPin"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_macos() {
  [[ "$(uname -s)" == "Darwin" ]] || die "the native macOS build requires Darwin"
}

require_full_xcode() {
  command -v xcodebuild >/dev/null 2>&1 || die "xcodebuild is unavailable; install full Xcode"
  xcodebuild -version >/dev/null 2>&1 || die "full Xcode is required; Command Line Tools alone are insufficient"
  xcode_major="$(xcodebuild -version | awk 'NR == 1 {split($2, version, "."); print version[1]}')"
  [[ "$xcode_major" =~ ^[0-9]+$ && "$xcode_major" -ge 26 ]] || die "Xcode 26 or later is required by the pinned Squirrel icon project"
}

read_lock_value() {
  /usr/bin/python3 - "$MACOS_DIR/dependencies.lock.json" "$1" <<'PY'
import json
import sys

document = json.load(open(sys.argv[1], encoding="utf-8"))
value = document
for component in sys.argv[2].split("."):
    value = value[component]
print(value)
PY
}
