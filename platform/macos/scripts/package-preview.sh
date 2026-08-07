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
package_root="$(mktemp -d "$build_root/.package-root.XXXXXX")"
component_plist="$(mktemp "$build_root/.package-components.XXXXXX")"
cleanup_package_staging() {
  rm -rf "$package_root"
  rm -f "$component_plist"
}
trap cleanup_package_staging EXIT
mkdir -p "$package_root/Library/Input Methods"
ditto "$app" "$package_root/Library/Input Methods/YunPin.app"
if ! codesign --verify --strict "$package_root/Library/Input Methods/YunPin.app" >/dev/null 2>&1; then
  printf 'warning: skipping strict signature verification for unsigned preview artifact\n'
fi
pkgbuild --analyze --root "$package_root" "$component_plist"
/usr/libexec/PlistBuddy -c 'Set :0:BundleIsRelocatable false' "$component_plist"
[[ "$(plutil -extract 0.BundleIsRelocatable raw -o - "$component_plist")" == false ]] || \
  die "YunPin installer component must not be relocatable"
pkgbuild \
  --root "$package_root" \
  --component-plist "$component_plist" \
  --identifier "$YUNPIN_BUNDLE_ID" \
  --version "0.1.0" \
  --scripts "${MACOS_DIR}/package" \
  "$package"
pkgutil --check-signature "$package" >/dev/null 2>&1 || true
shasum -a 256 "$package"
"${MACOS_DIR}/scripts/make-source-archive.sh"
printf 'packaged unsigned development preview: %s\n' "$package"
