#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Restore the locked model/license from a verified immutable source archive."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import tempfile
from urllib.parse import urlparse
from urllib.request import Request, urlopen
from zipfile import ZipFile

ROOT = Path(__file__).resolve().parents[1]
CHUNK = 1024 * 1024


def load_spec(root: Path) -> dict:
    models = [json.loads((root / "platform" / platform / "dependencies.lock.json")
                        .read_text(encoding="utf-8"))["grammarModel"]
              for platform in ("windows", "macos")]
    if models[0] != models[1]:
        raise ValueError("desktop grammarModel locks differ")
    model = models[0]
    spec = json.loads((root / "platform/grammar-model-recovery.lock.json")
                      .read_text(encoding="utf-8"))
    expected = [
        {"filename": model["filename"], "member": "sources/" + model["filename"],
         "size": model["size"], "sha256": model["sha256"]},
        {"filename": model["licenseFilename"],
         "member": "sources/" + model["licenseFilename"],
         "size": model["licenseSize"], "sha256": model["licenseSha256"]},
    ]
    if spec["format"] != 1 or spec["resources"] != expected:
        raise ValueError("recovery members do not match the desktop locks")
    archive = spec["archive"]
    expected_url = (archive["repository"] + "/releases/download/" +
                    archive["release"] + "/" + archive["filename"])
    if (archive["immutable"] is not True or archive["assetId"] <= 0 or
            archive["url"] != expected_url or
            urlparse(archive["url"]).scheme != "https"):
        raise ValueError("recovery archive must bind an immutable HTTPS release")
    for resource in [archive, *expected]:
        name = resource["filename"]
        if (not name or name in {".", ".."} or
                any(char in name for char in ("/", "\\", ":")) or
                not isinstance(resource["size"], int) or resource["size"] <= 0 or
                not re.fullmatch(r"[0-9a-f]{64}", resource["sha256"])):
            raise ValueError("invalid recovery filename, size, or SHA-256")
    if expected[0]["filename"] == expected[1]["filename"]:
        raise ValueError("model and license must have different filenames")
    return spec


def verify_file(path: Path, resource: dict) -> None:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size != resource["size"]:
        raise ValueError(f"not a regular file of the locked size: {path.name}")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(CHUNK), b""):
            digest.update(chunk)
    if digest.hexdigest() != resource["sha256"]:
        raise ValueError(f"SHA-256 mismatch: {path.name}")


def copy_checked(stream, target: Path, resource: dict) -> None:
    """Bound both network downloads and ZIP member reads to the locked size."""
    digest = hashlib.sha256()
    size = 0
    with target.open("xb") as output:
        while True:
            chunk = stream.read(min(CHUNK, resource["size"] - size + 1))
            if not chunk:
                break
            size += len(chunk)
            if size > resource["size"]:
                raise ValueError(f"resource exceeds locked size: {target.name}")
            digest.update(chunk)
            output.write(chunk)
    if size != resource["size"] or digest.hexdigest() != resource["sha256"]:
        raise ValueError(f"size or SHA-256 mismatch: {target.name}")


def extract_members(archive: Path, staging: Path, resources: list) -> None:
    with ZipFile(archive) as source:
        for resource in resources:
            matches = [entry for entry in source.infolist()
                       if entry.filename == resource["member"]]
            if len(matches) != 1:
                raise ValueError(f"missing or duplicate member: {resource['member']}")
            entry = matches[0]
            kind = stat.S_IFMT(entry.external_attr >> 16)
            if (entry.is_dir() or kind not in (0, stat.S_IFREG) or
                    entry.flag_bits & 1 or entry.file_size != resource["size"]):
                raise ValueError(f"unsafe or wrong-sized member: {entry.filename}")
            with source.open(entry) as stream:
                copy_checked(stream, staging / resource["filename"], resource)


def restore(root: Path, destination: Path, archive: Path | None = None) -> None:
    spec = load_spec(root)
    resources = spec["resources"]
    if destination.is_symlink() or (destination.exists() and not destination.is_dir()):
        raise ValueError("resource destination must be a real directory")
    present = []
    for resource in resources:
        path = destination / resource["filename"]
        exists = path.exists() or path.is_symlink()
        if exists:
            verify_file(path, resource)
        present.append(exists)
    if all(present):
        return
    destination.mkdir(parents=True, exist_ok=True)
    # Stage on the destination filesystem, then publish with no-overwrite links.
    # Both members are verified before either destination becomes visible.
    with tempfile.TemporaryDirectory(prefix=".grammar-recovery-", dir=destination) as temp:
        staging = Path(temp)
        if archive is None:
            archive = staging / "source.zip"
            request = Request(spec["archive"]["url"],
                              headers={"User-Agent": "YunPin-locked-model-recovery"})
            with urlopen(request, timeout=60) as response:
                if urlparse(response.geturl()).scheme != "https":
                    raise ValueError("recovery download redirected outside HTTPS")
                copy_checked(response, archive, spec["archive"])
        else:
            verify_file(archive, spec["archive"])
        extract_members(archive, staging, resources)
        for resource in resources:
            path = destination / resource["filename"]
            try:
                os.link(staging / resource["filename"], path)
            except FileExistsError:
                verify_file(path, resource)
    for resource in resources:
        verify_file(destination / resource["filename"], resource)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, help="use a previously downloaded locked ZIP")
    parser.add_argument("--destination", type=Path, default=ROOT / "sources")
    args = parser.parse_args()
    restore(ROOT, args.destination, args.archive)
    print("Verified locked grammar model and license in " + str(args.destination))


if __name__ == "__main__":
    main()
