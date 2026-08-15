#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos
require_full_xcode

command -v go >/dev/null 2>&1 || die "Go is required to build the YunPin sync agent"
command -v lipo >/dev/null 2>&1 || die "lipo is required to build universal sync agents"

(
  cd "$REPO_ROOT/desktopagent"
  go mod verify
)

build_root="${YUNPIN_MACOS_BUILD_ROOT:-${REPO_ROOT}/build/macos}"
agent_root="$build_root/sync-agent"
slice_root="$agent_root/slices"
public_root="$agent_root/public"
private_root="$build_root/e2e-private/macos"
public_binary="$public_root/yunpin-sync-agent"
private_binary="$private_root/yunpin-sync-agent"

mkdir -p "$slice_root" "$public_root" "$private_root"
rm -f \
  "$slice_root"/yunpin-sync-agent-* \
  "$public_binary" "$private_binary" \
  "$private_root/BUILD-METADATA.json" "$private_root/SHA256SUMS"

go_quote_argument() {
  local argument="$1"
  if [[ "$argument" == *"'"* && "$argument" == *'"'* ]]; then
    die "cannot safely quote a Go tool argument containing both quote styles"
  fi
  if [[ "$argument" == *"'"* ]]; then
    printf '"%s"' "$argument"
  else
    printf "'%s'" "$argument"
  fi
}

clang="$(xcrun --find clang)"
sdkroot="$(xcrun --sdk macosx --show-sdk-path)"
[[ -d "$sdkroot" && -f "$sdkroot/usr/include/stdlib.h" ]] ||
  die "the selected Xcode macOS SDK is incomplete: $sdkroot"
go_cc="$(go_quote_argument "$clang")"
go_sdkroot="$(go_quote_argument "$sdkroot")"

build_slice() {
  local goarch="$1"
  local clang_arch="$2"
  local output="$3"
  shift 3
  (
    cd "$REPO_ROOT/desktopagent"
    CGO_ENABLED=1 \
      GOOS=darwin \
      GOARCH="$goarch" \
      CC="$go_cc" \
      SDKROOT="$sdkroot" \
      MACOSX_DEPLOYMENT_TARGET=13.0 \
      CGO_CFLAGS="-arch $clang_arch -isysroot $go_sdkroot -mmacosx-version-min=13.0" \
      CGO_LDFLAGS="-arch $clang_arch -isysroot $go_sdkroot -mmacosx-version-min=13.0" \
      go build -trimpath -buildvcs=false "$@" \
        -o "$output" ./cmd/yunpin-sync-agent
  )
}

for variant in public private; do
  if [[ "$variant" == public ]]; then
    build_slice arm64 arm64 "$slice_root/yunpin-sync-agent-$variant-arm64"
    build_slice amd64 x86_64 "$slice_root/yunpin-sync-agent-$variant-x86_64"
  else
    build_slice arm64 arm64 "$slice_root/yunpin-sync-agent-$variant-arm64" \
      -tags=yunpin_pairing_private
    build_slice amd64 x86_64 "$slice_root/yunpin-sync-agent-$variant-x86_64" \
      -tags=yunpin_pairing_private
  fi
  output="$public_binary"
  if [[ "$variant" == private ]]; then
    output="$private_binary"
  fi
  lipo -create \
    "$slice_root/yunpin-sync-agent-$variant-arm64" \
    "$slice_root/yunpin-sync-agent-$variant-x86_64" \
    -output "$output"
  chmod 755 "$output"
  architectures="$(lipo -archs "$output")"
  [[ " $architectures " == *" arm64 "* && " $architectures " == *" x86_64 "* ]] ||
    die "$variant sync agent is not universal: $architectures"
  minos_values="$(xcrun vtool -show-build "$output" | awk '/^[[:space:]]*minos / {print $2}' | LC_ALL=C sort -u)"
  [[ "$minos_values" == "13.0" ]] || die "$variant sync agent minOS is not exactly 13.0: $minos_values"
done

license_root="$agent_root/licenses"
/usr/bin/python3 "$REPO_ROOT/scripts/package_go_licenses.py" \
  --go-module "$REPO_ROOT/desktopagent" --output "$license_root"
ditto "$license_root" "$private_root/licenses"

"$public_binary" install-probe >/dev/null
set +e
public_private_output="$("$public_binary" pairing-invite 2>&1)"
public_private_status=$?
set -e
[[ "$public_private_status" -ne 0 && "$public_private_output" == "yunpin-sync-agent: unknown command" ]] ||
  die "public sync agent exposes a private pairing command"
set +e
public_baseline_output="$("$public_binary" e2e-init-empty-baseline 2>&1)"
public_baseline_status=$?
set -e
[[ "$public_baseline_status" -ne 0 && "$public_baseline_output" == "yunpin-sync-agent: unknown command" ]] ||
  die "public sync agent exposes the private empty-baseline command"

codesign --force --sign - --timestamp=none "$private_binary"
codesign --verify --strict "$private_binary"
"$private_binary" install-probe >/dev/null
set +e
private_gate_output="$("$private_binary" pairing-invite 2>&1)"
private_gate_status=$?
set -e
[[ "$private_gate_status" -ne 0 && "$private_gate_output" == *"pairing-invite requires --confirm-display-invitation"* ]] ||
  die "private E2E sync agent does not expose the confirmation-gated pairing command"
set +e
private_baseline_gate_output="$("$private_binary" e2e-init-empty-baseline 2>&1)"
private_baseline_gate_status=$?
set -e
[[ "$private_baseline_gate_status" -ne 0 && "$private_baseline_gate_output" == *"e2e-init-empty-baseline requires --confirm-create-empty-baseline"* ]] ||
  die "private E2E sync agent does not expose the confirmation-gated empty-baseline command"

repo_commit="$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || printf 'source-export')"
/usr/bin/python3 - "$private_root/BUILD-METADATA.json" "$repo_commit" <<'PY'
import json
import sys

metadata = {
    "schemaVersion": 1,
    "repositoryCommit": sys.argv[2],
    "target": "darwin-universal",
    "buildTag": "yunpin_pairing_private",
    "purpose": "private Mac-R0W E2E acceptance only",
    "publicReleaseEligible": False,
}
with open(sys.argv[1], "w", encoding="utf-8") as output:
    json.dump(metadata, output, ensure_ascii=True, indent=2, sort_keys=True)
    output.write("\n")
PY
(
  cd "$private_root"
  shasum -a 256 BUILD-METADATA.json yunpin-sync-agent \
    $(find licenses -type f | LC_ALL=C sort) >SHA256SUMS
)

printf 'built public macOS sync agent: %s\n' "$public_binary"
printf 'built private E2E-only macOS sync agent: %s\n' "$private_binary"
