#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos

build_root="${YUNPIN_MACOS_BUILD_ROOT:-${REPO_ROOT}/build/macos}"
output_dir="$build_root/package"
package_name="YunPin-IME-development-preview.pkg"
source_name="YunPin-IME-development-preview-source.tar.gz"
instructions_name="安装说明.txt"
manifest_name="SHA256SUMS-macOS.txt"
dmg_name="YunPin-IME-macOS-development-preview.dmg"
dmg_checksum_name="YunPin-IME-macOS-development-preview.sha256"
package="$output_dir/$package_name"
source_archive="$output_dir/$source_name"
instructions="${MACOS_DIR}/package/$instructions_name"
dmg="$output_dir/$dmg_name"
dmg_checksum="$output_dir/$dmg_checksum_name"
volume_name="YunPin IME Preview"
hdiutil_bin="${YUNPIN_HDIUTIL:-/usr/bin/hdiutil}"

[[ -x "$hdiutil_bin" ]] || die "hdiutil is unavailable"
for input in "$package" "$source_archive" "$instructions"; do
  [[ -f "$input" && ! -L "$input" ]] ||
    die "DMG input must be a regular non-symbolic file: $input"
done

source_date_epoch="${SOURCE_DATE_EPOCH:-}"
if [[ -z "$source_date_epoch" ]]; then
  source_date_epoch="$(git -C "$REPO_ROOT" show -s --format=%ct HEAD)" ||
    die "unable to derive SOURCE_DATE_EPOCH from the release commit"
fi
[[ "$source_date_epoch" =~ ^[0-9]+$ ]] ||
  die "SOURCE_DATE_EPOCH must be a non-negative integer"

mkdir -p "$output_dir"
work="$(mktemp -d "$build_root/.dmg-build.XXXXXX")"
staging="$work/staging"
mount_dir="$work/mount"
raw_image="$work/YunPin-IME-macOS-development-preview.raw.dmg"
candidate_dmg="$work/$dmg_name"
candidate_checksum="$work/$dmg_checksum_name"
attached=false
cleanup_dmg_build() {
  if [[ "$attached" == true ]]; then
    "$hdiutil_bin" detach "$mount_dir" >/dev/null 2>&1 || true
  fi
  /bin/rm -rf -- "$work"
}
trap cleanup_dmg_build EXIT
mkdir -p "$staging" "$mount_dir"

/usr/bin/install -m 0644 "$package" "$staging/$package_name"
/usr/bin/install -m 0644 "$source_archive" "$staging/$source_name"
/usr/bin/install -m 0644 "$instructions" "$staging/$instructions_name"
(
  cd "$staging"
  /usr/bin/shasum -a 256 \
    "$package_name" "$source_name" "$instructions_name" >"$manifest_name"
)
chmod 0644 "$staging/$manifest_name"

# Normalize every staged timestamp to the source commit.  The HFS image helper
# below also normalizes volume-header dates and identifiers, while the UDIF
# helper replaces hdiutil's random segment identifier.  Together they make a
# repeated build from identical inputs byte-for-byte reproducible.
/usr/bin/python3 - "$staging" "$source_date_epoch" <<'PY'
import os
from pathlib import Path
import sys

root = Path(sys.argv[1])
epoch = int(sys.argv[2])
if epoch < 0:
    raise SystemExit("SOURCE_DATE_EPOCH must be non-negative")
for path in sorted(root.rglob("*"), reverse=True):
    if path.is_symlink():
        raise SystemExit(f"symbolic links are forbidden in DMG staging: {path.name}")
    os.utime(path, (epoch, epoch), follow_symlinks=False)
os.utime(root, (epoch, epoch), follow_symlinks=False)
PY

/usr/bin/python3 - "$staging" "$package_name" "$source_name" \
  "$instructions_name" "$manifest_name" <<'PY'
from pathlib import Path
import sys

root = Path(sys.argv[1])
expected = set(sys.argv[2:])
actual = {path.name for path in root.iterdir()}
if actual != expected:
    raise SystemExit(
        "DMG staging allowlist mismatch: "
        f"expected={sorted(expected)!r} actual={sorted(actual)!r}"
    )
if any(not path.is_file() or path.is_symlink() for path in root.iterdir()):
    raise SystemExit("DMG staging may contain regular files only")
PY

image_seed="$(/usr/bin/shasum -a 256 "$staging/$manifest_name" | /usr/bin/awk '{print $1}')"

normalize_hfs_image() {
  /usr/bin/python3 - "$1" "$source_date_epoch" "$image_seed" <<'PY'
import hashlib
import mmap
from pathlib import Path
import struct
import sys

path = Path(sys.argv[1])
unix_epoch = int(sys.argv[2])
seed = bytes.fromhex(sys.argv[3])
hfs_epoch = unix_epoch + 2_082_844_800
if not 0 <= hfs_epoch <= 0xFFFFFFFF:
    raise SystemExit("SOURCE_DATE_EPOCH is outside the HFS+ timestamp range")

with path.open("r+b") as stream:
    with mmap.mmap(stream.fileno(), 0) as image:
        candidates = []
        offset = image.find(b"H+\x00\x04")
        while offset >= 0:
            if offset + 112 <= len(image):
                block_size, total_blocks = struct.unpack_from(">II", image, offset + 40)
                volume_size = block_size * total_blocks
                alternate = offset + volume_size - 2048
                if (
                    block_size >= 512
                    and block_size <= 65536
                    and block_size & (block_size - 1) == 0
                    and volume_size >= 2048
                    and alternate > offset
                    and alternate + 112 <= len(image)
                    and image[alternate : alternate + 4] == b"H+\x00\x04"
                ):
                    candidates.append((offset, alternate))
            offset = image.find(b"H+\x00\x04", offset + 1)

        unique = sorted(set(candidates))
        if len(unique) != 1:
            raise SystemExit(
                f"expected one HFS+ primary/alternate header pair, found {len(unique)}"
            )
        volume_id = hashlib.sha256(b"YunPin-HFS-volume\0" + seed).digest()[:8]
        primary, alternate = unique[0]
        for header in (primary, alternate):
            struct.pack_into(">IIII", image, header + 16, *([hfs_epoch] * 4))
            image[header + 104 : header + 112] = volume_id

        # DiscRecording copies source ctime into Catalog attribute dates, and
        # ctime cannot be set by SOURCE_DATE_EPOCH.  Parse the catalog B-tree
        # rather than replacing timestamp-looking bytes globally (which could
        # corrupt the embedded package or source archive).
        volume_start = primary - 1024
        block_size = struct.unpack_from(">I", image, primary + 40)[0]
        catalog_fork = primary + 272
        catalog_size, _, catalog_blocks = struct.unpack_from(">QII", image, catalog_fork)
        extents = [
            struct.unpack_from(">II", image, catalog_fork + 16 + index * 8)
            for index in range(8)
        ]
        used_extents = [(start, count) for start, count in extents if count]
        if (
            len(used_extents) != 1
            or used_extents[0][1] < catalog_blocks
            or catalog_size <= 0
        ):
            raise SystemExit("unsupported fragmented HFS+ catalog layout")
        catalog_start = volume_start + used_extents[0][0] * block_size
        if catalog_start < volume_start or catalog_start + catalog_size > len(image):
            raise SystemExit("HFS+ catalog lies outside the image")
        if struct.unpack_from(">b", image, catalog_start + 8)[0] != 1:
            raise SystemExit("HFS+ catalog header node is missing")
        node_size = struct.unpack_from(">H", image, catalog_start + 14 + 18)[0]
        if (
            node_size < 512
            or node_size & (node_size - 1)
            or catalog_size % node_size
        ):
            raise SystemExit("invalid HFS+ catalog node size")

        dated_records = 0
        for node_offset in range(0, catalog_size, node_size):
            node = catalog_start + node_offset
            kind = struct.unpack_from(">b", image, node + 8)[0]
            record_count = struct.unpack_from(">H", image, node + 10)[0]
            if kind != -1:
                continue
            offsets = [
                struct.unpack_from(">H", image, node + node_size - 2 * (index + 1))[0]
                for index in range(record_count + 1)
            ]
            if offsets != sorted(offsets) or offsets[0] < 14 or offsets[-1] > node_size:
                raise SystemExit("invalid HFS+ catalog leaf offsets")
            for index in range(record_count):
                record = node + offsets[index]
                record_end = node + offsets[index + 1]
                key_length = struct.unpack_from(">H", image, record)[0]
                data = record + 2 + key_length
                if data + 2 > record_end:
                    raise SystemExit("truncated HFS+ catalog record")
                record_type = struct.unpack_from(">H", image, data)[0]
                if record_type in (1, 2):
                    if data + 32 > record_end:
                        raise SystemExit("truncated HFS+ dated catalog record")
                    struct.pack_into(">IIIII", image, data + 12, *([hfs_epoch] * 5))
                    dated_records += 1
        if dated_records == 0:
            raise SystemExit("HFS+ catalog contains no file or folder records")
        image.flush()
PY
}

normalize_udif_segment_id() {
  /usr/bin/python3 - "$1" "$image_seed" <<'PY'
import hashlib
import mmap
from pathlib import Path
import sys

path = Path(sys.argv[1])
seed = bytes.fromhex(sys.argv[2])
with path.open("r+b") as stream:
    with mmap.mmap(stream.fileno(), 0) as image:
        trailer = len(image) - 512
        if trailer < 0 or image[trailer : trailer + 4] != b"koly":
            raise SystemExit("compressed image is missing its UDIF trailer")
        image[trailer + 64 : trailer + 80] = hashlib.sha256(
            b"YunPin-UDIF-segment\0" + seed
        ).digest()[:16]
        image.flush()
PY
}

"$hdiutil_bin" makehybrid -quiet -hfs -hfs-volume-name "$volume_name" \
  -o "$raw_image" "$staging"
normalize_hfs_image "$raw_image"
"$hdiutil_bin" convert -quiet -format UDZO -imagekey zlib-level=9 \
  -o "$candidate_dmg" "$raw_image"
normalize_udif_segment_id "$candidate_dmg"
"$hdiutil_bin" verify "$candidate_dmg" >/dev/null

"$hdiutil_bin" attach -readonly -nobrowse -noautoopen \
  -mountpoint "$mount_dir" "$candidate_dmg" >/dev/null
attached=true
readonly_volume="$(/usr/sbin/diskutil info -plist "$mount_dir" | \
  /usr/bin/plutil -extract WritableVolume raw -o - -)"
[[ "$readonly_volume" == false ]] || die "DMG verification mount is writable"

/usr/bin/python3 - "$mount_dir" "$package_name" "$source_name" \
  "$instructions_name" "$manifest_name" <<'PY'
from pathlib import Path
import sys

root = Path(sys.argv[1])
expected = set(sys.argv[2:])
actual = {path.name for path in root.iterdir()}
if actual != expected:
    raise SystemExit(
        "mounted DMG allowlist mismatch: "
        f"expected={sorted(expected)!r} actual={sorted(actual)!r}"
    )
if any(not path.is_file() or path.is_symlink() for path in root.iterdir()):
    raise SystemExit("mounted DMG may contain regular files only")
PY
for file in "$package_name" "$source_name" "$instructions_name" "$manifest_name"; do
  /usr/bin/cmp "$staging/$file" "$mount_dir/$file"
done
(
  cd "$mount_dir"
  /usr/bin/shasum -a 256 -c "$manifest_name" >/dev/null
)
"$hdiutil_bin" detach "$mount_dir" >/dev/null
attached=false
printf 'verified read-only DMG contents: %s\n' "$candidate_dmg"

/usr/bin/shasum -a 256 "$candidate_dmg" | \
  /usr/bin/awk -v name="$dmg_name" '{print $1 "  " name}' >"$candidate_checksum"
chmod 0644 "$candidate_dmg" "$candidate_checksum"
/bin/mv -f "$candidate_dmg" "$dmg"
/bin/mv -f "$candidate_checksum" "$dmg_checksum"
printf 'created unsigned, unnotarized development-preview DMG: %s\n' "$dmg"
/bin/cat "$dmg_checksum"
