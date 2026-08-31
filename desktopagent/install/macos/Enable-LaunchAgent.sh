#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu

if [ "$(uname -s)" != "Darwin" ] || [ "$(id -u)" -eq 0 ]; then
  echo "Run this per-user enabler on macOS without sudo." >&2
  exit 1
fi
if [ "$#" -ne 0 ]; then
  echo "usage: Enable-LaunchAgent.sh" >&2
  exit 1
fi

label=io.github.kukuyan.inputmethod.YunPin.sync-agent
responsible_bundle=io.github.kukuyan.inputmethod.YunPin
state_dir="$HOME/Library/Application Support/YunPin/Sync"
installed_agent="$state_dir/bin/yunpin-sync-agent"
plist="$HOME/Library/LaunchAgents/$label.plist"
domain="gui/$(id -u)"
committed=0

fail_closed() {
  status=$?
  set +e
  if [ "$committed" -eq 0 ]; then
    launchctl bootout "$domain" "$plist" >/dev/null 2>&1
    launchctl disable "$domain/$label" >/dev/null 2>&1
  fi
  exit "$status"
}
trap fail_closed EXIT
trap 'exit 1' HUP INT TERM

[ -f "$installed_agent" ] && [ ! -L "$installed_agent" ] && [ -x "$installed_agent" ] || { echo "resident agent is absent or unsafe" >&2; exit 1; }
[ -f "$plist" ] && [ ! -L "$plist" ] || { echo "LaunchAgent registration is absent or unsafe" >&2; exit 1; }
codesign --verify --strict "$installed_agent" >/dev/null 2>&1 || { echo "resident agent code signature is invalid" >&2; exit 1; }
[ "$(codesign -d --verbose=2 "$installed_agent" 2>&1 | sed -n 's/^Identifier=//p')" = "$label" ] || { echo "resident agent code identifier differs" >&2; exit 1; }
[ "$(plutil -extract Label raw -o - "$plist")" = "$label" ] || { echo "LaunchAgent label differs" >&2; exit 1; }
[ "$(plutil -extract ProgramArguments.0 raw -o - "$plist")" = "$installed_agent" ] || { echo "LaunchAgent executable differs" >&2; exit 1; }
[ "$(plutil -extract ProgramArguments.1 raw -o - "$plist")" = "run" ] || { echo "LaunchAgent command differs" >&2; exit 1; }
[ "$(plutil -extract AssociatedBundleIdentifiers.0 raw -o - "$plist")" = "$responsible_bundle" ] || { echo "LaunchAgent responsible bundle differs" >&2; exit 1; }
if plutil -extract AssociatedBundleIdentifiers.1 raw -o - "$plist" >/dev/null 2>&1; then
  echo "LaunchAgent has unexpected additional responsible bundles" >&2
  exit 1
fi

# This local, redacted gate additionally requires finalized two-device trust,
# no pending protected setup journal, and complete private Rime bridge state.
if ! "$installed_agent" resident-ready >/dev/null 2>&1; then
  echo "YunPin sync setup or pairing is incomplete; the resident job remains disabled." >&2
  exit 1
fi

launchctl enable "$domain/$label"
if ! launchctl print "$domain/$label" >/dev/null 2>&1; then
  launchctl bootstrap "$domain" "$plist"
fi
launchctl kickstart -k "$domain/$label"
launchctl print "$domain/$label" >/dev/null
committed=1
echo "YunPin sync LaunchAgent enabled and started after local setup validation."
