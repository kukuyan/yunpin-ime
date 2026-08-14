#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos
require_full_xcode

app="${1:-${REPO_ROOT}/build/macos/DerivedData/Build/Products/Release/YunPin.app}"
source_dir="${2:-${REPO_ROOT}/build/macos/squirrel}"
frameworks="$app/Contents/Frameworks"
shared_support="$app/Contents/SharedSupport"
librime="$frameworks/librime.1.dylib"
plugin_dir="$frameworks/rime-plugins"
probe_source="$MACOS_DIR/tests/rime_public_candidate_probe.cpp"

[[ -f "$librime" && -d "$plugin_dir" && -d "$shared_support" ]] ||
  die "YunPin app is incomplete for the real Rime plugin runtime test"
[[ -f "$source_dir/librime/src/rime_api.h" && -f "$probe_source" ]] ||
  die "Rime runtime probe source or headers are missing"

expected_plugins='librime-lua.dylib librime-octagram.dylib librime-predict.dylib '
actual_plugins="$(find "$plugin_dir" -maxdepth 1 -type f -name '*.dylib' -exec basename {} \; | LC_ALL=C sort | tr '\n' ' ')"
[[ "$actual_plugins" == "$expected_plugins" ]] ||
  die "YunPin app has an incomplete or unexpected Rime plugin set: $actual_plugins"

temporary="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-rime-plugin-runtime.XXXXXX")"
trap 'rm -rf "$temporary"' EXIT
probe="$temporary/rime-public-candidate-probe"
user_dir="$temporary/user"
mkdir -m 700 "$user_dir"
mkdir -m 700 "$user_dir/lua"
install -m 600 "$shared_support/lua/lunar.db" "$user_dir/lua/lunar.db"

xcrun clang++ \
  -std=c++17 -Wall -Wextra -Wpedantic -Werror \
  -Wno-missing-field-initializers \
  -mmacosx-version-min=13.0 \
  -I "$source_dir/librime/src" \
  "$probe_source" "$librime" \
  -Wl,-rpath,"$frameworks" \
  -o "$probe"

set +e
DYLD_PRINT_LIBRARIES=1 "$probe" "$shared_support" "$user_dir" \
  >"$temporary/stdout" 2>"$temporary/stderr"
probe_status=$?
set -e
if [[ "$probe_status" -ne 0 ]]; then
  cat "$temporary/stdout" >&2
  tail -n 200 "$temporary/stderr" >&2
  die "real Rime plugin runtime probe failed with status $probe_status"
fi

for plugin in librime-lua.dylib librime-octagram.dylib librime-predict.dylib; do
  grep -F "$plugin_dir/$plugin" "$temporary/stderr" >/dev/null ||
    die "real Rime runtime did not load packaged plugin: $plugin"
done
for expected in \
  's:' 'sh:' 'shu:' 'shuru:' 'ceshi:' 'wendingxing:' \
  'lifecycle_sessions=128'; do
  grep -F "$expected" "$temporary/stdout" >/dev/null ||
    die "real Rime runtime probe omitted expected result: $expected"
done
if grep -E '^(s|sh|shu|shuru|ceshi|wendingxing):.* han=no$' "$temporary/stdout" >/dev/null; then
  cat "$temporary/stdout" >&2
  die "real Rime runtime returned an English-only public candidate page"
fi

cat "$temporary/stdout"
printf 'verified real same-toolchain Rime plugins and 128 session lifecycles\n'
