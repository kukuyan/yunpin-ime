#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"

source_dir="${1:-${REPO_ROOT}/build/macos/squirrel}"
cache_dir="${YUNPIN_MACOS_CACHE_DIR:-${REPO_ROOT}/build/macos/downloads}"
[[ -f "$source_dir/.yunpin-base-commit" ]] || die "run prepare-source.sh before fetching dependencies"
mkdir -p "$cache_dir" "$source_dir/download"

temporary_downloads=()
cleanup_temporary_downloads() {
  local path
  for path in "${temporary_downloads[@]-}"; do
    [[ -n "$path" ]] || continue
    [[ -e "$path" || -L "$path" ]] || continue
    /bin/rm -f -- "$path"
  done
}
trap cleanup_temporary_downloads EXIT

verify_online_grammar_asset_metadata() {
  local metadata_dir
  metadata_dir="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-grammar-metadata.XXXXXX")"
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2022-11-28' \
    --output "$metadata_dir/release.json" \
    'https://api.github.com/repos/amzxyz/RIME-LMDG/releases/tags/LTS'
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2022-11-28' \
    --output "$metadata_dir/tag.json" \
    'https://api.github.com/repos/amzxyz/RIME-LMDG/git/ref/tags/LTS'
  /usr/bin/python3 "$REPO_ROOT/scripts/verify_grammar_asset_metadata.py" \
    --lock "$MACOS_DIR/dependencies.lock.json" \
    --release-json "$metadata_dir/release.json" \
    --tag-json "$metadata_dir/tag.json" ||
    die "mutable grammar release metadata differs from the dependency lock"
  /bin/rm -rf -- "$metadata_dir"
}

fetch_grammar_resource() {
  local filename="$1"
  local url="$2"
  local expected_size="$3"
  local expected_sha256="$4"
  local label="$5"
  local verify_release_metadata="${6:-false}"
  local destination="$cache_dir/$filename"
  local bundled="$REPO_ROOT/sources/$filename"
  local partial

  if [[ -e "$destination" || -L "$destination" ]]; then
    verify_locked_grammar_resource \
      "$destination" "$expected_size" "$expected_sha256" "$label"
    return 0
  fi
  partial="$(/usr/bin/mktemp "$cache_dir/.${filename}.part.XXXXXX")" ||
    die "could not create a private temporary file for $label"
  temporary_downloads+=("$partial")
  if [[ -e "$bundled" || -L "$bundled" ]]; then
    verify_locked_grammar_resource \
      "$bundled" "$expected_size" "$expected_sha256" "$label"
    /usr/bin/install -m 600 "$bundled" "$partial"
  else
    if [[ "$verify_release_metadata" == true ]]; then
      verify_online_grammar_asset_metadata
    fi
    /usr/bin/curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
      --output "$partial" "$url"
    verify_locked_grammar_resource \
      "$partial" "$expected_size" "$expected_sha256" "$label"
  fi
  verify_locked_grammar_resource \
    "$partial" "$expected_size" "$expected_sha256" "$label"
  /bin/ln "$partial" "$destination" ||
    die "$label cache destination appeared while staging: $destination"
  /bin/rm -f -- "$partial"
  verify_locked_grammar_resource \
    "$destination" "$expected_size" "$expected_sha256" "$label"
}

fetch_grammar_resource \
  "$(read_lock_value grammarModel.filename)" \
  "$(read_lock_value grammarModel.url)" \
  "$(read_lock_value grammarModel.size)" \
  "$(read_lock_value grammarModel.sha256)" \
  "grammar model" \
  true
fetch_grammar_resource \
  "$(read_lock_value grammarModel.licenseFilename)" \
  "$(read_lock_value grammarModel.licenseUrl)" \
  "$(read_lock_value grammarModel.licenseSize)" \
  "$(read_lock_value grammarModel.licenseSha256)" \
  "grammar model license"

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
while IFS=$'\t' read -r name plugin commit license_sha256; do
  plugin_dir="$source_dir/librime/plugins/$plugin"
  marker="$plugin_dir/.yunpin-source-commit"
  if [[ -d "$plugin_dir" ]]; then
    [[ -f "$marker" && "$(<"$marker")" == "$commit" ]] || die "unexpected existing Rime plugin source: $plugin_dir"
    continue
  fi
  temporary="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-rime-plugin.XXXXXX")"
  tar -xzf "$cache_dir/$name" -C "$temporary" --strip-components 1
  [[ -f "$temporary/CMakeLists.txt" && -f "$temporary/LICENSE" ]] || die "Rime plugin source archive is incomplete: $name"
  if [[ -n "$license_sha256" ]]; then
    actual_license_sha256="$(shasum -a 256 "$temporary/LICENSE" | awk '{print $1}')"
    [[ "$actual_license_sha256" == "$license_sha256" ]] || die "Rime plugin license SHA-256 mismatch: $name"
  fi
  if [[ "$plugin" == octagram ]]; then
    grep -F 'u <<= 7;' "$temporary/src/gram_encoding.cc" >/dev/null ||
      die "librime-octagram source lacks the locked multi-byte encoder fix"
  fi
  printf '%s\n' "$commit" > "$temporary/.yunpin-source-commit"
  mkdir -p "$(dirname "$plugin_dir")"
  mv "$temporary" "$plugin_dir"
done < <(/usr/bin/python3 - "$MACOS_DIR/dependencies.lock.json" <<'PY'
import json
import sys

for archive in json.load(open(sys.argv[1], encoding="utf-8"))["archives"]:
    if "rime_plugin" in archive:
        print(
            archive["name"],
            archive["rime_plugin"],
            archive["commit"],
            archive.get("license_sha256", ""),
            sep="\t",
        )
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

grammar_model="$(resolve_locked_grammar_resource model)"
grammar_model_filename="$(read_lock_value grammarModel.filename)"
mkdir -p "$source_dir/data/plum"
staged_grammar_model="$source_dir/data/plum/$grammar_model_filename"
[[ ! -L "$staged_grammar_model" ]] ||
  die "locked grammar model staging path must not be a link"
find "$source_dir/data/plum" -maxdepth 1 \( -type f -o -type l \) -name '*.gram' \
  ! -name "$grammar_model_filename" -print -quit | grep -q . &&
  die "unexpected extra grammar model in Squirrel data staging"
/usr/bin/install -m 644 "$grammar_model" \
  "$staged_grammar_model"
verify_locked_grammar_resource \
  "$staged_grammar_model" \
  "$(read_lock_value grammarModel.size)" \
  "$(read_lock_value grammarModel.sha256)" \
  "staged grammar model"

touch "$source_dir/.yunpin-dependencies-ready"
printf 'verified and installed pinned macOS dependencies\n'
