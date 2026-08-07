#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos

require_universal=0
if [[ "${1:-}" == "--require-universal" ]]; then
  require_universal=1
  shift
fi
app="${1:-${REPO_ROOT}/build/macos/DerivedData/Build/Products/Release/YunPin.app}"
plist="$app/Contents/Info.plist"
executable="$app/Contents/MacOS/YunPin"

[[ -x "$executable" ]] || die "missing YunPin executable: $executable"
plutil -lint "$plist" >/dev/null
[[ "$(plutil -extract CFBundleIdentifier raw -o - "$plist")" == "$YUNPIN_BUNDLE_ID" ]] || die "unexpected bundle identifier"
[[ "$(plutil -extract TISInputSourceID raw -o - "$plist")" == "$YUNPIN_BUNDLE_ID" ]] || die "unexpected input-source identifier"
[[ "$(plutil -extract InputMethodConnectionName raw -o - "$plist")" == "YunPin_Connection" ]] || die "unexpected IMK connection name"
[[ "$(plutil -extract SUEnableAutomaticChecks raw -o - "$plist")" == "false" ]] || die "automatic updates must be disabled"
if plutil -extract SUFeedURL raw -o - "$plist" >/dev/null 2>&1; then
  die "development preview must not retain the upstream update feed"
fi

architectures="$(lipo -archs "$executable")"
if [[ "$require_universal" -eq 1 ]]; then
  [[ " $architectures " == *" arm64 "* && " $architectures " == *" x86_64 "* ]] || die "YunPin executable is not universal: $architectures"
fi

shared_support="$app/Contents/SharedSupport"
for required in \
  "$shared_support/default.custom.yaml" \
  "$shared_support/squirrel.custom.yaml" \
  "$shared_support/rime_ice.custom.yaml" \
  "$shared_support/rime_ice.schema.yaml" \
  "$shared_support/rime_ice.dict.yaml" \
  "$shared_support/yunpin-preview.json" \
  "$app/Contents/Resources/yunpin.pdf"; do
  [[ -f "$required" ]] || die "missing packaged resource: $required"
done

[[ -d "$shared_support/cn_dicts" && -d "$shared_support/lua" && -d "$shared_support/opencc" ]] || die "Rime Ice runtime directories are incomplete"
if ! codesign --verify --deep --strict "$app" >/dev/null 2>&1; then
  printf 'warning: skipping strict bundle signature verification for unsigned preview artifact\n'
fi
otool -L "$executable" | grep -Fq '@rpath/librime.1.dylib' || die "YunPin executable does not link bundled librime"

bundled_librime="$app/Contents/Frameworks/librime.1.dylib"
[[ -f "$bundled_librime" ]] || die "YunPin app does not bundle librime"
nm -gU "$bundled_librime" | grep -F 'rime_require_module_yunpin' >/dev/null || die "bundled librime does not contain the YunPin module"
if [[ "$require_universal" -eq 1 ]]; then
  librime_architectures="$(lipo -archs "$bundled_librime")"
  [[ " $librime_architectures " == *" arm64 "* && " $librime_architectures " == *" x86_64 "* ]] || die "bundled librime is not universal: $librime_architectures"
fi
/usr/bin/python3 - "$shared_support/yunpin-preview.json" <<'PY'
import json
import sys

manifest = json.load(open(sys.argv[1], encoding="utf-8"))
if manifest.get("yunpin_module_merged") is not True:
    raise SystemExit("preview manifest does not record the merged YunPin module")
if manifest.get("yunpin_ranking_native_host_e2e") is not False:
    raise SystemExit("development preview must not overstate native host evidence")
PY

public_lunar_db="$shared_support/lua/lunar.db"
[[ -f "$public_lunar_db" ]] || die "locked Rime Ice lunar database is missing"
source_lunar_hash="$(shasum -a 256 "${REPO_ROOT}/third_party/rime-ice/lua/lunar.db" | awk '{print $1}')"
bundled_lunar_hash="$(shasum -a 256 "$public_lunar_db" | awk '{print $1}')"
[[ "$bundled_lunar_hash" == "$source_lunar_hash" ]] || die "bundled Rime Ice lunar database does not match the locked source"
while IFS= read -r -d '' candidate; do
  [[ "$candidate" == "$public_lunar_db" ]] || die "forbidden personal or credential material found in app bundle: $candidate"
done < <(find "$app" -type f \( -name '*.scel' -o -name '*.sgpybin' -o -name '*.sqlite' -o -name '*.db' -o -name '*.pem' -o -name '*.p12' \) -print0)

printf 'verified YunPin.app: bundle=%s architectures=%s updates=offline\n' "$YUNPIN_BUNDLE_ID" "$architectures"
