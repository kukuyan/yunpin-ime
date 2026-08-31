#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import io
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
import zipfile


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

import verify_release_assets as release  # noqa: E402


TAG = "v0.1.0-preview.1"
COMMIT = "a" * 40
SYNTHETIC_MODEL_BYTES = b"fixed public grammar model fixture\n"
SYNTHETIC_LICENSE_BYTES = b"fixed public grammar license fixture\n"
SYNTHETIC_GRAMMAR_MODEL = {
    **release.locked_grammar_model(),
    "size": len(SYNTHETIC_MODEL_BYTES),
    "sha256": hashlib.sha256(SYNTHETIC_MODEL_BYTES).hexdigest(),
    "licenseSize": len(SYNTHETIC_LICENSE_BYTES),
    "licenseSha256": hashlib.sha256(SYNTHETIC_LICENSE_BYTES).hexdigest(),
}
MACOS_RUNTIME_BYTES = {
    "librime": ("Contents/Frameworks/librime.1.dylib", b"synthetic librime\n"),
    "octagram": (
        "Contents/Frameworks/rime-plugins/librime-octagram.dylib",
        b"synthetic octagram\n",
    ),
    "executable": ("Contents/MacOS/YunPin", b"synthetic YunPin executable\n"),
}


def grammar_files(kind: str) -> dict[str, bytes | str]:
    filename = str(SYNTHETIC_GRAMMAR_MODEL["filename"])
    license_filename = str(SYNTHETIC_GRAMMAR_MODEL["licenseFilename"])
    prefixes = {
        "windows-runtime": ("rime-data", "licenses"),
        "windows-source": ("sources", "sources"),
        "macos-source": ("YunPin-IME/YunPin/sources", "YunPin-IME/YunPin/sources"),
    }
    model_prefix, license_prefix = prefixes[kind]
    return {
        f"{model_prefix}/{filename}": SYNTHETIC_MODEL_BYTES,
        f"{license_prefix}/{license_filename}": SYNTHETIC_LICENSE_BYTES,
    }


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


def write_macos_payload(root: Path) -> None:
    app_root = root / "Library" / "Input Methods" / "YunPin.app"
    shared_support = app_root / "Contents" / "SharedSupport"
    shared_support.mkdir(parents=True)
    (shared_support / str(SYNTHETIC_GRAMMAR_MODEL["filename"])).write_bytes(
        SYNTHETIC_MODEL_BYTES
    )
    licenses = shared_support / "licenses"
    licenses.mkdir()
    (licenses / str(SYNTHETIC_GRAMMAR_MODEL["licenseFilename"])).write_bytes(
        SYNTHETIC_LICENSE_BYTES
    )
    for _, (relative, content) in MACOS_RUNTIME_BYTES.items():
        path = app_root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)


def synthetic_macos_grammar_evidence() -> dict[str, object]:
    baseline = {
        "initializeMicroseconds": 1000,
        "schemaSelectMicroseconds": 3000,
        "firstCompleteInputMicroseconds": 4000,
        "rssAfterInitializeBytes": 5_000_000,
        "rssAfterSchemaSelectBytes": 10_000_000,
        "rssAfterFirstInputBytes": 11_000_000,
        "rssAfterHoldoutBytes": 12_000_000,
        "measurementMaxRssBytes": 13_000_000,
        "finalKeyCandidateP95Microseconds": 500,
        "measurementProcessElapsedMicroseconds": 20_000,
    }
    model = {
        "initializeMicroseconds": 1100,
        "schemaSelectMicroseconds": 3200,
        "firstCompleteInputMicroseconds": 4300,
        "rssAfterInitializeBytes": 6_000_000,
        "rssAfterSchemaSelectBytes": 20_000_000,
        "rssAfterFirstInputBytes": 21_000_000,
        "rssAfterHoldoutBytes": 22_000_000,
        "measurementMaxRssBytes": 23_000_000,
        "finalKeyCandidateP95Microseconds": 600,
        "measurementProcessElapsedMicroseconds": 22_000,
    }
    deltas = {
        name: model[name] - baseline[name]
        for name in release.GRAMMAR_METRIC_FIELDS
    }
    return {
        "schemaVersion": 1,
        "repositoryCommit": COMMIT,
        "platform": "macos",
        "packagedArchitectures": ["arm64", "x86_64"],
        "probeArchitecture": "arm64",
        "bundleIdentifier": "io.github.kukuyan.inputmethod.YunPin",
        "grammarModel": SYNTHETIC_GRAMMAR_MODEL,
        "runtimeIdentity": {
            name: {
                "path": relative,
                "size": len(content),
                "sha256": hashlib.sha256(content).hexdigest(),
            }
            for name, (relative, content) in MACOS_RUNTIME_BYTES.items()
        },
        "qualityComparison": {
            "headlessRimeIce": True,
            "cacheCondition": "process-cold-deployed-user-data-os-warm",
            "comparisonOrder": ["baseline", "model"],
            "deploymentPhase": {
                "cacheCondition": "isolated-deployment-process-os-warm",
                "processIsolation": "separate-prepare-process",
                "baseline": {
                    "elapsedMicroseconds": 2_000_000,
                    "peakRssBytes": 20_000_000,
                },
                "model": {
                    "elapsedMicroseconds": 3_000_000,
                    "peakRssBytes": 30_000_000,
                },
            },
            "measurementPhase": {
                "processIsolation": "fresh-process-after-deployment",
                "maintenanceInvoked": False,
            },
            "holdoutCaseCount": 20,
            "acceptedQualityCases": {"baseline": 17, "model": 18},
            "gateMicroseconds": 20_000,
            "syntheticPrivateCounterfactual": True,
            "baseline": baseline,
            "model": model,
            "modelMinusBaseline": deltas,
            "loadStageEvidence": {
                "modelFileOpenObservedStage": "schema-select-before-first-input",
                "largestResidentGrowthStage": "schema-select",
                "modelMinusBaselineRssAfterInitializeBytes": 1_000_000,
                "modelMinusBaselineRssIncreaseAtSchemaSelectBytes": 9_000_000,
                "modelMinusBaselineRssIncreaseAtFirstInputBytes": 0,
                "modelMinusBaselineRssIncreaseAtHoldoutBytes": 0,
                "modelMinusBaselineSchemaSelectMicroseconds": 200,
                "firstInputLatencyDeltaMicroseconds": 300,
                "modelFirstInputExceeds20ms": False,
            },
        },
    }


def synthetic_windows_grammar_quality() -> dict[str, object]:
    quality = json.loads(
        json.dumps(synthetic_macos_grammar_evidence()["qualityComparison"])
    )
    quality["finalKeyCandidateP95Microseconds"] = quality["model"][
        "finalKeyCandidateP95Microseconds"
    ]
    quality["publicCases"] = [
        "youyuantuma",
        "youceshizhanghaoma",
        "shujukushiyongdeshinagebanben",
        "qingzaishiyici",
        "woyijingshoudaole",
    ]
    return quality


class ReleaseAssetTests(unittest.TestCase):
    def test_expanded_macos_payload_requires_one_exact_model_and_license(self) -> None:
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            release, "locked_grammar_model", return_value=SYNTHETIC_GRAMMAR_MODEL
        ):
            root = Path(directory) / "payload"
            write_macos_payload(root)
            release.verify_macos_package_payload(root)

            extra_model = root / "unexpected.gram"
            extra_model.write_bytes(SYNTHETIC_MODEL_BYTES)
            with self.assertRaises(release.VerificationError):
                release.verify_macos_package_payload(root)
            extra_model.unlink()

            model = (
                root
                / "Library"
                / "Input Methods"
                / "YunPin.app"
                / "Contents"
                / "SharedSupport"
                / str(SYNTHETIC_GRAMMAR_MODEL["filename"])
            )
            model.write_bytes(SYNTHETIC_MODEL_BYTES + b"changed")
            with self.assertRaises(release.VerificationError):
                release.verify_macos_package_payload(root)

            model.write_bytes(b"X" + SYNTHETIC_MODEL_BYTES[1:])
            with self.assertRaises(release.VerificationError):
                release.verify_macos_package_payload(root)

            model.unlink()
            model.symlink_to("licenses")
            with self.assertRaises(release.VerificationError):
                release.verify_macos_package_payload(root)

            license_root = Path(directory) / "license-payload"
            write_macos_payload(license_root)
            license_path = (
                license_root
                / "Library"
                / "Input Methods"
                / "YunPin.app"
                / "Contents"
                / "SharedSupport"
                / "licenses"
                / str(SYNTHETIC_GRAMMAR_MODEL["licenseFilename"])
            )
            license_path.write_bytes(b"X" + SYNTHETIC_LICENSE_BYTES[1:])
            with self.assertRaises(release.VerificationError):
                release.verify_macos_package_payload(license_root)

    @unittest.skipUnless(sys.platform == "darwin", "native DMG verification requires macOS")
    def test_native_macos_gate_mounts_dmg_and_expands_pkg_payload(self) -> None:
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            release, "locked_grammar_model", return_value=SYNTHETIC_GRAMMAR_MODEL
        ):
            root = Path(directory)
            payload = root / "payload"
            write_macos_payload(payload)
            package = root / release.MACOS_PACKAGE
            native_env = os.environ.copy()
            native_env["COPYFILE_DISABLE"] = "1"
            subprocess.run(
                [
                    "/usr/bin/pkgbuild",
                    "--root",
                    str(payload),
                    "--identifier",
                    "io.github.kukuyan.inputmethod.YunPin",
                    "--version",
                    "0.1.0",
                    str(package),
                ],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                env=native_env,
            )

            staging = root / "staging"
            staging.mkdir()
            for name, content in (
                (release.MACOS_PACKAGE, package.read_bytes()),
                (release.MACOS_SOURCE, b"synthetic corresponding source\n"),
                (release.MACOS_INSTRUCTIONS, b"synthetic installation instructions\n"),
            ):
                (staging / name).write_bytes(content)
            (staging / release.MACOS_DMG_MANIFEST).write_text(
                "".join(
                    f"{release.sha256_file(staging / name)}  {name}\n"
                    for name in (
                        release.MACOS_PACKAGE,
                        release.MACOS_SOURCE,
                        release.MACOS_INSTRUCTIONS,
                    )
                ),
                encoding="utf-8",
            )
            raw = root / "synthetic.raw.dmg"
            dmg = root / release.MACOS_DMG
            subprocess.run(
                [
                    "/usr/bin/hdiutil",
                    "makehybrid",
                    "-quiet",
                    "-hfs",
                    "-hfs-volume-name",
                    "YunPin Test",
                    "-o",
                    str(raw),
                    str(staging),
                ],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                env=native_env,
            )
            subprocess.run(
                [
                    "/usr/bin/hdiutil",
                    "convert",
                    "-quiet",
                    "-format",
                    "UDZO",
                    "-o",
                    str(dmg),
                    str(raw),
                ],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                env=native_env,
            )
            evidence = root / release.MACOS_GRAMMAR_EVIDENCE
            evidence.write_text(
                json.dumps(synthetic_macos_grammar_evidence()), encoding="utf-8"
            )
            release.verify_macos_installer(dmg, evidence, COMMIT)

    def test_release_scanner_requires_one_exact_locked_grammar_resource(self) -> None:
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            release, "locked_grammar_model", return_value=SYNTHETIC_GRAMMAR_MODEL
        ):
            root = Path(directory)
            valid = root / release.WINDOWS_RUNTIME
            write_zip(valid, grammar_files("windows-runtime"))
            release.scan_asset(valid)

            wrong = root / release.WINDOWS_SOURCE
            wrong_files = grammar_files("windows-source")
            model_path = next(name for name in wrong_files if name.endswith(".gram"))
            wrong_files[model_path] += b"changed"
            write_zip(wrong, wrong_files)
            with self.assertRaises(release.VerificationError):
                release.scan_asset(wrong)

            duplicate = root / release.MACOS_SOURCE
            duplicate_files = grammar_files("macos-source")
            duplicate_files["YunPin-IME/unexpected.gram"] = SYNTHETIC_MODEL_BYTES
            write_tar(duplicate, duplicate_files)
            with self.assertRaises(release.VerificationError):
                release.scan_asset(duplicate)

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

    def test_scan_locks_windows_synthetic_ranking_fixture_bytes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = (
                ROOT
                / "platform"
                / "windows"
                / "tests"
                / "fixtures"
                / "synthetic-public-ranking.tsv"
            ).read_bytes()
            member = (
                "YunPin-source/platform/windows/tests/fixtures/"
                "synthetic-public-ranking.tsv"
            )
            valid = root / "valid-windows-source.zip"
            write_zip(valid, {member: fixture})
            release.scan_asset(valid)

            changed = root / "changed-windows-source.zip"
            write_zip(changed, {member: fixture + b"unexpected-row\n"})
            with self.assertRaises(release.VerificationError):
                release.scan_asset(changed)

    def test_macos_grammar_evidence_requires_two_phase_load_stage_contract(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            release, "locked_grammar_model", return_value=SYNTHETIC_GRAMMAR_MODEL
        ):
            evidence = Path(directory) / release.MACOS_GRAMMAR_EVIDENCE
            document = synthetic_macos_grammar_evidence()
            evidence.write_text(json.dumps(document), encoding="utf-8")
            release.verify_macos_grammar_evidence(evidence, COMMIT)

            for mutate in (
                lambda item: item["qualityComparison"]["measurementPhase"].__setitem__(
                    "maintenanceInvoked", True
                ),
                lambda item: item["qualityComparison"]["deploymentPhase"][
                    "model"
                ].__setitem__("peakRssBytes", 0),
                lambda item: item["qualityComparison"]["loadStageEvidence"].__setitem__(
                    "modelFileOpenObservedStage", "first-input"
                ),
            ):
                with self.subTest(mutate=mutate):
                    broken = json.loads(json.dumps(document))
                    mutate(broken)
                    evidence.write_text(json.dumps(broken), encoding="utf-8")
                    with self.assertRaises(release.VerificationError):
                        release.verify_macos_grammar_evidence(evidence, COMMIT)

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
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            release, "locked_grammar_model", return_value=SYNTHETIC_GRAMMAR_MODEL
        ):
            root = Path(directory)
            runtime = root / release.WINDOWS_RUNTIME
            source = root / release.WINDOWS_SOURCE
            runtime_files = grammar_files("windows-runtime")
            runtime_files["YunPin/BUILD-METADATA.json"] = json.dumps(
                {
                    "repositoryCommit": COMMIT,
                    "signed": False,
                    "productionReady": False,
                    "grammarModel": SYNTHETIC_GRAMMAR_MODEL,
                    "grammarQuality": synthetic_windows_grammar_quality(),
                }
            )
            write_zip(
                runtime,
                runtime_files,
            )
            source_files = grammar_files("windows-source")
            source_files["YunPin-source/BUILD-SOURCE-METADATA.json"] = json.dumps(
                {
                    "repositoryCommit": COMMIT,
                    "grammarModel": SYNTHETIC_GRAMMAR_MODEL,
                }
            )
            write_zip(
                source,
                source_files,
            )
            release.verify_windows_commit(runtime, source, COMMIT)
            with self.assertRaises(release.VerificationError):
                release.verify_windows_commit(runtime, source, "b" * 40)

            broken_quality = synthetic_windows_grammar_quality()
            broken_quality["measurementPhase"]["maintenanceInvoked"] = True
            runtime_files["YunPin/BUILD-METADATA.json"] = json.dumps(
                {
                    "repositoryCommit": COMMIT,
                    "signed": False,
                    "productionReady": False,
                    "grammarModel": SYNTHETIC_GRAMMAR_MODEL,
                    "grammarQuality": broken_quality,
                }
            )
            write_zip(runtime, runtime_files)
            with self.assertRaises(release.VerificationError):
                release.verify_windows_commit(runtime, source, COMMIT)

    def test_finalize_creates_exact_checksum_covered_asset_set(self) -> None:
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            release, "locked_grammar_model", return_value=SYNTHETIC_GRAMMAR_MODEL
        ):
            root = Path(directory)
            windows = root / "windows"
            macos = root / "macos"
            output = root / "dist"
            windows.mkdir()
            macos.mkdir()

            runtime = windows / release.WINDOWS_RUNTIME
            windows_source = windows / release.WINDOWS_SOURCE
            runtime_files = grammar_files("windows-runtime")
            runtime_files["README.txt"] = b"unsigned preview"
            runtime_files["YunPin/BUILD-METADATA.json"] = json.dumps(
                {
                    "repositoryCommit": COMMIT,
                    "signed": False,
                    "productionReady": False,
                    "grammarModel": SYNTHETIC_GRAMMAR_MODEL,
                    "grammarQuality": synthetic_windows_grammar_quality(),
                }
            )
            write_zip(runtime, runtime_files)
            source_files = grammar_files("windows-source")
            source_files["README.txt"] = b"source"
            source_files["YunPin-source/BUILD-SOURCE-METADATA.json"] = json.dumps(
                {
                    "repositoryCommit": COMMIT,
                    "grammarModel": SYNTHETIC_GRAMMAR_MODEL,
                }
            )
            write_zip(windows_source, source_files)
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
            macos_source_files = grammar_files("macos-source")
            macos_source_files["YunPin-IME/README.md"] = b"source"
            write_tar(macos_source, macos_source_files)
            release.write_job_metadata(
                "macos",
                TAG,
                COMMIT,
                [dmg, macos_source],
                macos / release.MACOS_METADATA,
            )
            (macos / release.MACOS_GRAMMAR_EVIDENCE).write_text(
                json.dumps(synthetic_macos_grammar_evidence()), encoding="utf-8"
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
                release.MACOS_GRAMMAR_EVIDENCE,
                sbom.name,
                release.RELEASE_METADATA,
                release.RELEASE_CHECKSUMS,
                release.RELEASE_NOTES,
            }
            self.assertEqual(expected_files, {path.name for path in output.iterdir()})

            checksum_rows = (output / release.RELEASE_CHECKSUMS).read_text(
                encoding="utf-8"
            ).splitlines()
            self.assertEqual(7, len(checksum_rows))
            self.assertNotIn(release.RELEASE_CHECKSUMS, "\n".join(checksum_rows))
            metadata = json.loads(
                (output / release.RELEASE_METADATA).read_text(encoding="utf-8")
            )
            self.assertFalse(metadata["signed"])
            self.assertEqual(COMMIT, metadata["repositoryCommit"])
            self.assertEqual(SYNTHETIC_GRAMMAR_MODEL, metadata["grammarModel"])
            self.assertEqual(6, len(metadata["assets"]))
            evidence_row = next(
                row
                for row in metadata["assets"]
                if row["name"] == release.MACOS_GRAMMAR_EVIDENCE
            )
            self.assertEqual("quality-performance-evidence", evidence_row["role"])
            self.assertIn(
                release.MACOS_GRAMMAR_EVIDENCE,
                (output / release.RELEASE_NOTES).read_text(encoding="utf-8"),
            )

    def test_remote_assets_require_uploaded_state_size_and_digest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            assets = root / "assets"
            assets.mkdir()
            for index in range(8):
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
