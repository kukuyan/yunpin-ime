#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos
command -v cmake >/dev/null 2>&1 || die "cmake is required to build merged librime-yunpin"

source_dir="${1:-${REPO_ROOT}/build/macos/squirrel}"
[[ -d "$source_dir" ]] || die "prepared Squirrel source is missing: $source_dir"
source_dir="$(cd "$source_dir" && pwd)"
"${MACOS_DIR}/scripts/stage-yunpin-plugin.sh" "$source_dir"

librime_dir="$source_dir/librime"
build_dir="$librime_dir/build-yunpin"
install_dir="$librime_dir/dist-yunpin"
boost_version="$(read_lock_value boost_version)"
boost_root="$librime_dir/deps/boost-$boost_version"
[[ -f "$boost_root/boost/version.hpp" ]] || die "locked Boost headers are missing; run fetch-dependencies.sh"

ditto "$source_dir/download/include" "$librime_dir/include"
ditto "$source_dir/download/lib" "$librime_dir/lib"

(
  cd "$librime_dir"
  RIME_PLUGINS=librime-yunpin cmake --fresh -S . -B "$build_dir" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="$install_dir" \
    -DCMAKE_OSX_ARCHITECTURES='arm64;x86_64' \
    -DCMAKE_OSX_DEPLOYMENT_TARGET=13.0 \
    -DCMAKE_OSX_SYSROOT="$(xcrun --sdk macosx --show-sdk-path)" \
    -DBoost_NO_BOOST_CMAKE=TRUE \
    -DBOOST_ROOT="$boost_root" \
    -DBUILD_MERGED_PLUGINS=ON \
    -DENABLE_EXTERNAL_PLUGINS=ON \
    -DBUILD_TEST=OFF \
    -DENABLE_TIMESTAMP=OFF
  cmake --build "$build_dir" --parallel
  cmake --install "$build_dir"
)

library="$install_dir/lib/librime.1.dylib"
[[ -f "$library" ]] || die "merged YunPin librime was not installed"
architectures="$(lipo -archs "$library")"
[[ " $architectures " == *" arm64 "* && " $architectures " == *" x86_64 "* ]] || die "merged librime is not universal: $architectures"
nm -gU "$library" | grep -F 'rime_require_module_yunpin' >/dev/null || die "merged librime does not export the YunPin module"

cp -L "$library" "$source_dir/lib/librime.1.dylib"
cp "$install_dir/bin/rime_deployer" "$source_dir/bin/rime_deployer"
cp "$install_dir/bin/rime_dict_manager" "$source_dir/bin/rime_dict_manager"
for executable in "$source_dir/bin/rime_deployer" "$source_dir/bin/rime_dict_manager"; do
  if ! otool -l "$executable" | grep -A2 LC_RPATH | grep -Fq '@loader_path/../Frameworks'; then
    install_name_tool -add_rpath @loader_path/../Frameworks "$executable"
  fi
done

"${MACOS_DIR}/scripts/test-merged-ranking.sh" "$source_dir"

printf 'built merged Universal librime-yunpin: %s (%s)\n' "$library" "$architectures"
