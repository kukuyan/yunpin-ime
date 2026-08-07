#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos
require_full_xcode

build_root="${YUNPIN_MACOS_BUILD_ROOT:-${REPO_ROOT}/build/macos}"
app="$build_root/DerivedData/Build/Products/Release/YunPin.app"
output_dir="$build_root/package"
package="$output_dir/YunPin-IME-development-preview.pkg"

"${MACOS_DIR}/scripts/verify-app.sh" --require-universal "$app"
mkdir -p "$output_dir"
pkgbuild \
  --component "$app" \
  --install-location "/Library/Input Methods" \
  --identifier "$YUNPIN_BUNDLE_ID" \
  --version "0.1.0" \
  --scripts "${MACOS_DIR}/package" \
  "$package"
pkgutil --check-signature "$package" >/dev/null 2>&1 || true
shasum -a 256 "$package"
"${MACOS_DIR}/scripts/make-source-archive.sh"
printf 'packaged unsigned development preview: %s\n' "$package"
