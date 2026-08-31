#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Verify and assemble YunPin unsigned-preview release assets.

The release workflow uses this module at three boundaries:

* scan each platform archive before it leaves the build runner;
* bind every platform artifact to the immutable tag commit;
* assemble one exact, checksum-covered release directory.

DMG integrity and the package payload are checked with native Apple tools on
the macOS build runner.  A Linux runner cannot inspect that filesystem image
without adding another unlocked parser, so cross-platform release assembly
treats the already-verified DMG as opaque and binds its bytes to the macOS job
metadata instead.  All ZIP and tar source/runtime archives are opened and
inspected here.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import plistlib
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from typing import BinaryIO
import zipfile


ROOT = Path(__file__).resolve().parents[1]
WINDOWS_LOCK = ROOT / "platform" / "windows" / "dependencies.lock.json"
MACOS_LOCK = ROOT / "platform" / "macos" / "dependencies.lock.json"
TAG_PATTERN = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$")
COMMIT_PATTERN = re.compile(r"^[0-9a-f]{40}$")
SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
CHECKSUM_ROW = re.compile(r"^([0-9a-f]{64})  ([^/\\]+)$")
SECRET_PATTERN = re.compile(
    rb"(?:-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----"
    rb"|gh[pousr]_[A-Za-z0-9]{20,}"
    rb"|sk-(?:(?:proj|svcacct)-[A-Za-z0-9_-]{20,}|[A-Za-z0-9]{20,})"
    rb"|AKIA[0-9A-Z]{16}"
    rb"|xox[baprs]-[A-Za-z0-9-]{10,})"
)
FORBIDDEN_EXPORT = re.compile(
    r"(?i)(?:^|/)(?:conversations\.json|chat\.html|"
    r".*\.yunpinreplay|.*replay.*\.(?:jsonl|ndjson|sqlite|sqlite3|db)(?:-[^/]*)?|"
    r".*\.(?:bin|scel|sgpybin|sqlite|sqlite3|db|dpapi|p12|pfx|"
    r"mobileprovision|pem|key))$"
)
PRIVATE_SNAPSHOT = re.compile(
    r"(?i)(?:(?:^|/)(?:yunpin/)?private\.tsv$"
    r"|(?:^|/)private/imports?(?:/|$)"
    r"|(?:^|/)(?:r0w[-_]?sogou|sogou[-_]?personal|personal[-_]?dictionary)[^/]*)"
)
TEXT_SUFFIXES = {
    ".c", ".cc", ".cmake", ".cpp", ".css", ".go", ".h", ".hpp",
    ".html", ".ini", ".json", ".md", ".plist", ".ps1", ".py",
    ".rb", ".sh", ".swift", ".toml", ".ts", ".tsv", ".txt",
    ".xml", ".yaml", ".yml",
}
MAX_SECRET_SCAN_BYTES = 8 * 1024 * 1024

# These reviewed upstream source fixtures intentionally use database/key-like
# filenames or contain example key/token byte patterns.  They are accepted only
# at their exact locked source paths and only when their bytes match.  A path
# match alone is never sufficient.
ALLOWED_LOCKED_FIXTURE_SHA256 = {
    "/platform/macos/tests/fixtures/private.tsv":
        "250d4335a7b8ced9232d227da248f62aa0e1f232b3157ebb4696169fa0734733",
    "/platform/windows/tests/fixtures/synthetic-public-ranking.tsv":
        "250d4335a7b8ced9232d227da248f62aa0e1f232b3157ebb4696169fa0734733",
    "/third_party/rime-ice/lua/lunar.db":
        "30e66ebc3c7223397f2d98368e159ae6d636571056bbe6885f1fcafad56ad1c9",
    "/rime-data/lua/lunar.db":
        "30e66ebc3c7223397f2d98368e159ae6d636571056bbe6885f1fcafad56ad1c9",
    "/squirrel/librime/deps/boost-1.89.0/libs/mysql/tools/ssl/ca-cert.pem":
        "346167da10687f94409be18499dae473b81c56b22352670d84442dff3203cb41",
    "/squirrel/librime/deps/boost-1.89.0/libs/mysql/tools/ssl/server-key.pem":
        "c425c08cd3a62f368e7ed81322d122b73e03c6766735ec69ac6fd90fe42f7e7c",
    "/squirrel/librime/deps/boost-1.89.0/libs/mysql/tools/ssl/server-cert.pem":
        "034e1f11cf4261a9e21f0fdd6271f73b1f10902f30115a73f526026139f54b9c",
    "/squirrel/librime/deps/boost-1.89.0/libs/asio/example/cpp11/ssl/dh4096.pem":
        "5bd49a25e2e8a1c11d042a0e58f4ffc794d0b2a56d6dfc82bc9123cddad5f29c",
    "/squirrel/librime/deps/boost-1.89.0/libs/asio/example/cpp11/ssl/server.pem":
        "a6070c979ae317e3db6c9d72129697a3f1d0f76cdb17b31b11c2a322dc593c4e",
    "/squirrel/librime/deps/boost-1.89.0/libs/asio/example/cpp11/ssl/ca.pem":
        "dfc17a138ce665a7d2f4aae16751ebaff00967f4f85ff1fb20bf1e95a87a2b2a",
    "/squirrel/librime/deps/boost-1.89.0/libs/redis/tools/docker/tls/server.key":
        "76b8b9add6b3799d72d944ca8f7eac97e6fa31341937eaec32fc8c0bc6ab8e87",
    "/squirrel/librime/deps/boost-1.89.0/libs/redis/tools/docker/tls/ca.key":
        "5ff4585209d5f8920bfb637238e0220368a1a08fa90fcae04c11404b00832d85",
    "/squirrel/librime/deps/boost-1.89.0/libs/mysql/test/unit/test/sansio/handshake/handshake_csha2p_keys.hpp":
        "3d0ba3e70f9606b6df3b16527b1776a673cabe323ac7a1e1069ccafba27c6fd4",
    "/squirrel/librime/deps/boost-1.89.0/libs/beast/example/common/server_certificate.hpp":
        "38da7e73494f7cdfe9e4852b3475a06a2748403fea2a5d555833f6a1c2766c7e",
    "/squirrel/librime/deps/boost-1.89.0/libs/beast/test/beast/zlib/fixtures/cve_2018_25032/fixed.hpp":
        "d0d636f6b31935bb598f971fc6f918342dea8f60da0a0feb8e2876275ee9f7b4",
    "/squirrel/librime/deps/boost-1.89.0/libs/beast/test/beast/zlib/fixtures/cve_2018_25032/default.hpp":
        "d89bb5a0221d78ddca8735614550aeff04d034617182f112b7d3c03f54c29022",
}

WINDOWS_RUNTIME = "YunPin-IME-Windows-development-preview.zip"
WINDOWS_SOURCE = "YunPin-IME-Windows-development-preview-source.zip"
WINDOWS_CHECKSUMS = "SHA256SUMS"
WINDOWS_METADATA = "windows-release-metadata.json"
MACOS_DMG = "YunPin-IME-macOS-development-preview.dmg"
MACOS_PACKAGE = "YunPin-IME-development-preview.pkg"
MACOS_SOURCE = "YunPin-IME-development-preview-source.tar.gz"
MACOS_INSTRUCTIONS = "安装说明.txt"
MACOS_DMG_MANIFEST = "SHA256SUMS-macOS.txt"
MACOS_METADATA = "macos-release-metadata.json"
MACOS_GRAMMAR_EVIDENCE = "grammar-quality-metrics.json"
RELEASE_METADATA = "RELEASE-METADATA.json"
RELEASE_CHECKSUMS = "SHA256SUMS"
RELEASE_NOTES = "RELEASE-NOTES.md"


class VerificationError(RuntimeError):
    """Raised when an artifact cannot be safely released."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _validate_tag_commit(tag: str, commit: str) -> None:
    if not TAG_PATTERN.fullmatch(tag):
        raise VerificationError(f"invalid preview tag: {tag}")
    if not COMMIT_PATTERN.fullmatch(commit):
        raise VerificationError(f"invalid repository commit: {commit}")


def locked_grammar_model() -> dict[str, object]:
    try:
        windows = json.loads(WINDOWS_LOCK.read_text(encoding="utf-8"))["grammarModel"]
        macos = json.loads(MACOS_LOCK.read_text(encoding="utf-8"))["grammarModel"]
    except (OSError, KeyError, TypeError, json.JSONDecodeError) as error:
        raise VerificationError("cannot read shared grammar model lock") from error
    if not isinstance(windows, dict) or windows != macos:
        raise VerificationError("macOS and Windows grammar model locks disagree")
    model_filename = windows.get("filename")
    license_filename = windows.get("licenseFilename")
    if (
        not isinstance(model_filename, str)
        or PurePosixPath(model_filename).name != model_filename
        or not model_filename.endswith(".gram")
        or not isinstance(license_filename, str)
        or PurePosixPath(license_filename).name != license_filename
        or not license_filename
        or not SHA256_PATTERN.fullmatch(str(windows.get("sha256", "")))
        or not isinstance(windows.get("size"), int)
        or int(windows["size"]) <= 0
        or not SHA256_PATTERN.fullmatch(str(windows.get("licenseSha256", "")))
        or not isinstance(windows.get("licenseSize"), int)
        or int(windows["licenseSize"]) <= 0
    ):
        raise VerificationError("shared grammar model lock lacks exact bytes")
    return windows


GRAMMAR_METRIC_FIELDS = {
    "initializeMicroseconds",
    "schemaSelectMicroseconds",
    "firstCompleteInputMicroseconds",
    "rssAfterInitializeBytes",
    "rssAfterSchemaSelectBytes",
    "rssAfterFirstInputBytes",
    "rssAfterHoldoutBytes",
    "measurementMaxRssBytes",
    "finalKeyCandidateP95Microseconds",
    "measurementProcessElapsedMicroseconds",
}
GRAMMAR_DEPLOYMENT_METRIC_FIELDS = {"elapsedMicroseconds", "peakRssBytes"}
GRAMMAR_LOAD_STAGE_FIELDS = {
    "modelFileOpenObservedStage",
    "largestResidentGrowthStage",
    "modelMinusBaselineRssAfterInitializeBytes",
    "modelMinusBaselineRssIncreaseAtSchemaSelectBytes",
    "modelMinusBaselineRssIncreaseAtFirstInputBytes",
    "modelMinusBaselineRssIncreaseAtHoldoutBytes",
    "modelMinusBaselineSchemaSelectMicroseconds",
    "firstInputLatencyDeltaMicroseconds",
    "modelFirstInputExceeds20ms",
}


def verify_grammar_quality_contract(
    quality: object,
    platform_label: str,
) -> dict[str, object]:
    """Recompute the shared two-phase grammar A/B evidence contract."""

    if not isinstance(quality, dict) or set(quality) != {
        "headlessRimeIce",
        "cacheCondition",
        "comparisonOrder",
        "deploymentPhase",
        "measurementPhase",
        "holdoutCaseCount",
        "acceptedQualityCases",
        "gateMicroseconds",
        "syntheticPrivateCounterfactual",
        "baseline",
        "model",
        "modelMinusBaseline",
        "loadStageEvidence",
    }:
        raise VerificationError(f"{platform_label} grammar A/B evidence fields differ")
    if (
        quality.get("headlessRimeIce") is not True
        or quality.get("cacheCondition")
        != "process-cold-deployed-user-data-os-warm"
        or quality.get("comparisonOrder") != ["baseline", "model"]
        or quality.get("holdoutCaseCount") != 20
        or quality.get("acceptedQualityCases") != {"baseline": 17, "model": 18}
        or quality.get("gateMicroseconds") != 20000
        or quality.get("syntheticPrivateCounterfactual") is not True
    ):
        raise VerificationError(f"{platform_label} grammar A/B evidence contract differs")
    deployment = quality.get("deploymentPhase")
    if not isinstance(deployment, dict) or set(deployment) != {
        "cacheCondition",
        "processIsolation",
        "baseline",
        "model",
    }:
        raise VerificationError(
            f"{platform_label} grammar deployment evidence fields differ"
        )
    if (
        deployment.get("cacheCondition")
        != "isolated-deployment-process-os-warm"
        or deployment.get("processIsolation") != "separate-prepare-process"
    ):
        raise VerificationError(
            f"{platform_label} grammar deployment evidence contract differs"
        )
    for mode in ("baseline", "model"):
        metrics = deployment.get(mode)
        if (
            not isinstance(metrics, dict)
            or set(metrics) != GRAMMAR_DEPLOYMENT_METRIC_FIELDS
            or any(
                not isinstance(value, int)
                or isinstance(value, bool)
                or value <= 0
                for value in metrics.values()
            )
        ):
            raise VerificationError(
                f"{platform_label} {mode} deployment metrics are malformed"
            )
    if quality.get("measurementPhase") != {
        "processIsolation": "fresh-process-after-deployment",
        "maintenanceInvoked": False,
    }:
        raise VerificationError(
            f"{platform_label} grammar measurement isolation differs"
        )
    for mode in ("baseline", "model"):
        metrics = quality.get(mode)
        if not isinstance(metrics, dict) or set(metrics) != GRAMMAR_METRIC_FIELDS:
            raise VerificationError(
                f"{platform_label} {mode} grammar metrics are incomplete"
            )
        if any(
            not isinstance(value, int) or isinstance(value, bool) or value <= 0
            for value in metrics.values()
        ):
            raise VerificationError(
                f"{platform_label} {mode} grammar metrics are malformed"
            )
        if metrics["finalKeyCandidateP95Microseconds"] > 20000:
            raise VerificationError(
                f"{platform_label} {mode} grammar P95 exceeds 20 ms"
            )
    expected_deltas = {
        name: quality["model"][name] - quality["baseline"][name]
        for name in GRAMMAR_METRIC_FIELDS
    }
    if quality.get("modelMinusBaseline") != expected_deltas:
        raise VerificationError(
            f"{platform_label} grammar A/B deltas are inconsistent"
        )
    stage_deltas = {
        "initialize": expected_deltas["rssAfterInitializeBytes"],
        "schema-select": (
            expected_deltas["rssAfterSchemaSelectBytes"]
            - expected_deltas["rssAfterInitializeBytes"]
        ),
        "first-input": (
            expected_deltas["rssAfterFirstInputBytes"]
            - expected_deltas["rssAfterSchemaSelectBytes"]
        ),
        "holdout": (
            expected_deltas["rssAfterHoldoutBytes"]
            - expected_deltas["rssAfterFirstInputBytes"]
        ),
    }
    largest_resident_growth_stage = max(stage_deltas, key=stage_deltas.get)
    expected_load_evidence = {
        "modelFileOpenObservedStage": "schema-select-before-first-input",
        "largestResidentGrowthStage": largest_resident_growth_stage,
        "modelMinusBaselineRssAfterInitializeBytes": stage_deltas["initialize"],
        "modelMinusBaselineRssIncreaseAtSchemaSelectBytes": stage_deltas[
            "schema-select"
        ],
        "modelMinusBaselineRssIncreaseAtFirstInputBytes": stage_deltas[
            "first-input"
        ],
        "modelMinusBaselineRssIncreaseAtHoldoutBytes": stage_deltas["holdout"],
        "modelMinusBaselineSchemaSelectMicroseconds": expected_deltas[
            "schemaSelectMicroseconds"
        ],
        "firstInputLatencyDeltaMicroseconds": expected_deltas[
            "firstCompleteInputMicroseconds"
        ],
        "modelFirstInputExceeds20ms": (
            quality["model"]["firstCompleteInputMicroseconds"] > 20000
        ),
    }
    load_evidence = quality.get("loadStageEvidence")
    if (
        not isinstance(load_evidence, dict)
        or set(load_evidence) != GRAMMAR_LOAD_STAGE_FIELDS
        or load_evidence != expected_load_evidence
        or stage_deltas[largest_resident_growth_stage] <= 0
    ):
        raise VerificationError(
            f"{platform_label} grammar load-stage evidence is inconsistent"
        )
    return quality


def verify_macos_grammar_evidence(
    evidence: Path,
    commit: str,
    app_root: Path | None = None,
) -> dict[str, object]:
    """Validate external A/B metrics and optionally bind them to an app payload."""

    if not COMMIT_PATTERN.fullmatch(commit):
        raise VerificationError(f"invalid repository commit: {commit}")
    try:
        document = json.loads(evidence.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise VerificationError("invalid macOS grammar quality evidence") from error
    if not isinstance(document, dict):
        raise VerificationError("macOS grammar quality evidence must be an object")
    if set(document) != {
        "schemaVersion",
        "repositoryCommit",
        "platform",
        "packagedArchitectures",
        "probeArchitecture",
        "bundleIdentifier",
        "grammarModel",
        "runtimeIdentity",
        "qualityComparison",
    }:
        raise VerificationError("macOS grammar quality evidence fields differ")
    if (
        document.get("schemaVersion") != 1
        or document.get("repositoryCommit") != commit
        or document.get("platform") != "macos"
        or document.get("packagedArchitectures") != ["arm64", "x86_64"]
        or document.get("probeArchitecture") not in {"arm64", "x86_64"}
        or document.get("bundleIdentifier")
        != "io.github.kukuyan.inputmethod.YunPin"
        or document.get("grammarModel") != locked_grammar_model()
    ):
        raise VerificationError("macOS grammar evidence identity differs")

    quality = document.get("qualityComparison")
    verify_grammar_quality_contract(quality, "macOS")

    identities = document.get("runtimeIdentity")
    expected_paths = {
        "librime": "Contents/Frameworks/librime.1.dylib",
        "octagram": "Contents/Frameworks/rime-plugins/librime-octagram.dylib",
        "executable": "Contents/MacOS/YunPin",
    }
    if not isinstance(identities, dict) or set(identities) != set(expected_paths):
        raise VerificationError("macOS grammar runtime identities are incomplete")
    for name, expected_path in expected_paths.items():
        identity = identities[name]
        if (
            not isinstance(identity, dict)
            or set(identity) != {"path", "size", "sha256"}
            or identity.get("path") != expected_path
            or not isinstance(identity.get("size"), int)
            or identity["size"] <= 0
            or not SHA256_PATTERN.fullmatch(str(identity.get("sha256", "")))
        ):
            raise VerificationError(f"macOS grammar runtime identity is invalid: {name}")
        if app_root is not None:
            _verify_exact_payload_file(
                app_root,
                expected_path,
                int(identity["size"]),
                str(identity["sha256"]),
                f"macOS grammar evidence runtime {name}",
            )
    return document


def _locked_fixture_sha256(name: str) -> str | None:
    normalized = f"/{name.lower().lstrip('/')}"
    for suffix, expected in ALLOWED_LOCKED_FIXTURE_SHA256.items():
        if normalized.endswith(suffix):
            return expected
    return None


def _check_member_name(name: str, archive: Path) -> str:
    normalized = name.replace("\\", "/")
    while normalized.startswith("./"):
        normalized = normalized[2:]
    path = PurePosixPath(normalized)
    if not normalized or path.is_absolute() or ".." in path.parts:
        raise VerificationError(f"unsafe member path in {archive.name}: {name}")
    if FORBIDDEN_EXPORT.search(normalized) and _locked_fixture_sha256(normalized) is None:
        raise VerificationError(f"forbidden private/export member in {archive.name}: {name}")
    if PRIVATE_SNAPSHOT.search(normalized) and _locked_fixture_sha256(normalized) is None:
        raise VerificationError(f"private snapshot member in {archive.name}: {name}")
    return normalized


def _scan_member_bytes(name: str, content: bytes, archive: Path) -> None:
    expected_fixture = _locked_fixture_sha256(name)
    if expected_fixture is not None:
        if hashlib.sha256(content).hexdigest() != expected_fixture:
            raise VerificationError(
                f"locked public fixture bytes changed in {archive.name}: {name}"
            )
        return
    suffix = Path(name).suffix.lower()
    if suffix not in TEXT_SUFFIXES or len(content) > MAX_SECRET_SCAN_BYTES:
        return
    if SECRET_PATTERN.search(content):
        raise VerificationError(f"credential-like content in {archive.name}: {name}")


def _grammar_archive_layout(path: Path) -> dict[str, object] | None:
    model = locked_grammar_model()
    filename = str(model["filename"])
    license_filename = str(model["licenseFilename"])
    paths = {
        WINDOWS_RUNTIME: (
            f"rime-data/{filename}",
            f"licenses/{license_filename}",
        ),
        WINDOWS_SOURCE: (
            f"sources/{filename}",
            f"sources/{license_filename}",
        ),
        MACOS_SOURCE: (
            f"YunPin-IME/YunPin/sources/{filename}",
            f"YunPin-IME/YunPin/sources/{license_filename}",
        ),
    }
    expected = paths.get(path.name)
    if expected is None:
        return None
    return {
        "model_path": expected[0],
        "model_size": int(model["size"]),
        "model_sha256": str(model["sha256"]),
        "license_path": expected[1],
        "license_size": int(model["licenseSize"]),
        "license_sha256": str(model["licenseSha256"]),
    }


def _sha256_stream(stream: BinaryIO) -> str:
    digest = hashlib.sha256()
    while True:
        chunk = stream.read(1024 * 1024)
        if not chunk:
            return digest.hexdigest()
        digest.update(chunk)


def _scan_locked_grammar_resource(
    *,
    name: str,
    size: int,
    stream: BinaryIO,
    archive: Path,
    layout: dict[str, object],
    kind: str,
) -> None:
    expected_path = str(layout[f"{kind}_path"])
    expected_size = int(layout[f"{kind}_size"])
    expected_sha256 = str(layout[f"{kind}_sha256"])
    if name != expected_path:
        raise VerificationError(
            f"locked grammar {kind} is at the wrong path in {archive.name}: {name}"
        )
    if size != expected_size:
        raise VerificationError(
            f"locked grammar {kind} has the wrong size in {archive.name}"
        )
    if _sha256_stream(stream) != expected_sha256:
        raise VerificationError(
            f"locked grammar {kind} has the wrong SHA-256 in {archive.name}"
        )


def _finish_grammar_archive_scan(
    path: Path,
    layout: dict[str, object] | None,
    model_count: int,
    license_count: int,
) -> None:
    if layout is not None and (model_count != 1 or license_count != 1):
        raise VerificationError(
            f"{path.name} must contain exactly one locked grammar model and license"
        )


def _scan_zip(path: Path) -> None:
    layout = _grammar_archive_layout(path)
    model_count = 0
    license_count = 0
    try:
        with zipfile.ZipFile(path) as archive:
            corrupt = archive.testzip()
            if corrupt is not None:
                raise VerificationError(f"corrupt ZIP member in {path.name}: {corrupt}")
            for member in archive.infolist():
                if member.filename.replace("\\", "/") in {".", "./"}:
                    continue
                name = _check_member_name(member.filename, path)
                basename = PurePosixPath(name).name
                is_model = name.lower().endswith(".gram")
                is_license = layout is not None and basename == PurePosixPath(
                    str(layout["license_path"])
                ).name
                if is_model or is_license:
                    if layout is None or member.is_dir() or (
                        (member.external_attr >> 16) & 0o170000
                    ) == 0o120000:
                        raise VerificationError(
                            f"unexpected or linked grammar resource in {path.name}: {name}"
                        )
                    kind = "model" if is_model else "license"
                    with archive.open(member, "r") as stream:
                        _scan_locked_grammar_resource(
                            name=name,
                            size=member.file_size,
                            stream=stream,
                            archive=path,
                            layout=layout,
                            kind=kind,
                        )
                    if kind == "model":
                        model_count += 1
                    else:
                        license_count += 1
                    continue
                if member.is_dir() or (
                    member.file_size > MAX_SECRET_SCAN_BYTES
                    and _locked_fixture_sha256(name) is None
                ):
                    continue
                _scan_member_bytes(name, archive.read(member), path)
            _finish_grammar_archive_scan(path, layout, model_count, license_count)
    except zipfile.BadZipFile as error:
        raise VerificationError(f"invalid ZIP archive: {path}") from error


def _scan_tar(path: Path) -> None:
    layout = _grammar_archive_layout(path)
    model_count = 0
    license_count = 0
    try:
        with tarfile.open(path, mode="r:*") as archive:
            for member in archive:
                if member.name.replace("\\", "/") in {".", "./"}:
                    continue
                name = _check_member_name(member.name, path)
                basename = PurePosixPath(name).name
                is_model = name.lower().endswith(".gram")
                is_license = layout is not None and basename == PurePosixPath(
                    str(layout["license_path"])
                ).name
                if is_model or is_license:
                    if layout is None or not member.isfile():
                        raise VerificationError(
                            f"unexpected or linked grammar resource in {path.name}: {name}"
                        )
                    stream = archive.extractfile(member)
                    if stream is None:
                        raise VerificationError(
                            f"cannot read grammar resource in {path.name}: {name}"
                        )
                    with stream:
                        _scan_locked_grammar_resource(
                            name=name,
                            size=member.size,
                            stream=stream,
                            archive=path,
                            layout=layout,
                            kind="model" if is_model else "license",
                        )
                    if is_model:
                        model_count += 1
                    else:
                        license_count += 1
                    continue
                if member.issym() or member.islnk():
                    link = PurePosixPath(member.linkname)
                    if link.is_absolute() or ".." in link.parts:
                        raise VerificationError(
                            f"unsafe archive link in {path.name}: {member.name} -> {member.linkname}"
                        )
                if not member.isfile() or (
                    member.size > MAX_SECRET_SCAN_BYTES
                    and _locked_fixture_sha256(name) is None
                ):
                    continue
                stream = archive.extractfile(member)
                if stream is not None:
                    _scan_member_bytes(name, stream.read(), path)
            _finish_grammar_archive_scan(path, layout, model_count, license_count)
    except (tarfile.TarError, OSError) as error:
        raise VerificationError(f"invalid tar archive: {path}") from error


def _run_native_tool(command: list[str], description: str) -> subprocess.CompletedProcess[bytes]:
    try:
        return subprocess.run(
            command,
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except FileNotFoundError as error:
        raise VerificationError(f"{description} tool is unavailable: {command[0]}") from error
    except subprocess.CalledProcessError as error:
        detail = error.stderr.decode("utf-8", errors="replace").strip()
        if detail:
            detail = detail.splitlines()[-1][:500]
            raise VerificationError(f"{description} failed: {detail}") from error
        raise VerificationError(f"{description} failed") from error


def _normalized_payload_member(raw: str) -> str:
    if "\\" in raw or "\x00" in raw:
        raise VerificationError(f"unsafe macOS package payload path: {raw!r}")
    normalized = raw
    while normalized.startswith("./"):
        normalized = normalized[2:]
    if normalized in {"", "."}:
        return "."
    path = PurePosixPath(normalized)
    if path.is_absolute() or ".." in path.parts:
        raise VerificationError(f"unsafe macOS package payload path: {raw!r}")
    return path.as_posix()


def _expected_macos_payload_paths() -> tuple[str, str]:
    model = locked_grammar_model()
    shared_support = "Library/Input Methods/YunPin.app/Contents/SharedSupport"
    return (
        f"{shared_support}/{model['filename']}",
        f"{shared_support}/licenses/{model['licenseFilename']}",
    )


def _verify_macos_payload_listing(package: Path) -> None:
    result = _run_native_tool(
        ["/usr/sbin/pkgutil", "--payload-files", str(package)],
        "macOS package payload listing",
    )
    try:
        members = [
            _normalized_payload_member(row)
            for row in result.stdout.decode("utf-8").splitlines()
        ]
    except UnicodeDecodeError as error:
        raise VerificationError("macOS package payload paths are not UTF-8") from error
    expected_model, expected_license = _expected_macos_payload_paths()
    expected_model_sidecar = str(
        PurePosixPath(expected_model).parent
        / f"._{PurePosixPath(expected_model).name}"
    )
    expected_license_sidecar = str(
        PurePosixPath(expected_license).parent
        / f"._{PurePosixPath(expected_license).name}"
    )
    models = [
        name
        for name in members
        if name.lower().endswith(".gram")
        and not PurePosixPath(name).name.startswith("._")
    ]
    model_sidecars = [
        name
        for name in members
        if name.lower().endswith(".gram")
        and PurePosixPath(name).name.startswith("._")
    ]
    licenses = [
        name
        for name in members
        if PurePosixPath(name).name == PurePosixPath(expected_license).name
    ]
    license_sidecars = [
        name
        for name in members
        if PurePosixPath(name).name
        == f"._{PurePosixPath(expected_license).name}"
    ]
    if models != [expected_model]:
        raise VerificationError(
            "macOS package payload must list exactly one locked grammar model at "
            f"{expected_model}"
        )
    if model_sidecars not in ([], [expected_model_sidecar]):
        raise VerificationError("macOS package payload has an unexpected grammar sidecar")
    if licenses != [expected_license]:
        raise VerificationError(
            "macOS package payload must list exactly one locked grammar license at "
            f"{expected_license}"
        )
    if license_sidecars not in ([], [expected_license_sidecar]):
        raise VerificationError("macOS package payload has an unexpected grammar license sidecar")


def _payload_entries(root: Path) -> list[Path]:
    entries: list[Path] = []
    for directory, names, files in os.walk(root, followlinks=False):
        current = Path(directory)
        entries.extend(current / name for name in names)
        entries.extend(current / name for name in files)
    return entries


def _verify_exact_payload_file(
    root: Path,
    relative: str,
    expected_size: int,
    expected_sha256: str,
    label: str,
) -> None:
    path = root / PurePosixPath(relative)
    cursor = path
    try:
        while cursor != root:
            mode = cursor.lstat().st_mode
            if stat.S_ISLNK(mode):
                raise VerificationError(f"{label} path contains a symbolic link: {relative}")
            cursor = cursor.parent
    except FileNotFoundError as error:
        raise VerificationError(f"{label} is missing from macOS package: {relative}") from error
    if not stat.S_ISREG(path.lstat().st_mode):
        raise VerificationError(f"{label} is not a regular file: {relative}")
    if path.stat().st_size != expected_size:
        raise VerificationError(f"{label} has the wrong size in macOS package")
    try:
        digest = sha256_file(path)
    except OSError as error:
        raise VerificationError(f"cannot hash {label} in macOS package") from error
    if digest != expected_sha256:
        raise VerificationError(f"{label} has the wrong SHA-256 in macOS package")


def verify_macos_package_payload(
    root: Path,
    evidence: Path | None = None,
    commit: str | None = None,
) -> None:
    """Verify locked grammar bytes in an already-expanded component payload."""

    if root.is_symlink() or not root.is_dir():
        raise VerificationError("expanded macOS package payload is missing")
    model = locked_grammar_model()
    expected_model, expected_license = _expected_macos_payload_paths()
    entries = _payload_entries(root)
    model_entries = [
        path.relative_to(root).as_posix()
        for path in entries
        if path.name.lower().endswith(".gram")
    ]
    license_entries = [
        path.relative_to(root).as_posix()
        for path in entries
        if path.name == str(model["licenseFilename"])
    ]
    if model_entries != [expected_model]:
        raise VerificationError(
            "expanded macOS package must contain exactly one locked grammar model "
            f"at {expected_model}"
        )
    if license_entries != [expected_license]:
        raise VerificationError(
            "expanded macOS package must contain exactly one locked grammar license "
            f"at {expected_license}"
        )
    _verify_exact_payload_file(
        root,
        expected_model,
        int(model["size"]),
        str(model["sha256"]),
        "locked grammar model",
    )
    _verify_exact_payload_file(
        root,
        expected_license,
        int(model["licenseSize"]),
        str(model["licenseSha256"]),
        "locked grammar model license",
    )
    if evidence is not None:
        if commit is None:
            raise VerificationError("macOS grammar evidence requires a commit")
        app_root = root / "Library/Input Methods/YunPin.app"
        verify_macos_grammar_evidence(evidence, commit, app_root)


def _verify_macos_dmg_manifest(root: Path) -> None:
    manifest = root / MACOS_DMG_MANIFEST
    expected_names = {MACOS_PACKAGE, MACOS_SOURCE, MACOS_INSTRUCTIONS}
    rows: dict[str, str] = {}
    try:
        raw_rows = manifest.read_text(encoding="utf-8").splitlines()
    except (OSError, UnicodeDecodeError) as error:
        raise VerificationError("invalid macOS DMG checksum manifest") from error
    for raw in raw_rows:
        match = CHECKSUM_ROW.fullmatch(raw)
        if not match or match.group(2) in rows:
            raise VerificationError(f"invalid macOS DMG checksum row: {raw!r}")
        rows[match.group(2)] = match.group(1)
    if set(rows) != expected_names:
        raise VerificationError("macOS DMG checksum manifest has an unexpected file set")
    for name, expected in rows.items():
        if sha256_file(root / name) != expected:
            raise VerificationError(f"macOS DMG checksum mismatch: {name}")


def _verify_macos_package(
    package: Path,
    work: Path,
    evidence: Path | None = None,
    commit: str | None = None,
) -> None:
    _verify_macos_payload_listing(package)
    expanded = work / "expanded-package"
    _run_native_tool(
        ["/usr/sbin/pkgutil", "--expand", str(package), str(expanded)],
        "macOS package expansion",
    )
    payload = expanded / "Payload"
    try:
        payload_mode = payload.lstat().st_mode
    except FileNotFoundError as error:
        raise VerificationError("expanded macOS component package has no Payload") from error
    if stat.S_ISLNK(payload_mode) or not stat.S_ISREG(payload_mode):
        raise VerificationError("expanded macOS package Payload is not a regular file")
    payload_root = work / "payload-root"
    payload_root.mkdir()
    _run_native_tool(
        [
            "/usr/bin/ditto",
            "-x",
            # Reconstitute pkgbuild's AppleDouble metadata instead of leaving
            # ._*.gram as ordinary files.  Avoid applying quarantine, ACL, or
            # rootless metadata inside the private inspection directory.
            "--rsrc",
            "--extattr",
            "--noqtn",
            "--noacl",
            "--nopersistRootless",
            str(payload),
            str(payload_root),
        ],
        "macOS package payload expansion",
    )
    verify_macos_package_payload(payload_root, evidence, commit)


def _verify_macos_dmg_mount(
    root: Path,
    work: Path,
    evidence: Path | None = None,
    commit: str | None = None,
) -> None:
    expected_names = {
        MACOS_PACKAGE,
        MACOS_SOURCE,
        MACOS_INSTRUCTIONS,
        MACOS_DMG_MANIFEST,
    }
    try:
        contents = {path.name: path for path in root.iterdir()}
    except OSError as error:
        raise VerificationError("cannot enumerate mounted macOS DMG") from error
    if set(contents) != expected_names:
        raise VerificationError("mounted macOS DMG has an unexpected top-level file set")
    for name, path in contents.items():
        mode = path.lstat().st_mode
        if stat.S_ISLNK(mode) or not stat.S_ISREG(mode):
            raise VerificationError(f"mounted macOS DMG member is not a regular file: {name}")
    _verify_macos_dmg_manifest(root)
    _verify_macos_package(contents[MACOS_PACKAGE], work, evidence, commit)


def verify_macos_installer(
    dmg: Path,
    evidence: Path | None = None,
    commit: str | None = None,
) -> None:
    """Mount a final DMG and verify the locked grammar model inside its PKG."""

    if sys.platform != "darwin":
        raise VerificationError("native macOS installer verification requires Darwin")
    if (
        dmg.name != MACOS_DMG
        or dmg.is_symlink()
        or not dmg.is_file()
        or dmg.stat().st_size == 0
    ):
        raise VerificationError(f"invalid final macOS DMG: {dmg}")
    _run_native_tool(["/usr/bin/hdiutil", "verify", str(dmg)], "macOS DMG integrity")

    work = Path(tempfile.mkdtemp(prefix="yunpin-macos-installer-"))
    mount = work / "mount"
    mount.mkdir()
    attached = False
    try:
        attach = _run_native_tool(
            [
                "/usr/bin/hdiutil",
                "attach",
                "-readonly",
                "-nobrowse",
                "-noautoopen",
                "-plist",
                "-mountpoint",
                str(mount),
                str(dmg),
            ],
            "read-only macOS DMG attachment",
        )
        attached = True
        try:
            attach_document = plistlib.loads(attach.stdout)
        except (plistlib.InvalidFileException, ValueError) as error:
            raise VerificationError("hdiutil returned invalid attachment metadata") from error
        if not isinstance(attach_document, dict):
            raise VerificationError("hdiutil returned malformed attachment metadata")
        mount_points = [
            entity.get("mount-point")
            for entity in attach_document.get("system-entities", [])
            if isinstance(entity, dict) and entity.get("mount-point") is not None
        ]
        # macOS canonicalizes /var to /private/var in hdiutil's plist.  Compare
        # the resolved mount identity while still requiring one mounted volume.
        if (
            len(mount_points) != 1
            or not isinstance(mount_points[0], str)
            or Path(mount_points[0]).resolve() != mount.resolve()
        ):
            raise VerificationError("macOS DMG did not attach at the private mount point")
        disk_info = _run_native_tool(
            ["/usr/sbin/diskutil", "info", "-plist", str(mount)],
            "mounted macOS DMG inspection",
        )
        try:
            disk_document = plistlib.loads(disk_info.stdout)
        except (plistlib.InvalidFileException, ValueError) as error:
            raise VerificationError("diskutil returned invalid mounted-volume metadata") from error
        if not isinstance(disk_document, dict):
            raise VerificationError("diskutil returned malformed mounted-volume metadata")
        if disk_document.get("WritableVolume") is not False:
            raise VerificationError("final macOS DMG did not mount read-only")
        _verify_macos_dmg_mount(mount, work, evidence, commit)
    finally:
        detach_failed = False
        if attached:
            detached = subprocess.run(
                ["/usr/bin/hdiutil", "detach", str(mount)],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            if detached.returncode != 0:
                forced = subprocess.run(
                    ["/usr/bin/hdiutil", "detach", "-force", str(mount)],
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                )
                if forced.returncode != 0:
                    detach_failed = True
        if not detach_failed:
            shutil.rmtree(work, ignore_errors=True)
        if detach_failed:
            # Keep the private mount root intact for manual recovery instead of
            # recursively touching a volume that is still attached.
            raise VerificationError("failed to detach private macOS DMG mount")


def scan_asset(path: Path) -> None:
    if not path.is_file() or path.stat().st_size == 0:
        raise VerificationError(f"release asset is missing or empty: {path}")
    name = path.name.lower()
    _check_member_name(path.name, path)
    if name.endswith(".zip"):
        _scan_zip(path)
    elif name.endswith((".tar.gz", ".tgz", ".tar")):
        _scan_tar(path)
    elif name.endswith(".dmg"):
        # The macOS job must run hdiutil verify before writing its metadata.
        return
    else:
        raise VerificationError(f"unsupported release asset type: {path}")


def _asset_rows(paths: list[Path]) -> list[dict[str, str]]:
    names = [path.name for path in paths]
    if len(names) != len(set(names)):
        raise VerificationError("release asset names must be unique")
    return [
        {"name": path.name, "sha256": sha256_file(path)}
        for path in sorted(paths, key=lambda item: item.name)
    ]


def write_job_metadata(
    platform: str,
    tag: str,
    commit: str,
    assets: list[Path],
    output: Path,
) -> None:
    _validate_tag_commit(tag, commit)
    if platform not in {"windows", "macos"}:
        raise VerificationError(f"unsupported platform metadata: {platform}")
    for asset in assets:
        scan_asset(asset)
    document = {
        "schemaVersion": 1,
        "product": "YunPin IME",
        "channel": "development-preview",
        "platform": platform,
        "releaseTag": tag,
        "repositoryCommit": commit,
        "signed": False,
        "productionReady": False,
        "grammarModel": locked_grammar_model(),
        "assets": _asset_rows(assets),
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(
        json.dumps(document, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )


def verify_windows_commit(runtime: Path, source: Path, commit: str) -> None:
    if not COMMIT_PATTERN.fullmatch(commit):
        raise VerificationError(f"invalid repository commit: {commit}")
    requirements = (
        (runtime, "BUILD-METADATA.json", True),
        (source, "BUILD-SOURCE-METADATA.json", False),
    )
    for archive_path, metadata_name, runtime_metadata in requirements:
        scan_asset(archive_path)
        try:
            with zipfile.ZipFile(archive_path) as archive:
                matches = [
                    name
                    for name in archive.namelist()
                    if PurePosixPath(name.replace("\\", "/")).name == metadata_name
                ]
                if len(matches) != 1:
                    raise VerificationError(
                        f"{archive_path.name} must contain exactly one {metadata_name}"
                    )
                document = json.loads(archive.read(matches[0]).decode("utf-8-sig"))
        except (zipfile.BadZipFile, UnicodeDecodeError, json.JSONDecodeError) as error:
            raise VerificationError(
                f"invalid {metadata_name} in {archive_path.name}"
            ) from error
        if document.get("repositoryCommit") != commit:
            raise VerificationError(
                f"{archive_path.name} was not built from expected commit {commit}"
            )
        if document.get("grammarModel") != locked_grammar_model():
            raise VerificationError(
                f"{archive_path.name} grammar model differs from platform locks"
            )
        if runtime_metadata and (
            document.get("signed") is not False
            or document.get("productionReady") is not False
        ):
            raise VerificationError(
                f"{archive_path.name} is not marked as an unsigned development build"
            )
        if runtime_metadata:
            grammar_quality = document.get("grammarQuality")
            if not isinstance(grammar_quality, dict):
                raise VerificationError(
                    f"{archive_path.name} lacks Windows grammar quality evidence"
                )
            shared_quality = dict(grammar_quality)
            try:
                top_level_p95 = shared_quality.pop(
                    "finalKeyCandidateP95Microseconds"
                )
                public_cases = shared_quality.pop("publicCases")
            except KeyError as error:
                raise VerificationError(
                    f"{archive_path.name} grammar quality evidence is incomplete"
                ) from error
            verify_grammar_quality_contract(shared_quality, "Windows")
            if (
                top_level_p95
                != shared_quality["model"]["finalKeyCandidateP95Microseconds"]
                or public_cases
                != [
                    "youyuantuma",
                    "youceshizhanghaoma",
                    "shujukushiyongdeshinagebanben",
                    "qingzaishiyici",
                    "woyijingshoudaole",
                ]
            ):
                raise VerificationError(
                    f"{archive_path.name} Windows grammar quality summary differs"
                )


def _read_metadata(
    path: Path,
    platform: str,
    tag: str,
    commit: str,
    expected_assets: list[Path],
) -> dict[str, object]:
    try:
        document = json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, json.JSONDecodeError) as error:
        raise VerificationError(f"invalid release metadata: {path}") from error
    expected = {
        "schemaVersion": 1,
        "product": "YunPin IME",
        "channel": "development-preview",
        "platform": platform,
        "releaseTag": tag,
        "repositoryCommit": commit,
        "signed": False,
        "productionReady": False,
        "grammarModel": locked_grammar_model(),
        "assets": _asset_rows(expected_assets),
    }
    if document != expected:
        raise VerificationError(f"{platform} release metadata does not match tag, commit, or assets")
    return document


def _directory_files(path: Path) -> dict[str, Path]:
    if not path.is_dir():
        raise VerificationError(f"downloaded artifact directory is missing: {path}")
    files = {item.name: item for item in path.iterdir() if item.is_file()}
    nested = [item for item in path.iterdir() if item.is_dir()]
    if nested:
        raise VerificationError(f"unexpected nested artifact directory: {nested[0]}")
    return files


def _verify_platform_checksums(path: Path, expected_assets: list[Path]) -> None:
    rows: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        match = CHECKSUM_ROW.fullmatch(raw)
        if not match or match.group(2) in rows:
            raise VerificationError(f"invalid platform checksum row: {raw!r}")
        rows[match.group(2)] = match.group(1)
    expected = {asset.name: sha256_file(asset) for asset in expected_assets}
    if rows != expected:
        raise VerificationError("Windows SHA256SUMS does not match the downloaded archives")


def _release_notes(tag: str, commit: str) -> str:
    return f"""# YunPin IME {tag}

> [!WARNING]
> This is an **unsigned development prerelease** for controlled personal testing.
> Windows may show an unsigned-software warning. The macOS DMG contains an
> unsigned development installer and is neither notarized nor production-ready.

Repository commit: `{commit}`

## Downloads

- `YunPin-IME-Windows-development-preview.zip`: Windows x86/x64 TSF runtime and x64 input service.
- `YunPin-IME-Windows-development-preview-source.zip`: matching Windows corresponding source.
- `YunPin-IME-macOS-development-preview.dmg`: macOS 13+ Universal development installer image.
- `YunPin-IME-development-preview-source.tar.gz`: matching macOS corresponding source.
- `SHA256SUMS` and `RELEASE-METADATA.json`: byte hashes and immutable source identity.
- `YunPin-IME-{tag}.spdx.json`: SPDX 2.3 software bill of materials generated from reviewed locks.
- `grammar-quality-metrics.json`: commit- and runtime-bound public A/B quality, latency and RSS evidence.

No personal dictionary, Sogou export, input replay, credential, or encryption key is bundled.
Read the installer documentation before accepting either unsigned build.
"""


def finalize_release(
    tag: str,
    commit: str,
    windows_dir: Path,
    macos_dir: Path,
    sbom: Path,
    output_dir: Path,
) -> None:
    _validate_tag_commit(tag, commit)
    windows = _directory_files(windows_dir)
    macos = _directory_files(macos_dir)
    expected_windows_names = {
        WINDOWS_RUNTIME,
        WINDOWS_SOURCE,
        WINDOWS_CHECKSUMS,
        WINDOWS_METADATA,
    }
    expected_macos_names = {
        MACOS_DMG,
        MACOS_SOURCE,
        MACOS_METADATA,
        MACOS_GRAMMAR_EVIDENCE,
    }
    if set(windows) != expected_windows_names:
        raise VerificationError(
            f"unexpected Windows artifact set: {sorted(windows)}"
        )
    if set(macos) != expected_macos_names:
        raise VerificationError(f"unexpected macOS artifact set: {sorted(macos)}")

    windows_assets = [windows[WINDOWS_RUNTIME], windows[WINDOWS_SOURCE]]
    macos_assets = [macos[MACOS_DMG], macos[MACOS_SOURCE]]
    for asset in windows_assets + macos_assets:
        scan_asset(asset)
    _verify_platform_checksums(windows[WINDOWS_CHECKSUMS], windows_assets)
    verify_windows_commit(
        windows[WINDOWS_RUNTIME], windows[WINDOWS_SOURCE], commit
    )
    windows_metadata = _read_metadata(
        windows[WINDOWS_METADATA], "windows", tag, commit, windows_assets
    )
    macos_metadata = _read_metadata(
        macos[MACOS_METADATA], "macos", tag, commit, macos_assets
    )
    verify_macos_grammar_evidence(macos[MACOS_GRAMMAR_EVIDENCE], commit)
    if windows_metadata["grammarModel"] != macos_metadata["grammarModel"]:
        raise VerificationError("platform release metadata names different grammar models")

    expected_sbom_name = f"YunPin-IME-{tag}.spdx.json"
    if sbom.name != expected_sbom_name or not sbom.is_file() or sbom.stat().st_size == 0:
        raise VerificationError(f"missing expected SPDX asset: {expected_sbom_name}")
    try:
        sbom_document = json.loads(sbom.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise VerificationError(f"invalid SPDX JSON asset: {sbom}") from error
    if (
        sbom_document.get("spdxVersion") != "SPDX-2.3"
        or sbom_document.get("dataLicense") != "CC0-1.0"
    ):
        raise VerificationError("release SBOM must be SPDX-2.3 JSON under CC0-1.0")
    namespace = str(sbom_document.get("documentNamespace", ""))
    if tag not in namespace or commit not in namespace:
        raise VerificationError("release SBOM namespace does not bind tag and commit")
    if SECRET_PATTERN.search(sbom.read_bytes()):
        raise VerificationError("credential-like content in release SBOM")

    if output_dir.exists():
        shutil.rmtree(output_dir)
    output_dir.mkdir(parents=True)
    core_assets = windows_assets + macos_assets
    grammar_evidence = macos[MACOS_GRAMMAR_EVIDENCE]
    durable_assets = core_assets + [sbom, grammar_evidence]
    for asset in durable_assets:
        shutil.copy2(asset, output_dir / asset.name)

    roles = {
        WINDOWS_RUNTIME: ("windows", "runtime"),
        WINDOWS_SOURCE: ("windows", "corresponding-source"),
        MACOS_DMG: ("macos", "installer-image"),
        MACOS_SOURCE: ("macos", "corresponding-source"),
        expected_sbom_name: ("all", "sbom"),
        MACOS_GRAMMAR_EVIDENCE: ("macos", "quality-performance-evidence"),
    }
    release_document = {
        "schemaVersion": 1,
        "product": "YunPin IME",
        "channel": "development-preview",
        "releaseTag": tag,
        "repositoryCommit": commit,
        "signed": False,
        "productionReady": False,
        "grammarModel": locked_grammar_model(),
        "assets": [
            {
                "name": asset.name,
                "platform": roles[asset.name][0],
                "role": roles[asset.name][1],
                "sha256": sha256_file(asset),
            }
            for asset in sorted(durable_assets, key=lambda item: item.name)
        ],
    }
    metadata_path = output_dir / RELEASE_METADATA
    metadata_path.write_text(
        json.dumps(release_document, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    checksum_assets = sorted(
        [*(output_dir / item.name for item in durable_assets), metadata_path],
        key=lambda item: item.name,
    )
    (output_dir / RELEASE_CHECKSUMS).write_text(
        "".join(f"{sha256_file(asset)}  {asset.name}\n" for asset in checksum_assets),
        encoding="utf-8",
    )
    (output_dir / RELEASE_NOTES).write_text(
        _release_notes(tag, commit), encoding="utf-8"
    )


def verify_remote_assets(document_path: Path, local_directory: Path) -> None:
    try:
        document = json.loads(document_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise VerificationError("invalid GitHub release asset response") from error
    if not isinstance(document, list):
        raise VerificationError("GitHub release asset response must be an array")

    if not local_directory.is_dir():
        raise VerificationError("local release asset directory is missing")
    local_assets = {
        path.name: path
        for path in local_directory.iterdir()
        if path.name != RELEASE_NOTES
    }
    if len(local_assets) != 8:
        raise VerificationError("local release directory must contain exactly eight assets")
    for path in local_assets.values():
        if path.is_symlink() or not path.is_file() or path.stat().st_size == 0:
            raise VerificationError(f"invalid local release asset: {path.name}")

    remote_assets: dict[str, dict[str, object]] = {}
    for raw in document:
        if not isinstance(raw, dict) or not isinstance(raw.get("name"), str):
            raise VerificationError("malformed GitHub release asset record")
        name = str(raw["name"])
        if name in remote_assets:
            raise VerificationError(f"duplicate GitHub release asset: {name}")
        remote_assets[name] = raw
    if set(remote_assets) != set(local_assets):
        raise VerificationError("GitHub release asset names do not match local assets")

    for name, path in local_assets.items():
        remote = remote_assets[name]
        if remote.get("state") != "uploaded":
            raise VerificationError(f"GitHub release asset is not uploaded: {name}")
        if remote.get("size") != path.stat().st_size:
            raise VerificationError(f"GitHub release asset size mismatch: {name}")
        expected_digest = f"sha256:{sha256_file(path)}"
        if remote.get("digest") != expected_digest:
            raise VerificationError(f"GitHub release asset digest mismatch: {name}")


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)

    scan = commands.add_parser("scan", help="inspect ZIP/tar assets and check DMG presence")
    scan.add_argument("assets", nargs="+", type=Path)

    metadata = commands.add_parser("write-job-metadata")
    metadata.add_argument("--platform", required=True, choices=("windows", "macos"))
    metadata.add_argument("--tag", required=True)
    metadata.add_argument("--commit", required=True)
    metadata.add_argument("--output", required=True, type=Path)
    metadata.add_argument("assets", nargs="+", type=Path)

    windows_commit = commands.add_parser("verify-windows-commit")
    windows_commit.add_argument("--commit", required=True)
    windows_commit.add_argument("runtime", type=Path)
    windows_commit.add_argument("source", type=Path)

    macos_installer = commands.add_parser(
        "verify-macos-installer",
        help="mount a final DMG and inspect the locked grammar bytes inside its PKG",
    )
    macos_installer.add_argument("--evidence", required=True, type=Path)
    macos_installer.add_argument("--commit", required=True)
    macos_installer.add_argument("dmg", type=Path)

    finalize = commands.add_parser("finalize")
    finalize.add_argument("--tag", required=True)
    finalize.add_argument("--commit", required=True)
    finalize.add_argument("--windows-dir", required=True, type=Path)
    finalize.add_argument("--macos-dir", required=True, type=Path)
    finalize.add_argument("--sbom", required=True, type=Path)
    finalize.add_argument("--output-dir", required=True, type=Path)

    remote = commands.add_parser("verify-remote-assets")
    remote.add_argument("--response", required=True, type=Path)
    remote.add_argument("--local-directory", required=True, type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    arguments = _parser().parse_args(argv)
    try:
        if arguments.command == "scan":
            for asset in arguments.assets:
                scan_asset(asset)
            print(f"release asset scan passed: {len(arguments.assets)} file(s)")
        elif arguments.command == "write-job-metadata":
            write_job_metadata(
                arguments.platform,
                arguments.tag,
                arguments.commit,
                arguments.assets,
                arguments.output,
            )
            print(f"release metadata written: {arguments.output}")
        elif arguments.command == "verify-windows-commit":
            verify_windows_commit(arguments.runtime, arguments.source, arguments.commit)
            print("Windows archive commit metadata passed")
        elif arguments.command == "verify-macos-installer":
            verify_macos_installer(
                arguments.dmg, arguments.evidence, arguments.commit
            )
            print(
                "macOS DMG, expanded PKG grammar resources, and A/B evidence passed"
            )
        elif arguments.command == "finalize":
            finalize_release(
                arguments.tag,
                arguments.commit,
                arguments.windows_dir,
                arguments.macos_dir,
                arguments.sbom,
                arguments.output_dir,
            )
            print(f"release assets finalized: {arguments.output_dir}")
        elif arguments.command == "verify-remote-assets":
            verify_remote_assets(arguments.response, arguments.local_directory)
            print("GitHub release asset state, size, and digest passed")
    except VerificationError as error:
        print(f"release verification failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
