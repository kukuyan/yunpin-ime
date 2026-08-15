#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos
require_full_xcode
resolve_cmake
build_jobs="$(resolve_macos_build_jobs)"

source_dir="${1:-${REPO_ROOT}/build/macos/squirrel}"
[[ -d "$source_dir" ]] || die "prepared Squirrel source is missing: $source_dir"
source_dir="$(cd "$source_dir" && pwd)"
"${MACOS_DIR}/scripts/stage-yunpin-plugin.sh" "$source_dir"

librime_dir="$source_dir/librime"
build_dir="$librime_dir/build-yunpin"
install_dir="$librime_dir/dist-yunpin"
plugin_build_dir="$librime_dir/build-yunpin-runtime-plugins"
plugin_install_dir="$librime_dir/dist-yunpin-runtime-plugins"
boost_version="$(read_lock_value boost_version)"
boost_root="$librime_dir/deps/boost-$boost_version"
[[ -f "$boost_root/boost/version.hpp" ]] || die "locked Boost headers are missing; run fetch-dependencies.sh"
for plugin in lua octagram predict; do
  [[ -f "$librime_dir/plugins/$plugin/CMakeLists.txt" && -f "$librime_dir/plugins/$plugin/.yunpin-source-commit" ]] ||
    die "locked Rime plugin source is missing: $plugin"
done
[[ -f "$librime_dir/plugins/lua/thirdparty/lua5.4/lua.h" &&
   -f "$librime_dir/plugins/lua/thirdparty/.yunpin-source-commit" ]] ||
  die "locked in-tree Lua source is missing"

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
  cmake --build "$build_dir" --parallel "$build_jobs"
  cmake --install "$build_dir"
)

library="$install_dir/lib/librime.1.dylib"
[[ -f "$library" ]] || die "merged YunPin librime was not installed"
architectures="$(lipo -archs "$library")"
[[ " $architectures " == *" arm64 "* && " $architectures " == *" x86_64 "* ]] || die "merged librime is not universal: $architectures"
nm -gU "$library" | grep -F 'rime_require_module_yunpin' >/dev/null || die "merged librime does not export the YunPin module"
nm -gU "$library" | grep -F 'YunPinStartNativeSelectionSpoolerV1' >/dev/null || die "merged librime does not export the YunPin native spooler"

# Build the release's three external C++ plugins again from their locked source
# revisions.  They deliberately remain external because Squirrel's plugin
# loader and resource packaging expect these dylibs, but every C++ object now
# uses the same compiler, SDK, deployment target, Boost headers, and librime
# source as the merged YunPin core above.
(
  cd "$librime_dir"
  RIME_PLUGINS='lua octagram predict' cmake --fresh -S . -B "$plugin_build_dir" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="$plugin_install_dir" \
    -DCMAKE_OSX_ARCHITECTURES='arm64;x86_64' \
    -DCMAKE_OSX_DEPLOYMENT_TARGET=13.0 \
    -DCMAKE_OSX_SYSROOT="$(xcrun --sdk macosx --show-sdk-path)" \
    -DBoost_NO_BOOST_CMAKE=TRUE \
    -DBOOST_ROOT="$boost_root" \
    -DBUILD_MERGED_PLUGINS=OFF \
    -DENABLE_EXTERNAL_PLUGINS=ON \
    -DBUILD_TOOLS=OFF \
    -DBUILD_TEST=OFF \
    -DENABLE_TIMESTAMP=OFF
  cmake --build "$plugin_build_dir" --parallel "$build_jobs"
  cmake --install "$plugin_build_dir"
)

runtime_plugin_dir="$plugin_install_dir/lib/rime-plugins"
expected_runtime_plugins='librime-lua.dylib librime-octagram.dylib librime-predict.dylib '
actual_runtime_plugins="$(find "$runtime_plugin_dir" -maxdepth 1 -type f -name '*.dylib' -exec basename {} \; | LC_ALL=C sort | tr '\n' ' ')"
[[ "$actual_runtime_plugins" == "$expected_runtime_plugins" ]] ||
  die "rebuilt Rime plugin set is incomplete or unexpected: $actual_runtime_plugins"

packaged_plugin_dir="$source_dir/lib/rime-plugins"
existing_packaged_plugins="$(find "$packaged_plugin_dir" -maxdepth 1 -type f -name '*.dylib' -exec basename {} \; | LC_ALL=C sort | tr '\n' ' ')"
[[ "$existing_packaged_plugins" == "$expected_runtime_plugins" ]] ||
  die "prebuilt Rime plugin set is incomplete or unexpected: $existing_packaged_plugins"

for plugin in librime-lua.dylib librime-octagram.dylib librime-predict.dylib; do
  rebuilt="$runtime_plugin_dir/$plugin"
  plugin_architectures="$(lipo -archs "$rebuilt")"
  [[ " $plugin_architectures " == *" arm64 "* && " $plugin_architectures " == *" x86_64 "* ]] ||
    die "rebuilt Rime plugin is not universal: $plugin ($plugin_architectures)"
  otool -L "$rebuilt" | grep -F '@rpath/librime.1.dylib' >/dev/null ||
    die "rebuilt Rime plugin does not bind to the packaged librime ABI: $plugin"
  plugin_minos="$(xcrun vtool -show-build "$rebuilt" | awk '$1 == "minos" { print $2 }' | LC_ALL=C sort -u)"
  [[ "$plugin_minos" == '13.0' ]] ||
    die "rebuilt Rime plugin has an unexpected deployment target: $plugin ($plugin_minos)"
  install -m 755 "$rebuilt" "$packaged_plugin_dir/$plugin"
done

cp -L "$library" "$source_dir/lib/librime.1.dylib"
cp "$install_dir/bin/rime_deployer" "$source_dir/bin/rime_deployer"
cp "$install_dir/bin/rime_dict_manager" "$source_dir/bin/rime_dict_manager"
for executable in "$source_dir/bin/rime_deployer" "$source_dir/bin/rime_dict_manager"; do
  if ! otool -l "$executable" | grep -A2 LC_RPATH | grep -Fq '@loader_path/../Frameworks'; then
    install_name_tool -add_rpath @loader_path/../Frameworks "$executable"
  fi
done

"${MACOS_DIR}/scripts/test-merged-ranking.sh" "$source_dir"

printf 'built merged Universal librime-yunpin with same-toolchain runtime plugins: %s (%s)\n' "$library" "$architectures"
