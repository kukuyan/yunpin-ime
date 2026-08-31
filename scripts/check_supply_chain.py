#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Offline checks for immutable container and GitHub Actions references."""

from __future__ import annotations

import json
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
EXCLUDED_TOP_LEVEL = {".git", ".cache", "build", "dist", "third_party"}
FULL_SHA = re.compile(r"[0-9a-f]{40}")
DIGEST = re.compile(r"[0-9a-f]{64}")
UTC_TIMESTAMP = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z")
GRAMMAR_MODEL_FIELDS = {
    "name",
    "filename",
    "repository",
    "release",
    "immutable",
    "assetId",
    "assetUpdatedAt",
    "tagRef",
    "sourceSnapshotAtAssetUpdate",
    "url",
    "sha256",
    "size",
    "license",
    "licenseFilename",
    "licenseUrl",
    "licenseSha256",
    "licenseSize",
}


def _read_json(path: Path, errors: list[str]) -> dict:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        errors.append(f"{path.relative_to(ROOT)}: unreadable JSON ({type(exc).__name__})")
        return {}
    if not isinstance(value, dict):
        errors.append(f"{path.relative_to(ROOT)}: root must be an object")
        return {}
    return value


def check_octagram_source_lock(errors: list[str]) -> int:
    upstream_path = ROOT / "third_party" / "upstreams.lock.json"
    windows_path = ROOT / "platform" / "windows" / "dependencies.lock.json"
    macos_path = ROOT / "platform" / "macos" / "dependencies.lock.json"
    upstream = _read_json(upstream_path, errors)
    windows = _read_json(windows_path, errors)
    macos = _read_json(macos_path, errors)
    if not upstream or not windows or not macos:
        return 0

    matches = [
        item
        for item in upstream.get("upstreams", [])
        if isinstance(item, dict) and item.get("name") == "librime-octagram"
    ]
    if len(matches) != 1:
        errors.append("third_party/upstreams.lock.json: require one librime-octagram row")
        return 0
    canonical = matches[0]
    commit = canonical.get("commit", "")
    expected_archive_url = (
        "https://codeload.github.com/lotem/librime-octagram/tar.gz/" + commit
    )
    expected_license_url = (
        "https://raw.githubusercontent.com/lotem/librime-octagram/"
        + commit
        + "/LICENSE"
    )
    required = {
        "license": "BSD-3-Clause",
        "archive_url": expected_archive_url,
        "license_source": expected_license_url,
    }
    for field, expected in required.items():
        if canonical.get(field) != expected:
            errors.append(f"librime-octagram.{field} does not match the immutable source lock")
    for field in ("commit",):
        if not FULL_SHA.fullmatch(str(canonical.get(field, ""))):
            errors.append(f"librime-octagram.{field} is not a full commit")
    for field in ("archive_sha256", "license_sha256"):
        if not DIGEST.fullmatch(str(canonical.get(field, ""))):
            errors.append(f"librime-octagram.{field} is not a SHA-256")

    windows_lock = windows.get("librimeOctagram")
    if not isinstance(windows_lock, dict):
        errors.append("Windows lock lacks librimeOctagram")
    else:
        field_map = {
            "commit": "commit",
            "url": "archive_url",
            "sha256": "archive_sha256",
            "license": "license",
            "licenseSource": "license_source",
            "licenseSha256": "license_sha256",
            "archiveName": "archive_name",
        }
        for platform_field, canonical_field in field_map.items():
            if windows_lock.get(platform_field) != canonical.get(canonical_field):
                errors.append(
                    f"Windows librimeOctagram.{platform_field} disagrees with the canonical lock"
                )

    macos_matches = [
        item
        for item in macos.get("archives", [])
        if isinstance(item, dict) and item.get("rime_plugin") == "octagram"
    ]
    if len(macos_matches) != 1:
        errors.append("macOS lock must contain one octagram plugin archive")
    else:
        macos_lock = macos_matches[0]
        field_map = {
            "commit": "commit",
            "url": "archive_url",
            "sha256": "archive_sha256",
            "license": "license",
            "license_source": "license_source",
            "license_sha256": "license_sha256",
            "name": "archive_name",
        }
        for platform_field, canonical_field in field_map.items():
            if macos_lock.get(platform_field) != canonical.get(canonical_field):
                errors.append(
                    f"macOS octagram.{platform_field} disagrees with the canonical lock"
                )
    return 1


def project_dockerfiles() -> list[Path]:
    return sorted(
        path
        for path in ROOT.rglob("Dockerfile")
        if path.relative_to(ROOT).parts[0] not in EXCLUDED_TOP_LEVEL
    )


def check_dockerfiles(errors: list[str]) -> int:
    count = 0
    for path in project_dockerfiles():
        aliases: set[str] = set()
        for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            line = raw.strip()
            if not line or line.startswith("#") or not line.upper().startswith("FROM "):
                continue
            count += 1
            fields = line.split()
            index = 1
            while index < len(fields) and fields[index].startswith("--"):
                index += 1
            if index >= len(fields):
                errors.append(f"{path.relative_to(ROOT)}:{number}: malformed FROM")
                continue
            image = fields[index]
            alias = ""
            if index + 2 < len(fields) and fields[index + 1].upper() == "AS":
                alias = fields[index + 2].lower()
            if image.lower() not in aliases:
                if "$" in image:
                    errors.append(f"{path.relative_to(ROOT)}:{number}: FROM may not use ARG expansion")
                elif "@sha256:" not in image:
                    errors.append(f"{path.relative_to(ROOT)}:{number}: external FROM lacks sha256 digest")
                else:
                    tagged, digest = image.rsplit("@sha256:", 1)
                    tail = tagged.rsplit("/", 1)[-1]
                    if ":" not in tail or tail.rsplit(":", 1)[1].lower() == "latest":
                        errors.append(f"{path.relative_to(ROOT)}:{number}: external FROM lacks fixed non-latest tag")
                    if not DIGEST.fullmatch(digest):
                        errors.append(f"{path.relative_to(ROOT)}:{number}: invalid sha256 digest")
            if alias:
                if alias in aliases:
                    errors.append(f"{path.relative_to(ROOT)}:{number}: duplicate stage alias {alias}")
                aliases.add(alias)
    if count == 0:
        errors.append("no project Dockerfile FROM instructions found")
    return count


def _uses_value(line: str) -> str:
    match = re.match(r"^\s*(?:-\s*)?uses:\s*(.*?)\s*(?:#.*)?$", line)
    if not match:
        return ""
    return match.group(1).strip().strip("'\"")


def check_actions(errors: list[str]) -> int:
    count = 0
    workflow_dir = ROOT / ".github" / "workflows"
    workflows = sorted((*workflow_dir.glob("*.yml"), *workflow_dir.glob("*.yaml")))
    for path in workflows:
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            value = _uses_value(line)
            if not value:
                continue
            count += 1
            if value.startswith("./"):
                continue
            if value.startswith("docker://"):
                digest = value.rsplit("@sha256:", 1)[-1] if "@sha256:" in value else ""
                if not DIGEST.fullmatch(digest):
                    errors.append(f"{path.relative_to(ROOT)}:{number}: Docker action lacks sha256 digest")
                continue
            if "@" not in value:
                errors.append(f"{path.relative_to(ROOT)}:{number}: external Action lacks commit")
                continue
            action, revision = value.rsplit("@", 1)
            if not action or not FULL_SHA.fullmatch(revision):
                errors.append(f"{path.relative_to(ROOT)}:{number}: Action must use a full 40-character commit")
    if count == 0:
        errors.append("no GitHub Actions uses references found")
    return count


def check_grammar_model(errors: list[str]) -> int:
    locks = (
        ROOT / "platform" / "macos" / "dependencies.lock.json",
        ROOT / "platform" / "windows" / "dependencies.lock.json",
    )
    try:
        models = [json.loads(path.read_text(encoding="utf-8"))["grammarModel"] for path in locks]
    except (OSError, KeyError, json.JSONDecodeError, TypeError) as error:
        errors.append(f"grammar model lock is unreadable: {type(error).__name__}")
        return 0
    if models[0] != models[1]:
        errors.append("macOS and Windows grammarModel objects must be byte-for-byte equivalent")
        return 0
    model = models[0]
    if not isinstance(model, dict) or set(model) != GRAMMAR_MODEL_FIELDS:
        errors.append("grammarModel has missing or unexpected fields")
        return 0

    name = model.get("name")
    filename = model.get("filename")
    repository = model.get("repository")
    release = model.get("release")
    tag_ref = model.get("tagRef")
    source_snapshot = model.get("sourceSnapshotAtAssetUpdate")
    if not isinstance(name, str) or not name:
        errors.append("grammarModel.name must be non-empty")
    if filename != f"{name}.gram" or Path(str(filename)).name != filename:
        errors.append("grammarModel.filename must be the exact model name plus .gram")
    if repository != "https://github.com/amzxyz/RIME-LMDG":
        errors.append("grammarModel.repository is not the reviewed official upstream")
    if release != "LTS" or model.get("immutable") is not False:
        errors.append("grammarModel must explicitly record the mutable reviewed LTS release")
    if not isinstance(model.get("assetUpdatedAt"), str) or not UTC_TIMESTAMP.fullmatch(
        model["assetUpdatedAt"]
    ):
        errors.append("grammarModel.assetUpdatedAt must be an exact UTC timestamp")
    if not isinstance(tag_ref, str) or not FULL_SHA.fullmatch(tag_ref):
        errors.append("grammarModel.tagRef must be a full lowercase commit")
    if not isinstance(source_snapshot, str) or not FULL_SHA.fullmatch(source_snapshot):
        errors.append(
            "grammarModel.sourceSnapshotAtAssetUpdate must be a full lowercase commit"
        )
    expected_url = f"{repository}/releases/download/{release}/{filename}"
    if model.get("url") != expected_url:
        errors.append("grammarModel.url does not match repository/release/filename")
    if not isinstance(model.get("assetId"), int) or model["assetId"] <= 0:
        errors.append("grammarModel.assetId must be a positive GitHub asset ID")
    if not isinstance(model.get("size"), int) or model["size"] <= 0:
        errors.append("grammarModel.size must be a positive exact byte count")
    if not isinstance(model.get("sha256"), str) or not DIGEST.fullmatch(model["sha256"]):
        errors.append("grammarModel.sha256 must be a lowercase SHA-256")
    if model.get("license") != "CC-BY-4.0":
        errors.append("grammarModel must retain its reviewed CC-BY-4.0 license")
    if model.get("licenseFilename") != "RIME-LMDG-LICENSE.CC-BY-4.0":
        errors.append("grammarModel.licenseFilename must remain an exact non-generic cache name")
    expected_license_url = (
        "https://raw.githubusercontent.com/amzxyz/RIME-LMDG/"
        f"{source_snapshot}/LICENSE"
    )
    if model.get("licenseUrl") != expected_license_url:
        errors.append(
            "grammarModel.licenseUrl must bind the reviewed observed source snapshot"
        )
    if not isinstance(model.get("licenseSize"), int) or model["licenseSize"] <= 0:
        errors.append("grammarModel.licenseSize must be a positive exact byte count")
    if not isinstance(model.get("licenseSha256"), str) or not DIGEST.fullmatch(
        model["licenseSha256"]
    ):
        errors.append("grammarModel.licenseSha256 must be a lowercase SHA-256")

    tracked_models: list[str] = []
    if (ROOT / ".git").exists():
        completed = subprocess.run(
            ["git", "-C", str(ROOT), "ls-files", "--", "*.gram"],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        if completed.returncode != 0:
            errors.append("could not inspect Git index for grammar model data")
        else:
            tracked_models = [line for line in completed.stdout.splitlines() if line]
    if tracked_models:
        errors.append(f"grammar model data entered the repository: {tracked_models[0]}")
    return 1


def main() -> int:
    errors: list[str] = []
    from_count = check_dockerfiles(errors)
    action_count = check_actions(errors)
    native_archive_count = check_octagram_source_lock(errors)
    grammar_model_count = check_grammar_model(errors)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(
        "supply-chain pins passed: "
        f"{from_count} FROM instructions, {action_count} Actions references, "
        f"{native_archive_count} native source archive, "
        f"{grammar_model_count} shared grammar model"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
