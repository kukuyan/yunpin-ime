#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos

source_dir="${1:-${REPO_ROOT}/build/macos/squirrel}"
[[ -d "$source_dir" ]] || die "prepared Squirrel source is missing: $source_dir"
source_dir="$(cd "$source_dir" && pwd)"
install_dir="$source_dir/librime/dist-yunpin"
library="$install_dir/lib/librime.1.dylib"
[[ -f "$library" ]] || die "build merged librime-yunpin before running its ranking test"

temporary="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-rime-e2e.XXXXXX")"
trap 'rm -rf "$temporary"' EXIT
shared="$temporary/shared"
user="$temporary/user"
mkdir -p "$shared" "$user/yunpin"
cp "${MACOS_DIR}/tests/fixtures/default.yaml" "$shared/default.yaml"
cp "${MACOS_DIR}/tests/fixtures/yunpin_e2e.schema.yaml" "$shared/yunpin_e2e.schema.yaml"
cp "${MACOS_DIR}/tests/fixtures/yunpin_e2e.dict.yaml" "$shared/yunpin_e2e.dict.yaml"
cp "${MACOS_DIR}/tests/fixtures/private.tsv" "$user/yunpin/private.tsv"
chmod 700 "$user" "$user/yunpin"
chmod 600 "$user/yunpin/private.tsv"

driver="$temporary/yunpin_rime_e2e"
xcrun clang++ -std=c++17 -Wall -Wextra -Wpedantic \
  -Wno-missing-field-initializers \
  -I"$install_dir/include" \
  "${MACOS_DIR}/tests/yunpin_rime_e2e.cpp" \
  -L"$install_dir/lib" -lrime \
  -o "$driver"
DYLD_LIBRARY_PATH="$install_dir/lib" "$driver" "$shared" "$user"
