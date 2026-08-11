#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos
require_full_xcode

build_root="${YUNPIN_MACOS_BUILD_ROOT:-${REPO_ROOT}/build/macos}"
source_dir="$build_root/squirrel"
derived_data="$build_root/DerivedData"

if [[ ! -d "$source_dir" ]]; then
  "${MACOS_DIR}/scripts/prepare-source.sh" "$source_dir"
fi
if [[ ! -f "$source_dir/.yunpin-dependencies-ready" ]]; then
  "${MACOS_DIR}/scripts/fetch-dependencies.sh" "$source_dir"
fi
"${MACOS_DIR}/scripts/build-librime-yunpin.sh" "$source_dir"

mkdir -p "$source_dir/data/plum"
cp "${REPO_ROOT}/platform/rime/squirrel/default.custom.yaml" "$source_dir/data/default.custom.yaml"
cp "${REPO_ROOT}/platform/rime/squirrel/squirrel.custom.yaml" "$source_dir/data/squirrel.custom.yaml"
cp "${REPO_ROOT}/platform/rime/squirrel/rime_ice.custom.yaml" "$source_dir/data/rime_ice.custom.yaml"
cp "${MACOS_DIR}/preview-manifest.json" "$source_dir/data/yunpin-preview.json"
cp "$source_dir/data/default.custom.yaml" "$source_dir/data/plum/default.custom.yaml"
cp "$source_dir/data/squirrel.custom.yaml" "$source_dir/data/plum/squirrel.custom.yaml"
cp "$source_dir/data/rime_ice.custom.yaml" "$source_dir/data/plum/rime_ice.custom.yaml"
cp "$source_dir/data/yunpin-preview.json" "$source_dir/data/plum/yunpin-preview.json"
xcrun swift "${MACOS_DIR}/scripts/render-input-icon.swift" "$source_dir/resources/yunpin.pdf"

(
  cd "$source_dir"
  bash package/add_data_files
  xcodebuild \
    -project Squirrel.xcodeproj \
    -scheme Squirrel \
    -configuration Release \
    -derivedDataPath "$derived_data" \
    ARCHS="arm64 x86_64" \
    ONLY_ACTIVE_ARCH=NO \
    MACOSX_DEPLOYMENT_TARGET=13.0 \
    CODE_SIGNING_ALLOWED=NO \
    CODE_SIGNING_REQUIRED=NO \
    PRODUCT_NAME="$YUNPIN_PRODUCT" \
    PRODUCT_MODULE_NAME="$YUNPIN_PRODUCT" \
    PRODUCT_BUNDLE_IDENTIFIER="$YUNPIN_BUNDLE_ID" \
    INFOPLIST_KEY_CFBundleDisplayName="YunPin Input Method" \
    build
)

app="$derived_data/Build/Products/Release/YunPin.app"
[[ -d "$app" ]] || die "xcodebuild did not produce $app"
shared_support="$app/Contents/SharedSupport"
mkdir -p "$shared_support" "$shared_support/licenses"

find "${REPO_ROOT}/third_party/rime-ice" -maxdepth 1 -type f \
  \( -name '*.yaml' -o -name '*.txt' \) \
  ! -name 'squirrel.yaml' ! -name 'weasel.yaml' \
  -exec cp {} "$shared_support" \;
for data_dir in cn_dicts en_dicts lua opencc; do
  ditto "${REPO_ROOT}/third_party/rime-ice/$data_dir" "$shared_support/$data_dir"
done
cp "${REPO_ROOT}/third_party/rime-ice/LICENSE" "$shared_support/licenses/Rime-Ice-LICENSE"
cp "${REPO_ROOT}/third_party/squirrel/LICENSE.txt" "$shared_support/licenses/Squirrel-LICENSE"
cp "${REPO_ROOT}/LICENSE" "$shared_support/licenses/YunPin-Apache-LICENSE"
cp "$source_dir/librime/deps/boost-$(read_lock_value boost_version)/LICENSE_1_0.txt" \
  "$shared_support/licenses/Boost-1.89.0-LICENSE"
for package in bopomofo cangjie essay luna-pinyin prelude stroke terra-pinyin; do
  cp "$source_dir/plum/package/rime/$package/LICENSE" "$shared_support/licenses/Rime-$package-LGPL-3.0-LICENSE"
done
cp "${MACOS_DIR}/preview-manifest.json" "$shared_support/yunpin-preview.json"

"${MACOS_DIR}/scripts/sign-app-adhoc.sh" "$app"
"${MACOS_DIR}/scripts/verify-app.sh" --require-universal "$app"
lsregister="${YUNPIN_LSREGISTER:-/System/Library/Frameworks/CoreServices.framework/Versions/Current/Frameworks/LaunchServices.framework/Versions/Current/Support/lsregister}"
[[ -x "$lsregister" ]] || die "LaunchServices registration tool is unavailable"
"$lsregister" -u "$app" >/dev/null || die "failed to unregister the exact YunPin build bundle"
YUNPIN_LSREGISTER="$lsregister" \
  "${MACOS_DIR}/scripts/verify-launchservices-state.sh" "$app"
printf 'built YunPin development preview: %s\n' "$app"
