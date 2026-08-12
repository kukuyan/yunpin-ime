#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu

if [ "$(uname -s)" != "Darwin" ] || [ "$(id -u)" -eq 0 ]; then
  echo "Run this per-user uninstaller on macOS without sudo." >&2
  exit 1
fi

label=io.github.kukuyan.inputmethod.YunPin.sync-agent
state_dir="$HOME/Library/Application Support/YunPin/Sync"
installed_agent="$state_dir/bin/yunpin-sync-agent"
plist="$HOME/Library/LaunchAgents/$label.plist"
retired="$state_dir/retired/$(date -u +%Y%m%dT%H%M%SZ)"
domain="gui/$(id -u)"

umask 077
launchctl bootout "$domain" "$plist" >/dev/null 2>&1 || true
mkdir -p "$retired"
chmod 700 "$state_dir" "$state_dir/retired" "$retired"
if [ -f "$installed_agent" ] && [ ! -L "$installed_agent" ]; then
  mv "$installed_agent" "$retired/yunpin-sync-agent"
fi
if [ -f "$plist" ] && [ ! -L "$plist" ]; then
  mv "$plist" "$retired/$label.plist"
fi
echo "Background agent retired to $retired; endpoint, encrypted DB, Keychain items, and dictionaries were retained."
