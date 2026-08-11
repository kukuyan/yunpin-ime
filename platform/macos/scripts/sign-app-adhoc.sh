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
  codesign --force --sign - --timestamp=none "$target"
}

sparkle="$app/Contents/Frameworks/Sparkle.framework"
if [[ -d "$sparkle" ]]; then
  sparkle_version="$(cd "$sparkle/Versions/Current" && pwd -P)"
  xpc_root="$sparkle_version/XPCServices"

  # Sign from the innermost executable outwards. Signing a containing bundle
  # first would invalidate its resource seal when a child is re-signed.
  while IFS= read -r executable; do
    sign_adhoc "$executable"
  done < <(find "$xpc_root" -type f -perm -111 -path '*/Contents/MacOS/*' | sort)

  while IFS= read -r bundle; do
    sign_adhoc "$bundle"
  done < <(find "$xpc_root" -type d -name '*.xpc' | sort)

  updater="$sparkle_version/Updater.app"
  [[ -d "$updater" ]] && sign_adhoc "$updater"
  [[ -f "$sparkle_version/Autoupdate" ]] && sign_adhoc "$sparkle_version/Autoupdate"
  sign_adhoc "$sparkle"
fi

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

sign_adhoc "$app"
codesign --verify --deep --strict --verbose=2 "$app"
printf 'ad-hoc signed YunPin.app bottom-up: %s\n' "$app"
