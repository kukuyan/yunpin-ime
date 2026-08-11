#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"

destination="${YUNPIN_RIME_USER_DIR:-${HOME}/Library/Application Support/YunPin/Rime}"
force=0
if [[ "${1:-}" == "--force" ]]; then
  force=1
elif [[ -n "${1:-}" ]]; then
  die "usage: inject-rime-config.sh [--force]"
fi

mkdir -p "$destination"
chmod 700 "$destination"

install_overlay() {
  source_file="$1"
  target_file="$2"
  target="$destination/$target_file"
  if [[ -e "$target" && "$force" -ne 1 ]]; then
    printf 'preserved existing %s\n' "$target"
    return
  fi
  temporary="$(mktemp "$destination/.${target_file}.XXXXXX")"
  cp "$source_file" "$temporary"
  chmod 600 "$temporary"
  mv "$temporary" "$target"
  printf 'installed %s\n' "$target"
}

install_overlay "${REPO_ROOT}/platform/rime/squirrel/default.custom.yaml" "default.custom.yaml"
install_overlay "${REPO_ROOT}/platform/rime/squirrel/squirrel.custom.yaml" "squirrel.custom.yaml"
install_overlay "${REPO_ROOT}/platform/rime/squirrel/rime_ice.custom.yaml" "rime_ice.custom.yaml"
