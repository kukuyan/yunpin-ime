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

librime_patch_dir="${REPO_ROOT}/platform/patches/librime-1.16"
librime_patch_marker="$source_dir/librime/.yunpin-librime-patchset"
librime_patch_digest="$(/usr/bin/python3 - "${MACOS_DIR}/dependencies.lock.json" "$REPO_ROOT" "$librime_patch_dir" <<'PY'
import hashlib
import json
import sys
from pathlib import Path

lock_path, repo_root, patch_dir = map(Path, sys.argv[1:])
rows = json.loads(lock_path.read_text(encoding="utf-8")).get("librime_patches")
if not rows:
    raise SystemExit("dependency lock does not record the librime patch series")
paths = [repo_root / row["path"] for row in rows]
if paths != sorted(patch_dir.glob("*.patch")):
    raise SystemExit("locked librime patch series does not match the files on disk")
for row, path in zip(rows, paths):
    if hashlib.sha256(path.read_bytes()).hexdigest() != row["sha256"]:
        raise SystemExit(f"digest mismatch for {row['path']}")
print(":".join(row["sha256"] for row in rows))
PY
)" || die "librime patch series failed verification against the macOS dependency lock"

patch_files=("$librime_patch_dir"/*.patch)
if [[ -f "$librime_patch_marker" ]]; then
  [[ "$(<"$librime_patch_marker")" == "$librime_patch_digest" ]] || die "staged librime patch marker does not match the dependency lock"
  # Reverse the complete locked series, require the whole tracked checkout to
  # return to its clean base, then reapply. This rejects unrelated changes even
  # when they happen outside the two files touched by today's patch.
  for ((index=${#patch_files[@]} - 1; index >= 0; --index)); do
    patch_file="${patch_files[$index]}"
    git -C "$source_dir/librime" apply --reverse --check --whitespace=error-all "$patch_file" || die "librime patch marker exists but source does not match"
    git -C "$source_dir/librime" apply --reverse --whitespace=error-all "$patch_file"
  done
  tracked_base_clean=true
  git -C "$source_dir/librime" diff --quiet || tracked_base_clean=false
  for patch_file in "${patch_files[@]}"; do
    git -C "$source_dir/librime" apply --check --whitespace=error-all "$patch_file"
    git -C "$source_dir/librime" apply --whitespace=error-all "$patch_file"
  done
  [[ "$tracked_base_clean" == true ]] || die "staged librime contains tracked changes outside the locked patch series"
else
  git -C "$source_dir/librime" diff --quiet || \
    die "refusing to patch a modified librime checkout"
  for patch_file in "${patch_files[@]}"; do
    git -C "$source_dir/librime" apply --check --whitespace=error-all "$patch_file"
    git -C "$source_dir/librime" apply --whitespace=error-all "$patch_file"
  done
fi
printf '%s\n' "$librime_patch_digest" > "$librime_patch_marker"
grep -F 'corrector_component' "$source_dir/librime/src/rime/gear/script_translator.cc" >/dev/null || die "patched librime does not expose the corrector component selector"

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
