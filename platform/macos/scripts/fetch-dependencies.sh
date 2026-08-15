#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"

source_dir="${1:-${REPO_ROOT}/build/macos/squirrel}"
cache_dir="${YUNPIN_MACOS_CACHE_DIR:-${REPO_ROOT}/build/macos/downloads}"
[[ -f "$source_dir/.yunpin-base-commit" ]] || die "run prepare-source.sh before fetching dependencies"
mkdir -p "$cache_dir" "$source_dir/download"

while IFS=$'\t' read -r name url expected; do
  archive="$cache_dir/$name"
  if [[ ! -f "$archive" ]]; then
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --output "$archive.part" "$url"
    mv "$archive.part" "$archive"
  fi
  actual="$(shasum -a 256 "$archive" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || die "SHA-256 mismatch for $name"
  cp "$archive" "$source_dir/download/$name"
done < <(/usr/bin/python3 - "$MACOS_DIR/dependencies.lock.json" <<'PY'
import json
import sys

for archive in json.load(open(sys.argv[1], encoding="utf-8"))["archives"]:
    print(archive["name"], archive["url"], archive["sha256"], sep="\t")
PY
)

expected_plum="$(read_lock_value nested_submodules.plum)"
git -C "$source_dir" submodule update --init plum
actual_plum="$(git -C "$source_dir/plum" rev-parse HEAD)"
[[ "$actual_plum" == "$expected_plum" ]] || die "Plum checkout $actual_plum does not match lock $expected_plum"

# Squirrel's action-install.sh copies verified prebuilt runtime files below the
# librime gitlink. Initialize and verify the exact source checkout first;
# otherwise that copy creates a non-empty directory which Git cannot later
# populate as a submodule when the YunPin merged plugin is staged.
expected_librime="$(read_lock_value nested_submodules.librime)"
git -C "$source_dir" submodule update --init librime
actual_librime="$(git -C "$source_dir/librime" rev-parse HEAD)"
[[ "$actual_librime" == "$expected_librime" ]] || die "librime checkout $actual_librime does not match lock $expected_librime"

boost_version="$(read_lock_value boost_version)"
boost_archive="boost_${boost_version//./_}.tar.gz"
boost_sha256="$(/usr/bin/python3 - "$MACOS_DIR/dependencies.lock.json" <<'PY'
import json
import sys

archives = json.load(open(sys.argv[1], encoding="utf-8"))["archives"]
matches = [item for item in archives if item.get("boost_source") is True]
if len(matches) != 1:
    raise SystemExit("dependency lock must contain exactly one Boost source archive")
print(matches[0]["sha256"])
PY
)"
boost_root="$source_dir/librime/deps/boost-$boost_version"
boost_marker="$boost_root/.yunpin-source-sha256"
if [[ -d "$boost_root" ]]; then
  [[ -f "$boost_marker" && "$(<"$boost_marker")" == "$boost_sha256" ]] || die "unexpected existing Boost source: $boost_root"
else
  temporary="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-boost.XXXXXX")"
  tar -xzf "$source_dir/download/$boost_archive" -C "$temporary"
  extracted="$temporary/boost_${boost_version//./_}"
  [[ -f "$extracted/boost/version.hpp" && -f "$extracted/LICENSE_1_0.txt" ]] || die "Boost source archive is incomplete"
  printf '%s\n' "$boost_sha256" > "$extracted/.yunpin-source-sha256"
  mkdir -p "$(dirname "$boost_root")"
  mv "$extracted" "$boost_root"
  rmdir "$temporary"
fi

# The upstream librime release archive carries external C++ plugins that were
# built by an older Xcode toolchain.  Loading those dylibs into a freshly built
# YunPin librime mixes two libc++ ABIs and corrupts session teardown.  Stage the
# exact source revisions recorded by the release's version-info.txt so the
# plugins can be rebuilt together with the YunPin core.
while IFS=$'\t' read -r name plugin commit; do
  plugin_dir="$source_dir/librime/plugins/$plugin"
  marker="$plugin_dir/.yunpin-source-commit"
  if [[ -d "$plugin_dir" ]]; then
    [[ -f "$marker" && "$(<"$marker")" == "$commit" ]] || die "unexpected existing Rime plugin source: $plugin_dir"
    continue
  fi
  temporary="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-rime-plugin.XXXXXX")"
  tar -xzf "$cache_dir/$name" -C "$temporary" --strip-components 1
  [[ -f "$temporary/CMakeLists.txt" && -f "$temporary/LICENSE" ]] || die "Rime plugin source archive is incomplete: $name"
  printf '%s\n' "$commit" > "$temporary/.yunpin-source-commit"
  mkdir -p "$(dirname "$plugin_dir")"
  mv "$temporary" "$plugin_dir"
done < <(/usr/bin/python3 - "$MACOS_DIR/dependencies.lock.json" <<'PY'
import json
import sys

for archive in json.load(open(sys.argv[1], encoding="utf-8"))["archives"]:
    if "rime_plugin" in archive:
        print(archive["name"], archive["rime_plugin"], archive["commit"], sep="\t")
PY
)

while IFS=$'\t' read -r name plugin commit; do
  thirdparty_dir="$source_dir/librime/plugins/$plugin/thirdparty"
  marker="$thirdparty_dir/.yunpin-source-commit"
  if [[ -d "$thirdparty_dir" ]]; then
    [[ -f "$marker" && "$(<"$marker")" == "$commit" ]] || die "unexpected existing Rime plugin third-party source: $thirdparty_dir"
    continue
  fi
  temporary="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-rime-plugin-thirdparty.XXXXXX")"
  tar -xzf "$cache_dir/$name" -C "$temporary" --strip-components 1
  [[ -f "$temporary/lua5.4/lua.h" && -f "$temporary/lua5.4/lapi.c" ]] || die "Rime plugin third-party source archive is incomplete: $name"
  printf '%s\n' "$commit" > "$temporary/.yunpin-source-commit"
  mv "$temporary" "$thirdparty_dir"
done < <(/usr/bin/python3 - "$MACOS_DIR/dependencies.lock.json" <<'PY'
import json
import sys

for archive in json.load(open(sys.argv[1], encoding="utf-8"))["archives"]:
    if "rime_plugin_thirdparty" in archive:
        print(
            archive["name"],
            archive["rime_plugin_thirdparty"],
            archive["commit"],
            sep="\t",
        )
PY
)

while IFS=$'\t' read -r name package commit; do
  package_dir="$source_dir/plum/package/rime/$package"
  marker="$package_dir/.yunpin-source-commit"
  if [[ -d "$package_dir" ]]; then
    [[ -f "$marker" && "$(<"$marker")" == "$commit" ]] || die "unexpected existing Plum package: $package_dir"
    [[ "$(git -C "$package_dir" symbolic-ref --quiet --short HEAD || true)" == "locked" ]] || die "Plum package is not isolated on its locked local branch: $package_dir"
    continue
  fi
  temporary="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-plum-package.XXXXXX")"
  tar -xzf "$cache_dir/$name" -C "$temporary" --strip-components 1
  printf '%s\n' "$commit" > "$temporary/.yunpin-source-commit"
  git -C "$temporary" init --quiet --initial-branch=locked
  mkdir -p "$(dirname "$package_dir")"
  mv "$temporary" "$package_dir"
done < <(/usr/bin/python3 - "$MACOS_DIR/dependencies.lock.json" <<'PY'
import json
import sys

for archive in json.load(open(sys.argv[1], encoding="utf-8"))["archives"]:
    if "plum_package" in archive:
        print(archive["name"], archive["plum_package"], archive["commit"], sep="\t")
PY
)

(
  cd "$source_dir"
  no_download=1 no_update=1 SQUIRREL_BUNDLED_RECIPES="bopomofo@locked cangjie@locked essay@locked luna-pinyin@locked prelude@locked stroke@locked terra-pinyin@locked" ./action-install.sh
)

actual_source_commit="$(git -C "$source_dir" rev-parse HEAD)"
[[ "$actual_source_commit" == "$SQUIRREL_COMMIT" ]] || die "dependency installation moved the Squirrel source checkout"
[[ -z "$(git -C "$source_dir" symbolic-ref --quiet --short HEAD || true)" ]] || die "dependency installation attached the Squirrel source to a moving branch"

while IFS= read -r file_path; do
  [[ -f "$source_dir/$file_path" ]] || die "Squirrel project input is missing after dependency installation: $file_path"
done < <(sed -n 's/.*path = \(data\/plum\/[^;]*\);.*/\1/p' "$source_dir/Squirrel.xcodeproj/project.pbxproj" | sort -u)

touch "$source_dir/.yunpin-dependencies-ready"
printf 'verified and installed pinned macOS dependencies\n'
