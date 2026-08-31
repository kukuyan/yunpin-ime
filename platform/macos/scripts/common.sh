#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

MACOS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "${MACOS_DIR}/../.." && pwd)"
SQUIRREL_COMMIT="876adebaf2f612951dcdca8a591de65401222b9a"
YUNPIN_BUNDLE_ID="io.github.kukuyan.inputmethod.YunPin"
YUNPIN_PRODUCT="YunPin"
YUNPIN_SYNC_AGENT_ID="${YUNPIN_BUNDLE_ID}.sync-agent"
YUNPIN_REPLAY_LAB_ID="${YUNPIN_BUNDLE_ID}.replay-lab"
YUNPIN_LOCAL_NETWORK_USAGE_DESCRIPTION="YunPin uses the local network to synchronize encrypted personal vocabulary with your self-hosted relay."
YUNPIN_DEFAULT_XCODE_PATHS=(
  "/Volumes/YunPinDev/Applications/Xcode.app"
  "/Applications/Xcode.app"
)
YUNPIN_DEFAULT_CMAKE_PATHS=(
  "/Volumes/YunPinDev/Applications/third_party/bin"
  "/usr/local/bin"
  "/opt/homebrew/bin"
)

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

resolve_macos_build_jobs() {
  local build_jobs="${YUNPIN_MACOS_BUILD_JOBS-2}"
  [[ "$build_jobs" =~ ^[1-9][0-9]*$ ]] ||
    die "YUNPIN_MACOS_BUILD_JOBS must be a positive integer"
  printf '%s\n' "$build_jobs"
}

resolve_developer_dir() {
  if [[ -n "${DEVELOPER_DIR:-}" ]]; then
    if [[ -x "$DEVELOPER_DIR/usr/bin/xcodebuild" ]]; then
      export PATH="$DEVELOPER_DIR/usr/bin:$PATH"
      return
    fi
    printf 'warning: invalid DEVELOPER_DIR=%s; ignoring\n' "$DEVELOPER_DIR"
  fi

  if [[ -n "${YUNPIN_XCODE_APP_PATH:-}" && -x "${YUNPIN_XCODE_APP_PATH%/}/Contents/Developer/usr/bin/xcodebuild" ]]; then
    DEVELOPER_DIR="${YUNPIN_XCODE_APP_PATH%/}/Contents/Developer"
    export DEVELOPER_DIR
    export PATH="$DEVELOPER_DIR/usr/bin:$PATH"
    return
  fi

  # Respect the active developer directory before probing conventional app
  # locations. CI selects a versioned Xcode bundle with xcode-select; choosing
  # /Applications/Xcode.app first can silently replace that selection with an
  # older default Xcode.
  local xcode_select_path=""
  if xcode_select_path="$(xcode-select -p 2>/dev/null)"; then
    if [[ -x "$xcode_select_path/usr/bin/xcodebuild" && "$xcode_select_path" != *"/CommandLineTools" ]]; then
      DEVELOPER_DIR="$xcode_select_path"
      export DEVELOPER_DIR
      export PATH="$DEVELOPER_DIR/usr/bin:$PATH"
      return
    fi
  fi

  local selected=""
  for candidate in "${YUNPIN_DEFAULT_XCODE_PATHS[@]}"; do
    if [[ -x "$candidate/Contents/Developer/usr/bin/xcodebuild" ]]; then
      selected="$candidate"
      break
    fi
  done
  if [[ -z "$selected" ]]; then
    for candidate in /Volumes/*/Applications/Xcode.app; do
      [[ -x "$candidate/Contents/Developer/usr/bin/xcodebuild" ]] || continue
      selected="$candidate"
      break
    done
  fi

  if [[ -n "$selected" ]]; then
    DEVELOPER_DIR="${selected%/}/Contents/Developer"
    export DEVELOPER_DIR
    export PATH="$DEVELOPER_DIR/usr/bin:$PATH"
    return
  fi
}

resolve_cmake() {
  if command -v cmake >/dev/null 2>&1; then
    return
  fi

  local selected=""
  for candidate in "${YUNPIN_DEFAULT_CMAKE_PATHS[@]}"; do
    [[ -x "$candidate/cmake" ]] || continue
    export PATH="$candidate:$PATH"
    local candidate_pythonpath="${candidate%/}/../lib/python3.9/site-packages"
    if [[ -d "$candidate_pythonpath" ]]; then
      if [[ -z "${PYTHONPATH:-}" ]]; then
        export PYTHONPATH="$candidate_pythonpath"
      else
        export PYTHONPATH="$candidate_pythonpath:$PYTHONPATH"
      fi
    fi
    if command -v cmake >/dev/null 2>&1; then
      selected="$candidate"
      break
    fi
  done

  [[ -n "$selected" ]] || die "cmake is required to build merged librime-yunpin"
}

require_macos() {
  [[ "$(uname -s)" == "Darwin" ]] || die "the native macOS build requires Darwin"
}

require_clean_repository() {
  local dirty
  dirty="$(git -C "$REPO_ROOT" status --porcelain --untracked-files=normal)" ||
    die "unable to inspect the YunPin repository state"
  [[ -z "$dirty" ]] ||
    die "packaging requires a clean YunPin repository so binaries and corresponding source use the same commit"
}

require_full_xcode() {
  resolve_developer_dir
  command -v xcodebuild >/dev/null 2>&1 || die "xcodebuild is unavailable; install full Xcode"
  xcodebuild -version >/dev/null 2>&1 || die "full Xcode is required; Command Line Tools alone are insufficient"
  xcode_major="$(xcodebuild -version | awk 'NR == 1 {split($2, version, "."); print version[1]}')"
  [[ "$xcode_major" =~ ^[0-9]+$ && "$xcode_major" -ge 26 ]] || die "Xcode 26 or later is required by the pinned Squirrel icon project"
}

read_lock_value() {
  /usr/bin/python3 - "$MACOS_DIR/dependencies.lock.json" "$1" <<'PY'
import json
import sys

document = json.load(open(sys.argv[1], encoding="utf-8"))
value = document
for component in sys.argv[2].split("."):
    value = value[component]
print(value)
PY
}

verify_locked_grammar_resource() {
  local path="$1"
  local expected_size="$2"
  local expected_sha256="$3"
  local label="$4"
  local actual_size
  local actual_sha256

  [[ -f "$path" && ! -L "$path" ]] ||
    die "$label is missing, linked, or not a regular file: $path"
  actual_size="$(/usr/bin/stat -f%z "$path")" ||
    die "could not read $label size: $path"
  [[ "$actual_size" == "$expected_size" ]] ||
    die "$label size mismatch: expected $expected_size, observed $actual_size"
  actual_sha256="$(/usr/bin/shasum -a 256 "$path" | /usr/bin/awk '{print $1}')" ||
    die "could not hash $label: $path"
  [[ "$actual_sha256" == "$expected_sha256" ]] ||
    die "$label SHA-256 mismatch"
}

resolve_locked_grammar_resource() {
  local kind="$1"
  local cache_dir="${YUNPIN_MACOS_CACHE_DIR:-${REPO_ROOT}/build/macos/downloads}"
  local filename
  local expected_size
  local expected_sha256
  local label
  local candidate

  case "$kind" in
    model)
      filename="$(read_lock_value grammarModel.filename)"
      expected_size="$(read_lock_value grammarModel.size)"
      expected_sha256="$(read_lock_value grammarModel.sha256)"
      label="grammar model"
      ;;
    license)
      filename="$(read_lock_value grammarModel.licenseFilename)"
      expected_size="$(read_lock_value grammarModel.licenseSize)"
      expected_sha256="$(read_lock_value grammarModel.licenseSha256)"
      label="grammar model license"
      ;;
    *)
      die "unknown grammar resource kind: $kind"
      ;;
  esac

  for candidate in "$cache_dir/$filename" "$REPO_ROOT/sources/$filename"; do
    [[ -e "$candidate" || -L "$candidate" ]] || continue
    verify_locked_grammar_resource \
      "$candidate" "$expected_size" "$expected_sha256" "$label"
    printf '%s\n' "$candidate"
    return 0
  done
  die "verified $label is unavailable as exact file $filename"
}
