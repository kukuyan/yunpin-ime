#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

import verify_release_assets as release  # noqa: E402


TAG = "v0.1.0-preview.1"
COMMIT = "a" * 40


def write_zip(path: Path, files: dict[str, bytes | str]) -> None:
    with zipfile.ZipFile(path, mode="w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name, content in files.items():
            if isinstance(content, str):
                content = content.encode("utf-8")
            archive.writestr(name, content)


def write_tar(path: Path, files: dict[str, bytes | str]) -> None:
    with tarfile.open(path, mode="w:gz") as archive:
        for name, content in files.items():
            if isinstance(content, str):
                content = content.encode("utf-8")
            info = tarfile.TarInfo(name)
            info.size = len(content)
            info.mtime = 0
            archive.addfile(info, io.BytesIO(content))


class ReleaseAssetTests(unittest.TestCase):
    def test_scan_rejects_private_snapshot_and_traversal(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private = root / "private.zip"
            write_zip(private, {"YunPin/private.tsv": "secret\tphrase\n"})
            with self.assertRaises(release.VerificationError):
                release.scan_asset(private)

            traversal = root / "traversal.zip"
            write_zip(traversal, {"../outside.txt": "no"})
            with self.assertRaises(release.VerificationError):
                release.scan_asset(traversal)

    def test_scan_allows_synthetic_private_fixture_only_at_reviewed_path(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "source.zip"
            fixture = (
                ROOT / "platform" / "macos" / "tests" / "fixtures" / "private.tsv"
            ).read_bytes()
            write_zip(
                archive,
                {
                    "YunPin/platform/macos/tests/fixtures/private.tsv": fixture,
                },
            )
            release.scan_asset(archive)

            changed = root / "changed-fixture.zip"
            write_zip(
                changed,
                {
                    "YunPin/platform/macos/tests/fixtures/private.tsv":
                        fixture + b"unexpected-personal-row\n",
                },
            )
            with self.assertRaises(release.VerificationError):
                release.scan_asset(changed)

    def test_scan_does_not_trust_squirrel_or_third_party_directories(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for filename, content in (
                ("YunPin/Squirrel/private-user-dictionary.scel", b"private"),
                ("YunPin/third_party/device-private-key.pem", b"private"),
                (
                    "YunPin/third_party/README.md",
                    b"gh" + b"p_" + b"123456789012345678901234567890123456",
                ),
                (
                    "YunPin/Squirrel/librime/deps/boost-1.89.0/libs/beast/"
                    "example/common/server_certificate.hpp",
                    b"gh" + b"p_" + b"123456789012345678901234567890123456",
                ),
                ("YunPin/rime-data/lua/lunar.db", b"not-the-locked-public-table"),
            ):
                with self.subTest(filename=filename):
                    archive = root / (Path(filename).name + ".zip")
                    write_zip(archive, {filename: content})
                    with self.assertRaises(release.VerificationError):
                        release.scan_asset(archive)

    def test_windows_metadata_must_bind_exact_commit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = root / release.WINDOWS_RUNTIME
            source = root / release.WINDOWS_SOURCE
            write_zip(
                runtime,
                {
                    "YunPin/BUILD-METADATA.json": json.dumps(
                        {
                            "repositoryCommit": COMMIT,
                            "signed": False,
                            "productionReady": False,
                        }
                    )
                },
            )
            write_zip(
                source,
                {
                    "YunPin-source/BUILD-SOURCE-METADATA.json": json.dumps(
                        {"repositoryCommit": COMMIT}
                    )
                },
            )
            release.verify_windows_commit(runtime, source, COMMIT)
            with self.assertRaises(release.VerificationError):
                release.verify_windows_commit(runtime, source, "b" * 40)

    def test_finalize_creates_exact_checksum_covered_asset_set(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            windows = root / "windows"
            macos = root / "macos"
            output = root / "dist"
            windows.mkdir()
            macos.mkdir()

            runtime = windows / release.WINDOWS_RUNTIME
            windows_source = windows / release.WINDOWS_SOURCE
            write_zip(runtime, {"YunPin/README.txt": "unsigned preview"})
            write_zip(windows_source, {"YunPin-source/README.txt": "source"})
            windows_assets = [runtime, windows_source]
            (windows / release.WINDOWS_CHECKSUMS).write_text(
                "".join(
                    f"{release.sha256_file(asset)}  {asset.name}\n"
                    for asset in windows_assets
                ),
                encoding="utf-8",
            )
            release.write_job_metadata(
                "windows",
                TAG,
                COMMIT,
                windows_assets,
                windows / release.WINDOWS_METADATA,
            )

            dmg = macos / release.MACOS_DMG
            dmg.write_bytes(b"synthetic-dmg-for-static-test")
            macos_source = macos / release.MACOS_SOURCE
            write_tar(macos_source, {"YunPin/README.md": "source"})
            release.write_job_metadata(
                "macos",
                TAG,
                COMMIT,
                [dmg, macos_source],
                macos / release.MACOS_METADATA,
            )

            sbom = root / f"YunPin-IME-{TAG}.spdx.json"
            sbom.write_text(
                json.dumps(
                    {
                        "spdxVersion": "SPDX-2.3",
                        "dataLicense": "CC0-1.0",
                        "documentNamespace": f"https://example.invalid/{TAG}/{COMMIT}",
                    }
                ),
                encoding="utf-8",
            )

            release.finalize_release(TAG, COMMIT, windows, macos, sbom, output)
            expected_files = {
                release.WINDOWS_RUNTIME,
                release.WINDOWS_SOURCE,
                release.MACOS_DMG,
                release.MACOS_SOURCE,
                sbom.name,
                release.RELEASE_METADATA,
                release.RELEASE_CHECKSUMS,
                release.RELEASE_NOTES,
            }
            self.assertEqual(expected_files, {path.name for path in output.iterdir()})

            checksum_rows = (output / release.RELEASE_CHECKSUMS).read_text(
                encoding="utf-8"
            ).splitlines()
            self.assertEqual(6, len(checksum_rows))
            self.assertNotIn(release.RELEASE_CHECKSUMS, "\n".join(checksum_rows))
            metadata = json.loads(
                (output / release.RELEASE_METADATA).read_text(encoding="utf-8")
            )
            self.assertFalse(metadata["signed"])
            self.assertEqual(COMMIT, metadata["repositoryCommit"])
            self.assertEqual(5, len(metadata["assets"]))

    def test_remote_assets_require_uploaded_state_size_and_digest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            assets = root / "assets"
            assets.mkdir()
            for index in range(7):
                (assets / f"asset-{index}.bin").write_bytes(
                    f"release-{index}\n".encode("utf-8")
                )
            (assets / release.RELEASE_NOTES).write_text(
                "not uploaded\n", encoding="utf-8"
            )
            response = root / "remote.json"

            rows = [
                {
                    "name": path.name,
                    "state": "uploaded",
                    "size": path.stat().st_size,
                    "digest": f"sha256:{release.sha256_file(path)}",
                }
                for path in sorted(assets.glob("asset-*.bin"))
            ]
            response.write_text(json.dumps(rows), encoding="utf-8")
            release.verify_remote_assets(response, assets)

            for field, value in (
                ("state", "starter"),
                ("size", 0),
                ("digest", "sha256:" + "0" * 64),
            ):
                with self.subTest(field=field):
                    broken = json.loads(json.dumps(rows))
                    broken[0][field] = value
                    response.write_text(json.dumps(broken), encoding="utf-8")
                    with self.assertRaises(release.VerificationError):
                        release.verify_remote_assets(response, assets)


if __name__ == "__main__":
    unittest.main()
