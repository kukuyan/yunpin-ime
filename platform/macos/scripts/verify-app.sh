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
sync_agent="$app/Contents/MacOS/yunpin-sync-agent"
replay_lab="$app/Contents/MacOS/yunpin-replay-lab"

[[ -x "$executable" ]] || die "missing YunPin executable: $executable"
[[ -x "$sync_agent" ]] || die "missing public sync agent: $sync_agent"
[[ -x "$replay_lab" ]] || die "missing Replay Lab CLI: $replay_lab"
plutil -lint "$plist" >/dev/null
[[ "$(plutil -extract CFBundleIdentifier raw -o - "$plist")" == "$YUNPIN_BUNDLE_ID" ]] || die "unexpected bundle identifier"
[[ "$(plutil -extract TISInputSourceID raw -o - "$plist")" == "$YUNPIN_BUNDLE_ID" ]] || die "unexpected input-source identifier"
[[ "$(plutil -extract InputMethodConnectionName raw -o - "$plist")" == "YunPin_Connection" ]] || die "unexpected IMK connection name"
[[ "$(plutil -extract NSLocalNetworkUsageDescription raw -o - "$plist")" == "$YUNPIN_LOCAL_NETWORK_USAGE_DESCRIPTION" ]] ||
  die "local-network usage description is missing or unexpected"
for update_key in SUEnableAutomaticChecks SUFeedURL SUPublicEDKey SUEnableInstallerLauncherService; do
  if plutil -extract "$update_key" raw -o - "$plist" >/dev/null 2>&1; then
    die "YunPin must not retain Sparkle metadata: $update_key"
  fi
done
for forbidden_name in Sparkle.framework Updater.app Autoupdate Installer.xpc Downloader.xpc; do
  forbidden_path="$(find "$app" -name "$forbidden_name" -print -quit)"
  [[ -z "$forbidden_path" ]] || die "YunPin app contains removed updater component: $forbidden_path"
done
linked_libraries="$(otool -L "$executable")"
[[ "$linked_libraries" != *Sparkle* ]] || die "YunPin executable still links Sparkle"

architectures="$(lipo -archs "$executable")"
if [[ "$require_universal" -eq 1 ]]; then
  [[ " $architectures " == *" arm64 "* && " $architectures " == *" x86_64 "* ]] || die "YunPin executable is not universal: $architectures"
  sync_architectures="$(lipo -archs "$sync_agent")"
  [[ " $sync_architectures " == *" arm64 "* && " $sync_architectures " == *" x86_64 "* ]] || die "public sync agent is not universal: $sync_architectures"
  replay_architectures="$(lipo -archs "$replay_lab")"
  [[ " $replay_architectures " == *" arm64 "* && " $replay_architectures " == *" x86_64 "* ]] || die "Replay Lab CLI is not universal: $replay_architectures"
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

sync_support="$shared_support/SyncAgent"
for required in \
  "$sync_support/Install-LaunchAgent.sh" \
  "$sync_support/Verify-LaunchAgent.sh" \
  "$sync_support/Enable-LaunchAgent.sh" \
  "$sync_support/Uninstall-LaunchAgent.sh" \
  "$sync_support/README.md"; do
  [[ -f "$required" && ! -L "$required" ]] || die "missing public sync-agent support file: $required"
done
[[ -f "$shared_support/licenses/YunPin-Sync-Agent-Go/LICENSES.json" ]] ||
  die "public sync agent license-text bundle is missing"
[[ -f "$shared_support/licenses/YunPin-Replay-Lab-Go/LICENSES.json" ]] ||
  die "Replay Lab license-text bundle is missing"
grep -Fq '"artifact": "yunpin-replay-lab"' \
  "$shared_support/licenses/YunPin-Replay-Lab-Go/LICENSES.json" ||
  die "Replay Lab license manifest names the wrong artifact"
[[ ! -e "$sync_support/BUILD-METADATA.json" && ! -e "$sync_support/SHA256SUMS" ]] ||
  die "private E2E artifact metadata entered the public app bundle"

codesign --verify --strict "$sync_agent" >/dev/null 2>&1 ||
  die "public sync agent does not have a valid signature"
codesign --verify --strict "$replay_lab" >/dev/null 2>&1 ||
  die "Replay Lab CLI does not have a valid signature"
code_identifier() {
  codesign -d --verbose=4 "$1" 2>&1 | awk -F= '$1 == "Identifier" { print substr($0, index($0, "=") + 1); exit }'
}
[[ "$(code_identifier "$sync_agent")" == "$YUNPIN_SYNC_AGENT_ID" ]] ||
  die "public sync agent has an unstable or unexpected code identifier"
[[ "$(code_identifier "$replay_lab")" == "$YUNPIN_REPLAY_LAB_ID" ]] ||
  die "Replay Lab CLI has an unstable or unexpected code identifier"
"$sync_agent" install-probe >/dev/null || die "public sync agent install-probe failed"
set +e
private_command_output="$("$sync_agent" pairing-invite 2>&1)"
private_command_status=$?
set -e
[[ "$private_command_status" -ne 0 && "$private_command_output" == "yunpin-sync-agent: unknown command" ]] ||
  die "public app bundle exposes a private pairing command"
set +e
replay_usage_output="$("$replay_lab" 2>&1)"
replay_usage_status=$?
set -e
[[ "$replay_usage_status" -ne 0 && "$replay_usage_output" == "error: usage: yunpin-replay-lab"* ]] ||
  die "bundled Replay Lab CLI usage probe failed"

[[ -d "$shared_support/cn_dicts" && -d "$shared_support/lua" && -d "$shared_support/opencc" ]] || die "Rime Ice runtime directories are incomplete"
codesign --verify --deep --strict "$app" >/dev/null 2>&1 || \
  die "YunPin app does not have a valid strict deep signature"
otool -L "$executable" | grep -Fq '@rpath/librime.1.dylib' || die "YunPin executable does not link bundled librime"

bundled_librime="$app/Contents/Frameworks/librime.1.dylib"
[[ -f "$bundled_librime" ]] || die "YunPin app does not bundle librime"
nm -gU "$bundled_librime" | grep -F 'rime_require_module_yunpin' >/dev/null || die "bundled librime does not contain the YunPin module"
nm -gU "$bundled_librime" | grep -F 'YunPinStartNativeSelectionSpoolerV1' >/dev/null || die "bundled librime does not contain the YunPin native spooler"
nm -gU "$bundled_librime" | grep -F 'YunPinStartReplaySpoolerV1' >/dev/null || die "bundled librime does not contain the Replay Lab spooler"
if [[ "$require_universal" -eq 1 ]]; then
  librime_architectures="$(lipo -archs "$bundled_librime")"
  [[ " $librime_architectures " == *" arm64 "* && " $librime_architectures " == *" x86_64 "* ]] || die "bundled librime is not universal: $librime_architectures"
fi

plugin_dir="$app/Contents/Frameworks/rime-plugins"
expected_plugins='librime-lua.dylib librime-octagram.dylib librime-predict.dylib '
actual_plugins="$(find "$plugin_dir" -maxdepth 1 -type f -name '*.dylib' -exec basename {} \; | LC_ALL=C sort | tr '\n' ' ')"
[[ "$actual_plugins" == "$expected_plugins" ]] ||
  die "bundled Rime plugin set is incomplete or unexpected: $actual_plugins"
for plugin in librime-lua.dylib librime-octagram.dylib librime-predict.dylib; do
  bundled_plugin="$plugin_dir/$plugin"
  otool -L "$bundled_plugin" | grep -F '@rpath/librime.1.dylib' >/dev/null ||
    die "bundled Rime plugin does not bind to the packaged librime ABI: $plugin"
  plugin_minos="$(xcrun vtool -show-build "$bundled_plugin" | awk '$1 == "minos" { print $2 }' | LC_ALL=C sort -u)"
  [[ "$plugin_minos" == '13.0' ]] ||
    die "bundled Rime plugin has an unexpected deployment target: $plugin ($plugin_minos)"
  if [[ "$require_universal" -eq 1 ]]; then
    plugin_architectures="$(lipo -archs "$bundled_plugin")"
    [[ " $plugin_architectures " == *" arm64 "* && " $plugin_architectures " == *" x86_64 "* ]] ||
      die "bundled Rime plugin is not universal: $plugin ($plugin_architectures)"
  fi
done
for license in \
  librime-lua-BSD-3-Clause-LICENSE \
  Lua-5.4.8-Copyright-Notice.h \
  librime-octagram-GPL-3.0-LICENSE \
  librime-predict-BSD-3-Clause-LICENSE; do
  [[ -f "$shared_support/licenses/$license" ]] ||
    die "rebuilt Rime plugin license is missing: $license"
done
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
