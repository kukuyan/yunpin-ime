#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu

if [ "$(uname -s)" != "Darwin" ]; then
  echo "Install-LaunchAgent.sh must run as the signed-in macOS user." >&2
  exit 1
fi
if [ "$(id -u)" -eq 0 ]; then
  echo "Do not run this per-user installer with sudo." >&2
  exit 1
fi
if [ "$#" -ne 1 ]; then
  echo "usage: Install-LaunchAgent.sh /absolute/path/to/yunpin-sync-agent" >&2
  exit 1
fi

source_agent=$1
case "$source_agent" in
  /*) ;;
  *) echo "agent path must be absolute" >&2; exit 1 ;;
esac
if [ ! -f "$source_agent" ] || [ -L "$source_agent" ] || [ ! -x "$source_agent" ]; then
  echo "agent must be an executable regular file, not a symlink" >&2
  exit 1
fi
source_owner=$(stat -f '%u' "$source_agent")
if [ "$source_owner" -ne "$(id -u)" ] && [ "$source_owner" -ne 0 ]; then
  echo "agent source must be owned by the signed-in user or root" >&2
  exit 1
fi

label=io.github.kukuyan.inputmethod.YunPin.sync-agent
state_dir="$HOME/Library/Application Support/YunPin/Sync"
bin_dir="$state_dir/bin"
installed_agent="$bin_dir/yunpin-sync-agent"
launch_dir="$HOME/Library/LaunchAgents"
plist="$launch_dir/$label.plist"
temporary_agent="$bin_dir/.yunpin-sync-agent.tmp.$$"
temporary_plist="$launch_dir/.$label.tmp.$$.plist"
domain="gui/$(id -u)"
rollback_agent="$bin_dir/.yunpin-sync-agent.rollback.$$"
rollback_plist="$launch_dir/.$label.rollback.$$.plist"
had_agent=0
had_plist=0
committed=0

launchagent_is_disabled() {
  launchctl print-disabled "$domain" 2>/dev/null | awk -v target="\"$label\"" '
    $1 == target && $2 == "=>" && ($3 == "true" || $3 == "disabled") { found = 1 }
    END { exit found ? 0 : 1 }
  '
}

rollback_install() {
  status=$?
  set +e
  if [ "$committed" -eq 0 ]; then
    if [ "$had_agent" -eq 1 ]; then
      mv -f "$rollback_agent" "$installed_agent"
    else
      rm -f "$installed_agent"
    fi
    if [ "$had_plist" -eq 1 ]; then
      mv -f "$rollback_plist" "$plist"
    else
      rm -f "$plist"
    fi
    launchctl disable "$domain/$label" >/dev/null 2>&1
  fi
  rm -f "$temporary_agent" "$temporary_plist" "$rollback_agent" "$rollback_plist"
  exit "$status"
}
trap rollback_install EXIT
trap 'exit 1' HUP INT TERM

umask 077
mkdir -p "$state_dir" "$bin_dir" "$launch_dir"
chmod 700 "$state_dir" "$bin_dir"
if launchctl print "$domain/$label" >/dev/null 2>&1; then
  echo "Refusing to replace a loaded LaunchAgent; stop or uninstall it before staging." >&2
  exit 1
fi
if [ -f "$installed_agent" ] && [ ! -L "$installed_agent" ]; then
  cp -p "$installed_agent" "$rollback_agent"
  had_agent=1
fi
if [ -f "$plist" ] && [ ! -L "$plist" ]; then
  cp -p "$plist" "$rollback_plist"
  had_plist=1
fi
install -m 700 "$source_agent" "$temporary_agent"
mv -f "$temporary_agent" "$installed_agent"

plutil -create xml1 "$temporary_plist"
plutil -insert Label -string "$label" "$temporary_plist"
plutil -insert ProgramArguments -xml '<array/>' "$temporary_plist"
plutil -insert ProgramArguments.0 -string "$installed_agent" "$temporary_plist"
plutil -insert ProgramArguments.1 -string run "$temporary_plist"
plutil -insert ProgramArguments.2 -string --interval "$temporary_plist"
plutil -insert ProgramArguments.3 -string 1m "$temporary_plist"
plutil -insert RunAtLoad -bool NO "$temporary_plist"
plutil -insert KeepAlive -bool YES "$temporary_plist"
plutil -insert ProcessType -string Background "$temporary_plist"
plutil -insert ThrottleInterval -integer 30 "$temporary_plist"
plutil -insert StandardOutPath -string /dev/null "$temporary_plist"
plutil -insert StandardErrorPath -string /dev/null "$temporary_plist"
chmod 600 "$temporary_plist"
plutil -lint "$temporary_plist" >/dev/null
mv -f "$temporary_plist" "$plist"

launchctl disable "$domain/$label"
if ! launchagent_is_disabled; then
  echo "LaunchAgent could not be registered in the disabled state." >&2
  exit 1
fi
if launchctl print "$domain/$label" >/dev/null 2>&1; then
  echo "Disabled LaunchAgent unexpectedly remained loaded." >&2
  exit 1
fi
"$installed_agent" install-probe >/dev/null
committed=1
echo "Installed and locally verified $label; it remains disabled until Enable-LaunchAgent.sh is run after setup."
