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

# The patch series carries the whole preview identity, so verify it against the
# lock before touching a checkout. Windows already does this through
# platform/windows/dependencies.lock.json.
/usr/bin/python3 - "${MACOS_DIR}/dependencies.lock.json" "$REPO_ROOT" "$patch_dir" <<'VERIFY' || die "Squirrel patch series failed verification against the macOS dependency lock"
import hashlib
import json
import sys
from pathlib import Path

lock, repo_root, patch_dir = (Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3]))
rows = json.loads(lock.read_text(encoding="utf-8")).get("squirrel_patches")
if not rows:
    sys.exit("dependency lock does not record the Squirrel patch series")
if [repo_root / row["path"] for row in rows] != sorted(patch_dir.glob("*.patch")):
    sys.exit("locked Squirrel patch series does not match the files on disk")
for row in rows:
    if hashlib.sha256((repo_root / row["path"]).read_bytes()).hexdigest() != row["sha256"]:
        sys.exit(f"digest mismatch for {row['path']}")
VERIFY

mkdir -p "$(dirname "$output")"
git clone --quiet --no-hardlinks "$source_checkout" "$output"
git -C "$output" checkout --quiet --detach "$SQUIRREL_COMMIT"
for patch_file in "$patch_dir"/*.patch; do
  git -C "$output" apply --check --whitespace=error-all "$patch_file"
  git -C "$output" apply --whitespace=error-all "$patch_file"
done

cp "${MACOS_DIR}/assets/yunpin-mark.svg" "$output/Rime.icon/Assets/logo.svg"
cp "${REPO_ROOT}/platform/rime/squirrel/default.custom.yaml" "$output/data/default.custom.yaml"
cp "${REPO_ROOT}/platform/rime/squirrel/squirrel.custom.yaml" "$output/data/squirrel.custom.yaml"
cp "${REPO_ROOT}/platform/rime/squirrel/rime_ice.custom.yaml" "$output/data/rime_ice.custom.yaml"
cp "${MACOS_DIR}/preview-manifest.json" "$output/data/yunpin-preview.json"

printf '%s\n' "$SQUIRREL_COMMIT" > "$output/.yunpin-base-commit"
printf 'prepared YunPin Squirrel source at %s\n' "$output"
