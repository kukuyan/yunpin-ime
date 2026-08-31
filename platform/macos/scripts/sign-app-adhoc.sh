#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos

app="${1:-${REPO_ROOT}/build/macos/DerivedData/Build/Products/Release/YunPin.app}"
[[ -d "$app" ]] || die "missing YunPin app bundle: $app"

# A copied or incrementally built bundle can retain Finder/resource-fork
# metadata that codesign refuses to seal. Clearing it is safe for a generated
# build artifact and must finish before any nested object is signed.
xattr -cr "$app"

sign_adhoc() {
  local target="$1"
  local identifier="${2:-}"
  if [[ -n "$identifier" ]]; then
    codesign --force --sign - --timestamp=none --identifier "$identifier" "$target"
  else
    codesign --force --sign - --timestamp=none "$target"
  fi
}

frameworks="$app/Contents/Frameworks"
while IFS= read -r dylib; do
  sign_adhoc "$dylib"
done < <(find "$frameworks" -maxdepth 1 -type f -name 'librime*.dylib' | sort)

plugins="$frameworks/rime-plugins"
if [[ -d "$plugins" ]]; then
  while IFS= read -r plugin; do
    sign_adhoc "$plugin"
  done < <(find "$plugins" -type f -name '*.dylib' | sort)
fi

while IFS= read -r helper; do
  sign_adhoc "$helper"
done < <(find "$app/Contents/MacOS" -maxdepth 1 -type f -perm -111 -name 'rime*' | sort)

sync_agent="$app/Contents/MacOS/yunpin-sync-agent"
replay_lab="$app/Contents/MacOS/yunpin-replay-lab"
[[ -x "$sync_agent" ]] || die "bundled public sync agent is missing"
[[ -x "$replay_lab" ]] || die "bundled Replay Lab CLI is missing"
sign_adhoc "$sync_agent" "$YUNPIN_SYNC_AGENT_ID"
sign_adhoc "$replay_lab" "$YUNPIN_REPLAY_LAB_ID"

sign_adhoc "$app"
codesign --verify --deep --strict --verbose=2 "$app"
printf 'ad-hoc signed YunPin.app bottom-up: %s\n' "$app"
