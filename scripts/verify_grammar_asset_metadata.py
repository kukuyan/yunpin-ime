#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Fail closed when the mutable Wanxiang LTS release asset identity drifts."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
from typing import Any


class MetadataError(RuntimeError):
    """Raised when GitHub release metadata differs from the dependency lock."""


def _read_object(path: Path, label: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise MetadataError(f"{label} is unreadable") from error
    if not isinstance(value, dict):
        raise MetadataError(f"{label} must be a JSON object")
    return value


def verify_metadata(
    lock: dict[str, Any],
    release: dict[str, Any],
    tag_ref: dict[str, Any],
) -> None:
    try:
        model = lock["grammarModel"]
    except (KeyError, TypeError) as error:
        raise MetadataError("dependency lock lacks grammarModel") from error
    if not isinstance(model, dict):
        raise MetadataError("grammarModel must be an object")
    repository = model.get("repository")
    release_name = model.get("release")
    filename = model.get("filename")
    asset_id = model.get("assetId")
    if (
        repository != "https://github.com/amzxyz/RIME-LMDG"
        or release_name != "LTS"
        or model.get("immutable") is not False
        or not isinstance(filename, str)
        or not isinstance(asset_id, int)
    ):
        raise MetadataError("grammarModel is not the reviewed mutable Wanxiang LTS asset")

    if release.get("tag_name") != release_name:
        raise MetadataError("GitHub release tag differs from the lock")
    assets = release.get("assets")
    if not isinstance(assets, list):
        raise MetadataError("GitHub release assets are missing")
    matches = [
        asset
        for asset in assets
        if isinstance(asset, dict) and asset.get("name") == filename
    ]
    if len(matches) != 1:
        raise MetadataError("GitHub release must contain one exact locked model asset")
    asset = matches[0]
    expected_asset_url = (
        "https://api.github.com/repos/amzxyz/RIME-LMDG/releases/assets/"
        f"{asset_id}"
    )
    expected = {
        "id": asset_id,
        "name": filename,
        "updated_at": model.get("assetUpdatedAt"),
        "size": model.get("size"),
        "state": "uploaded",
        "content_type": "application/octet-stream",
        "browser_download_url": model.get("url"),
        "url": expected_asset_url,
    }
    for field, expected_value in expected.items():
        if asset.get(field) != expected_value:
            raise MetadataError(f"GitHub model asset {field} differs from the lock")

    expected_ref = f"refs/tags/{release_name}"
    target = tag_ref.get("object")
    if (
        tag_ref.get("ref") != expected_ref
        or not isinstance(target, dict)
        or target.get("type") != "commit"
        or target.get("sha") != model.get("tagRef")
    ):
        raise MetadataError("GitHub LTS tag ref differs from the lock")


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lock", required=True, type=Path)
    parser.add_argument("--release-json", required=True, type=Path)
    parser.add_argument("--tag-json", required=True, type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    arguments = _parser().parse_args(argv)
    try:
        lock = _read_object(arguments.lock, "dependency lock")
        release = _read_object(arguments.release_json, "GitHub release metadata")
        tag_ref = _read_object(arguments.tag_json, "GitHub tag metadata")
        verify_metadata(lock, release, tag_ref)
    except MetadataError as error:
        print(f"grammar asset metadata verification failed: {error}", file=sys.stderr)
        return 1
    print("mutable grammar asset metadata matches id, tag, timestamp, size, and URL")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
