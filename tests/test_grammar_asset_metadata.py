#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

import copy
import importlib.util
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "verify_grammar_asset_metadata",
    ROOT / "scripts" / "verify_grammar_asset_metadata.py",
)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class GrammarAssetMetadataTests(unittest.TestCase):
    def setUp(self) -> None:
        self.model = {
            "repository": "https://github.com/amzxyz/RIME-LMDG",
            "release": "LTS",
            "immutable": False,
            "filename": "wanxiang-lts-zh-hans.gram",
            "assetId": 536587145,
            "assetUpdatedAt": "2026-08-30T12:25:59Z",
            "tagRef": "c78463a521aee2681db6cd6424a75a9b413237a3",
            "url": (
                "https://github.com/amzxyz/RIME-LMDG/releases/download/"
                "LTS/wanxiang-lts-zh-hans.gram"
            ),
            "size": 420248620,
        }
        self.lock = {"grammarModel": self.model}
        self.release = {
            "tag_name": "LTS",
            "assets": [
                {
                    "id": 536587145,
                    "name": "wanxiang-lts-zh-hans.gram",
                    "updated_at": "2026-08-30T12:25:59Z",
                    "size": 420248620,
                    "state": "uploaded",
                    "content_type": "application/octet-stream",
                    "browser_download_url": self.model["url"],
                    "url": (
                        "https://api.github.com/repos/amzxyz/RIME-LMDG/"
                        "releases/assets/536587145"
                    ),
                }
            ],
        }
        self.tag = {
            "ref": "refs/tags/LTS",
            "object": {"type": "commit", "sha": self.model["tagRef"]},
        }

    def test_accepts_exact_mutable_asset_identity(self) -> None:
        MODULE.verify_metadata(self.lock, self.release, self.tag)

    def test_rejects_each_remote_identity_drift(self) -> None:
        cases = (
            ("id", 1),
            ("name", "other.gram"),
            ("updated_at", "2026-08-31T00:00:00Z"),
            ("size", 1),
            ("state", "new"),
            ("content_type", "text/plain"),
            ("browser_download_url", "https://example.invalid/model.gram"),
            ("url", "https://api.github.com/repos/example/assets/1"),
        )
        for field, value in cases:
            with self.subTest(field=field):
                release = copy.deepcopy(self.release)
                release["assets"][0][field] = value
                with self.assertRaises(MODULE.MetadataError):
                    MODULE.verify_metadata(self.lock, release, self.tag)

    def test_rejects_tag_ref_drift(self) -> None:
        tag = copy.deepcopy(self.tag)
        tag["object"]["sha"] = "0" * 40
        with self.assertRaises(MODULE.MetadataError):
            MODULE.verify_metadata(self.lock, self.release, tag)

    def test_rejects_duplicate_named_asset(self) -> None:
        release = copy.deepcopy(self.release)
        release["assets"].append(copy.deepcopy(release["assets"][0]))
        with self.assertRaises(MODULE.MetadataError):
            MODULE.verify_metadata(self.lock, release, self.tag)


if __name__ == "__main__":
    unittest.main()
