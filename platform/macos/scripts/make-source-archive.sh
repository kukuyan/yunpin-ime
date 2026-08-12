#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_clean_repository
export COPYFILE_DISABLE=1

build_root="${YUNPIN_MACOS_BUILD_ROOT:-${REPO_ROOT}/build/macos}"
source_dir="$build_root/squirrel"
output_dir="$build_root/package"
archive="$output_dir/YunPin-IME-development-preview-source.tar.gz"
[[ -f "$source_dir/.yunpin-base-commit" ]] || die "prepared Squirrel source is missing"

mkdir -p "$build_root"
staging="$(mktemp -d "$build_root/.source-archive.XXXXXX")"
trap 'rm -rf "$staging"' EXIT
mkdir -p "$staging/YunPin-IME/Squirrel" "$staging/YunPin-IME/YunPin"

tar -C "$source_dir" \
  --exclude='._*' \
  --exclude='.DS_Store' \
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

# Export only committed files.  A recursive worktree copy also picks up ignored
# compiler output (for example engine/build after `make test`), which is neither
# corresponding source nor appropriate release material.
git -C "$REPO_ROOT" archive HEAD -- \
  LICENSE NOTICE THIRD_PARTY_NOTICES.md \
  third_party/go-modules.lock.json scripts/package_go_licenses.py \
  platform/LICENSE-BOUNDARIES.md platform/upstream-lock.json \
  platform/macos platform/rime platform/patches/squirrel \
  platform/patches/librime-1.16 \
  desktopagent localstore protocol syncclient \
  engine librime-yunpin | \
  tar -C "$staging/YunPin-IME/YunPin" -xf -

mkdir -p "$staging/YunPin-IME/YunPin/third_party/rime-ice"
git -C "${REPO_ROOT}/third_party/rime-ice" archive HEAD | \
  tar -C "$staging/YunPin-IME/YunPin/third_party/rime-ice" -xf -
git -C "${REPO_ROOT}/third_party/rime-ice" rev-parse HEAD > \
  "$staging/YunPin-IME/YunPin/third_party/rime-ice.commit"

if find "$staging" \( -name '._*' -o -name '.DS_Store' \) -print -quit | grep -q .; then
  die "corresponding-source staging contains forbidden macOS metadata files"
fi

mkdir -p "$output_dir"
tar -C "$staging" --exclude='._*' --exclude='.DS_Store' \
  -czf "$archive" YunPin-IME
shasum -a 256 "$archive"
printf 'created corresponding-source archive: %s\n' "$archive"
