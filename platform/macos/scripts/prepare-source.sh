#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"

output="${1:-${REPO_ROOT}/build/macos/squirrel}"
source_checkout="${REPO_ROOT}/third_party/squirrel"
patch_dir="${REPO_ROOT}/platform/patches/squirrel"

[[ ! -e "$output" ]] || die "refusing to overwrite existing source directory: $output"
[[ -e "$source_checkout/.git" ]] || die "third_party/squirrel is not initialized"
actual_commit="$(git -C "$source_checkout" rev-parse HEAD)"
[[ "$actual_commit" == "$SQUIRREL_COMMIT" ]] || die "Squirrel checkout $actual_commit does not match lock $SQUIRREL_COMMIT"
[[ "$(read_lock_value squirrel_commit)" == "$SQUIRREL_COMMIT" ]] || die "macOS dependency lock disagrees with the platform lock"

mkdir -p "$(dirname "$output")"
git clone --quiet --no-hardlinks "$source_checkout" "$output"
git -C "$output" checkout --quiet --detach "$SQUIRREL_COMMIT"
for patch_file in "$patch_dir"/*.patch; do
  git -C "$output" apply --check --whitespace=error-all "$patch_file"
  git -C "$output" apply --whitespace=error-all "$patch_file"
done

mv "$output/Rime.icon" "$output/YunPin.icon"
cp "${MACOS_DIR}/assets/yunpin-mark.svg" "$output/YunPin.icon/Assets/logo.svg"
cp "${REPO_ROOT}/platform/rime/squirrel/default.custom.yaml" "$output/data/default.custom.yaml"
cp "${REPO_ROOT}/platform/rime/squirrel/squirrel.custom.yaml" "$output/data/squirrel.custom.yaml"
cp "${REPO_ROOT}/platform/rime/squirrel/rime_ice.custom.yaml" "$output/data/rime_ice.custom.yaml"
cp "${MACOS_DIR}/preview-manifest.json" "$output/data/yunpin-preview.json"

printf '%s\n' "$SQUIRREL_COMMIT" > "$output/.yunpin-base-commit"
printf 'prepared YunPin Squirrel source at %s\n' "$output"
