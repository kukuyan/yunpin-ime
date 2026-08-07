#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"

build_root="${YUNPIN_MACOS_BUILD_ROOT:-${REPO_ROOT}/build/macos}"
source_dir="$build_root/squirrel"
output_dir="$build_root/package"
archive="$output_dir/YunPin-IME-development-preview-source.tar.gz"
[[ -f "$source_dir/.yunpin-base-commit" ]] || die "prepared Squirrel source is missing"

staging="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-source.XXXXXX")"
trap 'rm -rf "$staging"' EXIT
mkdir -p "$staging/YunPin-IME/Squirrel" "$staging/YunPin-IME/YunPin"

tar -C "$source_dir" \
  --exclude='.git' \
  --exclude='build' \
  --exclude='download' \
  --exclude='Frameworks' \
  --exclude='lib' \
  --exclude='bin' \
  --exclude='librime/dist' \
  --exclude='librime/build-yunpin' \
  --exclude='librime/dist-yunpin' \
  -cf - . | tar -C "$staging/YunPin-IME/Squirrel" -xf -

for path in \
  LICENSE NOTICE THIRD_PARTY_NOTICES.md \
  platform/LICENSE-BOUNDARIES.md platform/upstream-lock.json \
  platform/macos platform/rime platform/patches/squirrel \
  engine librime-yunpin; do
  parent="$staging/YunPin-IME/YunPin/$(dirname "$path")"
  mkdir -p "$parent"
  cp -R "${REPO_ROOT}/$path" "$parent/"
done

mkdir -p "$staging/YunPin-IME/YunPin/third_party/rime-ice"
git -C "${REPO_ROOT}/third_party/rime-ice" archive HEAD | \
  tar -C "$staging/YunPin-IME/YunPin/third_party/rime-ice" -xf -
git -C "${REPO_ROOT}/third_party/rime-ice" rev-parse HEAD > \
  "$staging/YunPin-IME/YunPin/third_party/rime-ice.commit"

mkdir -p "$output_dir"
tar -C "$staging" -czf "$archive" YunPin-IME
shasum -a 256 "$archive"
printf 'created corresponding-source archive: %s\n' "$archive"
