# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import stat
import tempfile
import unittest
from unittest import mock
import warnings
from zipfile import ZipFile, ZipInfo

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "restore_grammar_model", ROOT / "scripts/restore_grammar_model.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class GrammarRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.destination = self.root / "sources"
        self.archive = self.root / "source.zip"
        self.resources = [
            self.resource("fixture.gram", b"synthetic model"),
            self.resource("LICENSE.fixture", b"synthetic license"),
        ]
        self.model = {
            "filename": self.resources[0]["filename"],
            "size": self.resources[0]["size"], "sha256": self.resources[0]["sha256"],
            "licenseFilename": self.resources[1]["filename"],
            "licenseSize": self.resources[1]["size"],
            "licenseSha256": self.resources[1]["sha256"],
        }
        for platform in ("windows", "macos"):
            path = self.root / "platform" / platform / "dependencies.lock.json"
            path.parent.mkdir(parents=True)
            path.write_text(json.dumps({"grammarModel": self.model}))
        self.manifest = {
            "format": 1,
            "archive": {"repository": "https://example.invalid/project",
                        "release": "v1", "immutable": True, "assetId": 1,
                        "filename": "source.zip",
                        "url": "https://example.invalid/project/releases/download/v1/source.zip"},
            "resources": self.resources,
        }
        self.entries = [(item["member"], data) for item, data in zip(
            self.resources, (b"synthetic model", b"synthetic license"))]
        self.write_archive()

    @staticmethod
    def resource(name, data):
        return {"filename": name, "member": "sources/" + name,
                "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}

    def write_manifest(self):
        (self.root / "platform/grammar-model-recovery.lock.json").write_text(
            json.dumps(self.manifest))

    def write_archive(self, entries=None):
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", UserWarning)
            with ZipFile(self.archive, "w") as archive:
                for name, data in self.entries if entries is None else entries:
                    archive.writestr(name, data)
        self.manifest["archive"].update(
            size=self.archive.stat().st_size,
            sha256=hashlib.sha256(self.archive.read_bytes()).hexdigest())
        self.write_manifest()

    def restore(self):
        MODULE.restore(self.root, self.destination, self.archive)

    def assert_unpublished(self):
        for resource in self.resources:
            self.assertFalse((self.destination / resource["filename"]).exists())
        if self.destination.exists():
            self.assertEqual(list(self.destination.iterdir()), [])

    def test_real_repository_locks_match_recovery_manifest(self):
        MODULE.load_spec(ROOT)

    def test_extracts_only_exact_members_and_repeat_needs_no_network(self):
        self.write_archive(self.entries + [("../outside.txt", b"unrelated")])
        self.restore()
        self.assertEqual({path.name for path in self.destination.iterdir()},
                         {item["filename"] for item in self.resources})
        self.assertFalse((self.root / "outside.txt").exists())
        with mock.patch.object(MODULE, "urlopen") as network:
            MODULE.restore(self.root, self.destination)
        network.assert_not_called()

    def test_network_download_checks_archive_and_members(self):
        response = io.BytesIO(self.archive.read_bytes())
        response.geturl = lambda: self.manifest["archive"]["url"]
        with mock.patch.object(MODULE, "urlopen", return_value=response):
            MODULE.restore(self.root, self.destination)
        self.assertEqual((self.destination / "fixture.gram").read_bytes(), b"synthetic model")

    def test_archive_digest_drift_is_rejected_before_extraction(self):
        data = bytearray(self.archive.read_bytes())
        data[40] ^= 1
        self.archive.write_bytes(data)
        with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
            self.restore()
        self.assert_unpublished()

    def test_member_digest_drift_publishes_neither_resource(self):
        self.write_archive([self.entries[0], (self.entries[1][0], b"Synthetic license")])
        with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
            self.restore()
        self.assert_unpublished()

    def test_missing_and_duplicate_members_are_rejected(self):
        for entries in (self.entries[:1], self.entries + self.entries[:1]):
            with self.subTest(entries=len(entries)):
                self.write_archive(entries)
                with self.assertRaisesRegex(ValueError, "missing or duplicate"):
                    self.restore()
                self.assert_unpublished()

    def test_link_directory_and_wrong_size_members_are_rejected(self):
        for mode, data in ((stat.S_IFLNK, b"synthetic model"),
                           (stat.S_IFDIR, b"synthetic model"),
                           (stat.S_IFREG, b"extra bytes beyond the locked size")):
            with self.subTest(mode=mode, size=len(data)):
                info = ZipInfo(self.entries[0][0])
                info.create_system = 3
                info.external_attr = (mode | 0o600) << 16
                self.write_archive([(info, data), self.entries[1]])
                with self.assertRaisesRegex(ValueError, "unsafe or wrong-sized"):
                    self.restore()
                self.assert_unpublished()

    def test_invalid_existing_file_is_preserved_without_network(self):
        self.destination.mkdir()
        path = self.destination / "fixture.gram"
        path.write_bytes(b"user-owned invalid bytes")
        with mock.patch.object(MODULE, "urlopen") as network:
            with self.assertRaises(ValueError):
                MODULE.restore(self.root, self.destination)
        network.assert_not_called()
        self.assertEqual(path.read_bytes(), b"user-owned invalid bytes")

    def test_model_lock_drift_and_recovery_drift_are_rejected(self):
        changed = copy.deepcopy(self.model)
        changed["sha256"] = "a" * 64
        path = self.root / "platform/macos/dependencies.lock.json"
        path.write_text(json.dumps({"grammarModel": changed}))
        with self.assertRaisesRegex(ValueError, "locks differ"):
            self.restore()
        (self.root / "platform/windows/dependencies.lock.json").write_text(
            json.dumps({"grammarModel": changed}))
        with self.assertRaisesRegex(ValueError, "do not match"):
            self.restore()
        self.assert_unpublished()

    def test_download_overrun_is_bounded_and_unpublished(self):
        response = io.BytesIO(self.archive.read_bytes() + b"unlocked suffix")
        response.geturl = lambda: self.manifest["archive"]["url"]
        with mock.patch.object(MODULE, "urlopen", return_value=response):
            with self.assertRaisesRegex(ValueError, "exceeds locked size"):
                MODULE.restore(self.root, self.destination)
        self.assert_unpublished()


if __name__ == "__main__":
    unittest.main()
