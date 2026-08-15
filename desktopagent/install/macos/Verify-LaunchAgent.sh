#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu

if [ "$(uname -s)" != "Darwin" ] || [ "$(id -u)" -eq 0 ]; then
  echo "Run this per-user verifier on macOS without sudo." >&2
  exit 1
fi

label=io.github.kukuyan.inputmethod.YunPin.sync-agent
state_dir="$HOME/Library/Application Support/YunPin/Sync"
installed_agent="$state_dir/bin/yunpin-sync-agent"
plist="$HOME/Library/LaunchAgents/$label.plist"
domain="gui/$(id -u)"

launchagent_is_disabled() {
  launchctl print-disabled "$domain" 2>/dev/null | awk -v target="\"$label\"" '
    $1 == target && $2 == "=>" && ($3 == "true" || $3 == "disabled") { found = 1 }
    END { exit found ? 0 : 1 }
  '
}

[ -d "$state_dir" ] && [ ! -L "$state_dir" ] || { echo "private sync state is absent or unsafe" >&2; exit 1; }
[ -f "$installed_agent" ] && [ ! -L "$installed_agent" ] && [ -x "$installed_agent" ] || { echo "resident agent is absent or unsafe" >&2; exit 1; }
[ -f "$plist" ] && [ ! -L "$plist" ] || { echo "LaunchAgent registration is absent or unsafe" >&2; exit 1; }
[ "$(stat -f '%Su:%Lp' "$state_dir")" = "$(id -un):700" ] || { echo "private sync state owner or mode differs" >&2; exit 1; }
[ "$(stat -f '%Su:%Lp' "$plist")" = "$(id -un):600" ] || { echo "LaunchAgent owner or mode differs" >&2; exit 1; }
[ "$(plutil -extract Label raw -o - "$plist")" = "$label" ] || { echo "LaunchAgent label differs" >&2; exit 1; }
[ "$(plutil -extract ProgramArguments.0 raw -o - "$plist")" = "$installed_agent" ] || { echo "LaunchAgent executable differs" >&2; exit 1; }
[ "$(plutil -extract ProgramArguments.1 raw -o - "$plist")" = "run" ] || { echo "LaunchAgent command differs" >&2; exit 1; }
launchagent_is_disabled || { echo "LaunchAgent is not registered as disabled" >&2; exit 1; }
if launchctl print "$domain/$label" >/dev/null 2>&1; then
  echo "Disabled LaunchAgent is unexpectedly loaded" >&2
  exit 1
fi
"$installed_agent" install-probe >/dev/null
echo "YunPin sync LaunchAgent installation and disabled registration verified."
