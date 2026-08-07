#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"

source_dir="${1:-${REPO_ROOT}/build/macos/squirrel}"
[[ -f "$source_dir/.yunpin-dependencies-ready" ]] || die "run fetch-dependencies.sh before staging the YunPin plugin"

expected_librime="$(read_lock_value nested_submodules.librime)"
git -C "$source_dir" submodule update --init librime
actual_librime="$(git -C "$source_dir/librime" rev-parse HEAD)"
[[ "$actual_librime" == "$expected_librime" ]] || die "nested librime $actual_librime does not match lock $expected_librime"

plugin_dir="$source_dir/librime/plugins/librime-yunpin"
if [[ -e "$plugin_dir" ]]; then
  [[ -f "$plugin_dir/.yunpin-staged" && "$(<"$plugin_dir/.yunpin-staged")" == "$expected_librime" ]] || die "refusing to overwrite an unknown librime-yunpin staging directory"
fi

staging="$(mktemp -d "$source_dir/librime/plugins/.yunpin-stage.XXXXXX")"
trap 'rm -rf "$staging"' EXIT
candidate="$staging/librime-yunpin"
mkdir -p "$candidate" "$candidate/engine"
cp -R "${REPO_ROOT}/librime-yunpin/CMakeLists.txt" \
  "${REPO_ROOT}/librime-yunpin/README.md" \
  "${REPO_ROOT}/librime-yunpin/include" \
  "${REPO_ROOT}/librime-yunpin/src" \
  "${REPO_ROOT}/librime-yunpin/tests" \
  "$candidate/"
cp -R "${REPO_ROOT}/engine/include" "${REPO_ROOT}/engine/src" "$candidate/engine/"
printf '%s\n' "$expected_librime" > "$candidate/.yunpin-staged"

if [[ -e "$plugin_dir" ]]; then
  mv "$plugin_dir" "$staging/previous"
fi
if ! mv "$candidate" "$plugin_dir"; then
  if [[ -e "$staging/previous" ]]; then
    mv "$staging/previous" "$plugin_dir"
  fi
  die "failed to atomically refresh librime-yunpin staging"
fi
rm -rf "$staging/previous"
rmdir "$staging"
trap - EXIT

[[ -f "$plugin_dir/src/yunpin_module.cpp" && -f "$plugin_dir/engine/src/phrase_engine.cpp" ]] || die "YunPin plugin staging is incomplete"
printf 'staged librime-yunpin and engine into %s\n' "$plugin_dir"
