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
grammar_model="$(resolve_locked_grammar_resource model)"
grammar_model_license="$(resolve_locked_grammar_resource license)"
grammar_model_filename="$(read_lock_value grammarModel.filename)"
grammar_model_license_filename="$(read_lock_value grammarModel.licenseFilename)"

mkdir -p "$build_root"
staging="$(mktemp -d "$build_root/.source-archive.XXXXXX")"
trap 'rm -rf "$staging"' EXIT
mkdir -p "$staging/YunPin-IME/Squirrel" "$staging/YunPin-IME/YunPin"

tar -C "$source_dir" \
  --exclude='._*' \
  --exclude='.DS_Store' \
  --exclude='*.gram' \
  --exclude='.git' \
  --exclude='build' \
  --exclude='download' \
  --exclude='Frameworks' \
  --exclude='lib' \
  --exclude='bin' \
  --exclude='librime/dist' \
  --exclude='librime/build-yunpin' \
  --exclude='librime/dist-yunpin' \
  --exclude='librime/build-yunpin-runtime-plugins' \
  --exclude='librime/dist-yunpin-runtime-plugins' \
  -cf - . | tar -C "$staging/YunPin-IME/Squirrel" -xf -

# Export only committed files.  A recursive worktree copy also picks up ignored
# compiler output (for example engine/build after `make test`), which is neither
# corresponding source nor appropriate release material.
git -C "$REPO_ROOT" archive HEAD -- \
  LICENSE NOTICE THIRD_PARTY_NOTICES.md \
  third_party/go-modules.lock.json scripts/package_go_licenses.py \
  scripts/verify_grammar_asset_metadata.py \
  platform/LICENSE-BOUNDARIES.md platform/upstream-lock.json \
  platform/macos platform/rime platform/patches/squirrel \
  platform/patches/librime-1.16 \
  desktopagent localstore protocol replaylab syncclient \
  engine librime-yunpin | \
  tar -C "$staging/YunPin-IME/YunPin" -xf -

mkdir -p "$staging/YunPin-IME/YunPin/third_party/rime-ice"
git -C "${REPO_ROOT}/third_party/rime-ice" archive HEAD | \
  tar -C "$staging/YunPin-IME/YunPin/third_party/rime-ice" -xf -
git -C "${REPO_ROOT}/third_party/rime-ice" rev-parse HEAD > \
  "$staging/YunPin-IME/YunPin/third_party/rime-ice.commit"

# The model is a release asset rather than Git source. Retain one verified copy
# beside the offline build inputs, never an unbounded glob or a partial file.
mkdir -p "$staging/YunPin-IME/YunPin/sources"
/usr/bin/install -m 644 "$grammar_model" \
  "$staging/YunPin-IME/YunPin/sources/$grammar_model_filename"
/usr/bin/install -m 644 "$grammar_model_license" \
  "$staging/YunPin-IME/YunPin/sources/$grammar_model_license_filename"
verify_locked_grammar_resource \
  "$staging/YunPin-IME/YunPin/sources/$grammar_model_filename" \
  "$(read_lock_value grammarModel.size)" \
  "$(read_lock_value grammarModel.sha256)" \
  "source-bundled grammar model"
verify_locked_grammar_resource \
  "$staging/YunPin-IME/YunPin/sources/$grammar_model_license_filename" \
  "$(read_lock_value grammarModel.licenseSize)" \
  "$(read_lock_value grammarModel.licenseSha256)" \
  "source-bundled grammar model license"
source_grammar_models="$(find "$staging" \( -type f -o -type l \) \
  -name '*.gram' -print)"
[[ "$source_grammar_models" == \
  "$staging/YunPin-IME/YunPin/sources/$grammar_model_filename" ]] ||
  die "corresponding source must contain exactly one locked grammar model"

if find "$staging" \( -name '._*' -o -name '.DS_Store' \) -print -quit | grep -q .; then
  die "corresponding-source staging contains forbidden macOS metadata files"
fi

/usr/bin/python3 - "$staging/YunPin-IME" <<'PY'
import hashlib
from pathlib import Path
import sys

root = Path(sys.argv[1])
rows = []
for path in sorted(item for item in root.rglob("*") if item.is_file() and not item.is_symlink()):
    relative = path.relative_to(root).as_posix()
    if relative == "SOURCE-MANIFEST.sha256":
        continue
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    rows.append(f"{digest.hexdigest()}  {relative}\n")
(root / "SOURCE-MANIFEST.sha256").write_text("".join(rows), encoding="utf-8")
PY

mkdir -p "$output_dir"
tar -C "$staging" --exclude='._*' --exclude='.DS_Store' \
  -czf "$archive" YunPin-IME
shasum -a 256 "$archive"
printf 'created corresponding-source archive: %s\n' "$archive"
