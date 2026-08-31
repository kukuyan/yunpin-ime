#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Generate YunPin's deterministic, offline SPDX 2.3 Release SBOM.

Only committed, allow-listed lock files are read.  The generator never invokes
Git, scans the worktree, reads build/user-data directories, or accesses the
network.  ``SOURCE_DATE_EPOCH`` controls the creation timestamp; when it is not
set, the Unix epoch is used so identical inputs always produce identical bytes.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
from typing import Any
from urllib.parse import quote


ROOT = Path(__file__).resolve().parents[1]
UPSTREAM_LOCK = ROOT / "third_party" / "upstreams.lock.json"
GO_MODULE_LOCK = ROOT / "third_party" / "go-modules.lock.json"
PLATFORM_LOCK = ROOT / "platform" / "upstream-lock.json"
WINDOWS_LOCK = ROOT / "platform" / "windows" / "dependencies.lock.json"
MACOS_LOCK = ROOT / "platform" / "macos" / "dependencies.lock.json"
LOCK_PATHS = (
    UPSTREAM_LOCK,
    GO_MODULE_LOCK,
    PLATFORM_LOCK,
    WINDOWS_LOCK,
    MACOS_LOCK,
)

TAG_PATTERN = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$")
COMMIT_PATTERN = re.compile(r"^[0-9a-f]{40}$")
SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
SPDX_ID_PATTERN = re.compile(r"^SPDXRef-[A-Za-z0-9.-]+$")
ROOT_REPOSITORY = "https://github.com/kukuyan/yunpin-ime"
ROOT_LICENSE = "Apache-2.0 AND GPL-3.0-only"
TOOL_NAME = "yunpin-release-sbom/1.0"


class SBOMError(RuntimeError):
    """Raised when locked metadata cannot produce a trustworthy SBOM."""


def _object(value: Any, context: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise SBOMError(f"{context} must be a JSON object")
    return value


def _array(value: Any, context: str) -> list[Any]:
    if not isinstance(value, list):
        raise SBOMError(f"{context} must be a JSON array")
    return value


def _string(value: Any, context: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise SBOMError(f"{context} must be a non-empty string")
    return value


def _https_url(value: Any, context: str) -> str:
    url = _string(value, context)
    if not url.startswith("https://"):
        raise SBOMError(f"{context} must be an HTTPS URL")
    return url


def _commit(value: Any, context: str) -> str:
    commit = _string(value, context)
    if not COMMIT_PATTERN.fullmatch(commit):
        raise SBOMError(f"{context} must be a lowercase 40-character commit")
    return commit


def _sha256(value: Any, context: str) -> str:
    digest = _string(value, context)
    if not SHA256_PATTERN.fullmatch(digest):
        raise SBOMError(f"{context} must be a lowercase SHA-256")
    return digest


def _license(value: Any, context: str) -> str:
    declared = _string(value, context)
    if declared == "NOASSERTION":
        raise SBOMError(f"{context} may not be NOASSERTION")
    return declared


def load_json(path: Path) -> dict[str, Any]:
    if path not in LOCK_PATHS:
        raise SBOMError(f"refusing non-allow-listed SBOM input: {path}")
    try:
        return _object(json.loads(path.read_text(encoding="utf-8")), str(path))
    except (OSError, json.JSONDecodeError) as error:
        raise SBOMError(f"cannot read locked SBOM metadata: {path}") from error


def validate_release_identity(tag: str, commit: str) -> None:
    if not TAG_PATTERN.fullmatch(tag):
        raise SBOMError(f"invalid preview tag: {tag}")
    if not COMMIT_PATTERN.fullmatch(commit):
        raise SBOMError(f"invalid repository commit: {commit}")


def creation_timestamp() -> str:
    raw = os.environ.get("SOURCE_DATE_EPOCH", "0")
    if not re.fullmatch(r"0|[1-9][0-9]*", raw):
        raise SBOMError("SOURCE_DATE_EPOCH must be a non-negative integer")
    try:
        moment = datetime.fromtimestamp(int(raw), tz=timezone.utc)
    except (OverflowError, OSError, ValueError) as error:
        raise SBOMError("SOURCE_DATE_EPOCH is outside the supported range") from error
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def stable_spdx_id(identity: str, label: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9.-]+", "-", label).strip("-.") or "Package"
    slug = slug[:48]
    suffix = hashlib.sha256(identity.encode("utf-8")).hexdigest()[:16]
    result = f"SPDXRef-Package-{slug}-{suffix}"
    if not SPDX_ID_PATTERN.fullmatch(result):  # defensive; slug is normalized above
        raise SBOMError(f"could not create a valid SPDX identifier for {label}")
    return result


def _vcs_reference(repository: str, commit: str) -> dict[str, str]:
    return {
        "referenceCategory": "OTHER",
        "referenceType": "vcs",
        "referenceLocator": f"git+{repository}@{commit}",
    }


def _purl_reference(module: str, version: str) -> dict[str, str]:
    locator = f"pkg:golang/{quote(module, safe='/')}@{quote(version, safe='')}"
    return {
        "referenceCategory": "PACKAGE-MANAGER",
        "referenceType": "purl",
        "referenceLocator": locator,
    }


def make_package(
    *,
    identity: str,
    name: str,
    version: str,
    download_location: str,
    declared_license: str,
    source_info: str,
    purpose: str = "LIBRARY",
    repository: str | None = None,
    commit: str | None = None,
    sha256: str | None = None,
    archive_name: str | None = None,
    external_refs: list[dict[str, str]] | None = None,
) -> dict[str, Any]:
    package: dict[str, Any] = {
        "SPDXID": stable_spdx_id(identity, name),
        "name": _string(name, f"{identity}.name"),
        "versionInfo": _string(version, f"{identity}.version"),
        "downloadLocation": _string(
            download_location, f"{identity}.downloadLocation"
        ),
        "filesAnalyzed": False,
        "licenseConcluded": "NOASSERTION",
        "licenseDeclared": _license(declared_license, f"{identity}.license"),
        "copyrightText": "NOASSERTION",
        "primaryPackagePurpose": purpose,
        "sourceInfo": _string(source_info, f"{identity}.sourceInfo"),
    }
    refs = list(external_refs or [])
    if commit is not None or repository is not None:
        if commit is None or repository is None:
            raise SBOMError(f"{identity}: repository and commit must be provided together")
        refs.append(
            _vcs_reference(
                _https_url(repository, f"{identity}.repository"),
                _commit(commit, f"{identity}.commit"),
            )
        )
    if refs:
        package["externalRefs"] = sorted(
            refs,
            key=lambda item: (
                item["referenceCategory"],
                item["referenceType"],
                item["referenceLocator"],
            ),
        )
    if sha256 is not None:
        package["checksums"] = [
            {
                "algorithm": "SHA256",
                "checksumValue": _sha256(sha256, f"{identity}.sha256"),
            }
        ]
    if archive_name is not None:
        package["packageFileName"] = _string(archive_name, f"{identity}.archive")
    return package


def _load_upstream_packages(
    upstream_lock: dict[str, Any], platform_lock: dict[str, Any]
) -> list[dict[str, Any]]:
    if upstream_lock.get("format") != 1:
        raise SBOMError("unsupported third_party upstream lock format")
    if platform_lock.get("schemaVersion") != 1:
        raise SBOMError("unsupported platform upstream lock format")

    platform_components: dict[str, dict[str, Any]] = {}
    for raw in _array(platform_lock.get("components"), "platform components"):
        component = _object(raw, "platform component")
        name = _string(component.get("name"), "platform component name").lower()
        if name in platform_components:
            raise SBOMError(f"duplicate platform component: {name}")
        platform_components[name] = component

    packages: list[dict[str, Any]] = []
    names: set[str] = set()
    for raw in _array(upstream_lock.get("upstreams"), "third-party upstreams"):
        item = _object(raw, "third-party upstream")
        name = _string(item.get("name"), "upstream name")
        folded = name.lower()
        if folded in names:
            raise SBOMError(f"duplicate upstream component: {name}")
        names.add(folded)
        repository = _https_url(item.get("url"), f"{name}.url")
        commit = _commit(item.get("commit"), f"{name}.commit")
        declared = _license(item.get("license"), f"{name}.license")
        version = _string(item.get("release", commit), f"{name}.version")

        matching = platform_components.get(folded)
        source = "third_party/upstreams.lock.json"
        if matching is not None:
            if _commit(matching.get("commit"), f"platform {name}.commit") != commit:
                raise SBOMError(f"platform and third-party locks disagree for {name}")
            if _https_url(
                matching.get("repository"), f"platform {name}.repository"
            ).removesuffix(".git") != repository.removesuffix(".git"):
                raise SBOMError(f"platform and third-party URLs disagree for {name}")
            source += " and platform/upstream-lock.json"

        archive_url = item.get("archive_url")
        archive_sha256 = item.get("archive_sha256")
        archive_name = item.get("archive_name")
        if any(value is not None for value in (archive_url, archive_sha256, archive_name)):
            if not all(value is not None for value in (archive_url, archive_sha256, archive_name)):
                raise SBOMError(f"{name}: incomplete immutable source archive lock")
            download_location = _https_url(archive_url, f"{name}.archive_url")
            checksum = _sha256(archive_sha256, f"{name}.archive_sha256")
            package_file_name = _string(archive_name, f"{name}.archive_name")
        else:
            download_location = repository
            checksum = None
            package_file_name = None

        purpose = "DATA" if folded in {
            "rime-ice", "rime-essay", "thuocl", "phrase-pinyin-data"
        } else "APPLICATION" if folded in {"weasel", "squirrel", "imewlconverter"} else "LIBRARY"
        packages.append(
            make_package(
                identity=f"upstream:{name}:{commit}",
                name=name,
                version=version,
                download_location=download_location,
                declared_license=declared,
                source_info=source,
                purpose=purpose,
                repository=repository,
                commit=commit,
                sha256=checksum,
                archive_name=package_file_name,
            )
        )
    missing = set(platform_components) - names
    if missing:
        raise SBOMError(
            "platform components missing from third-party lock: "
            + ", ".join(sorted(missing))
        )
    return packages


def _load_windows_packages(
    lock: dict[str, Any], upstream_lock: dict[str, Any]
) -> list[dict[str, Any]]:
    if lock.get("schemaVersion") != 1:
        raise SBOMError("unsupported Windows dependency lock format")
    upstreams = {
        _string(item.get("name"), "upstream name").lower(): item
        for item in _array(upstream_lock.get("upstreams"), "third-party upstreams")
    }
    for key in ("weasel", "librime"):
        section = _object(lock.get(key), f"Windows {key} lock")
        upstream = _object(upstreams.get(key), f"upstream {key}")
        if _commit(section.get("commit"), f"Windows {key}.commit") != _commit(
            upstream.get("commit"), f"upstream {key}.commit"
        ):
            raise SBOMError(f"Windows and upstream locks disagree for {key}")

    boost = _object(lock.get("boost"), "Windows Boost lock")
    version = _string(boost.get("version"), "Windows Boost version")
    return [
        make_package(
            identity=f"windows:boost:{version}:{boost.get('sha256')}",
            name="Boost",
            version=version,
            download_location=_https_url(boost.get("url"), "Windows Boost URL"),
            declared_license=_license(boost.get("license"), "Windows Boost license"),
            source_info="platform/windows/dependencies.lock.json",
            sha256=_sha256(boost.get("sha256"), "Windows Boost SHA-256"),
            archive_name=Path(_string(boost.get("url"), "Windows Boost URL")).name,
        )
    ]


def _load_macos_packages(lock: dict[str, Any]) -> list[dict[str, Any]]:
    if lock.get("format") != 1:
        raise SBOMError("unsupported macOS dependency lock format")
    nested_map = _object(lock.get("nested_submodules"), "macOS nested submodules")
    components = _array(lock.get("nested_components"), "macOS nested components")
    archives = [
        _object(item, "macOS archive")
        for item in _array(lock.get("archives"), "macOS archives")
    ]
    archive_by_name = {
        _string(item.get("name"), "macOS archive name"): item for item in archives
    }
    if len(archive_by_name) != len(archives):
        raise SBOMError("duplicate macOS archive names")

    boost_archives = [item for item in archives if item.get("boost_source") is True]
    if len(boost_archives) != 1:
        raise SBOMError("macOS lock must contain exactly one Boost source archive")
    boost = boost_archives[0]
    boost_version = _string(lock.get("boost_version"), "macOS Boost version")
    if _string(boost.get("version"), "macOS Boost archive version") != boost_version:
        raise SBOMError("macOS Boost version disagrees with its locked archive")

    packages = [
        make_package(
            identity=f"macos:boost:{boost_version}:{boost.get('sha256')}",
            name="Boost",
            version=boost_version,
            download_location=_https_url(boost.get("url"), "macOS Boost URL"),
            declared_license=_license(boost.get("license"), "macOS Boost license"),
            source_info="platform/macos/dependencies.lock.json",
            sha256=_sha256(boost.get("sha256"), "macOS Boost SHA-256"),
            archive_name=_string(boost.get("name"), "macOS Boost archive name"),
        )
    ]

    component_names: set[str] = set()
    for raw in components:
        component = _object(raw, "macOS nested component")
        name = _string(component.get("name"), "macOS component name")
        if name in component_names:
            raise SBOMError(f"duplicate macOS nested component: {name}")
        component_names.add(name)
        commit = _commit(component.get("commit"), f"macOS {name}.commit")
        if _commit(nested_map.get(name), f"macOS nested_submodules.{name}") != commit:
            raise SBOMError(f"macOS nested component map disagrees for {name}")
        version = _string(component.get("version"), f"macOS {name}.version")
        repository = _https_url(component.get("repository"), f"macOS {name}.repository")
        declared = _license(component.get("license"), f"macOS {name}.license")
        packages.append(
            make_package(
                identity=f"macos:{name}:{version}:{commit}",
                name=name,
                version=version,
                download_location=repository,
                declared_license=declared,
                source_info="platform/macos/dependencies.lock.json",
                repository=repository,
                commit=commit,
            )
        )
    if component_names != set(nested_map):
        missing = set(nested_map) - component_names
        extra = component_names - set(nested_map)
        details = sorted(missing | extra)
        raise SBOMError("macOS nested component coverage mismatch: " + ", ".join(details))
    return packages


def _load_go_packages(
    lock: dict[str, Any], repository_commit: str
) -> tuple[list[dict[str, Any]], set[str]]:
    if lock.get("format") != 1:
        raise SBOMError("unsupported Go module license lock format")
    packages: list[dict[str, Any]] = []
    local_ids: set[str] = set()
    pairs: set[tuple[str, str]] = set()

    for raw in _array(lock.get("modules"), "Go module lock modules"):
        item = _object(raw, "Go module lock entry")
        module = _string(item.get("module"), "Go module path")
        version = _string(item.get("version"), f"{module}.version")
        pair = (module, version)
        if pair in pairs:
            raise SBOMError(f"duplicate Go module lock entry: {module}@{version}")
        pairs.add(pair)
        evidence = _https_url(item.get("license_source"), f"{module}@{version}.license_source")
        packages.append(
            make_package(
                identity=f"go:{module}@{version}",
                name=module,
                version=version,
                download_location=evidence,
                declared_license=_license(item.get("license"), f"{module}@{version}.license"),
                source_info=(
                    "third_party/go-modules.lock.json; downloadLocation is the "
                    "reviewed, version-pinned source/license evidence URL"
                ),
                external_refs=[_purl_reference(module, version)],
            )
        )

    for raw in _array(lock.get("local_replacements"), "Go local replacements"):
        item = _object(raw, "Go local replacement")
        module = _string(item.get("module"), "local Go module path")
        version = _string(item.get("version"), f"{module}.version")
        pair = (module, version)
        if pair in pairs:
            raise SBOMError(f"duplicate Go module lock entry: {module}@{version}")
        pairs.add(pair)
        replacement = _string(item.get("replacement"), f"{module}.replacement")
        package = make_package(
            identity=f"go-local:{module}@{version}:{replacement}",
            name=module,
            version=version,
            download_location=ROOT_REPOSITORY,
            declared_license=_license(item.get("license"), f"{module}@{version}.license"),
            source_info=(
                "third_party/go-modules.lock.json local replacement " + replacement
            ),
            repository=ROOT_REPOSITORY,
            commit=repository_commit,
            external_refs=[_purl_reference(module, version)],
        )
        packages.append(package)
        local_ids.add(package["SPDXID"])
    return packages, local_ids


def build_document(tag: str, commit: str) -> dict[str, Any]:
    validate_release_identity(tag, commit)
    upstream_lock = load_json(UPSTREAM_LOCK)
    platform_lock = load_json(PLATFORM_LOCK)
    windows_lock = load_json(WINDOWS_LOCK)
    macos_lock = load_json(MACOS_LOCK)
    go_lock = load_json(GO_MODULE_LOCK)

    root = make_package(
        identity=f"root:{tag}:{commit}",
        name="YunPin IME",
        version=tag,
        download_location=ROOT_REPOSITORY,
        declared_license=ROOT_LICENSE,
        source_info="Repository license policy in docs/LICENSE_MATRIX.md",
        purpose="APPLICATION",
        repository=ROOT_REPOSITORY,
        commit=commit,
    )
    upstream_packages = _load_upstream_packages(upstream_lock, platform_lock)
    packages = [root]
    packages.extend(upstream_packages)
    packages.extend(_load_windows_packages(windows_lock, upstream_lock))
    packages.extend(_load_macos_packages(macos_lock))
    go_packages, local_go_ids = _load_go_packages(go_lock, commit)
    packages.extend(go_packages)
    packages.sort(
        key=lambda item: (
            0 if item["SPDXID"] == root["SPDXID"] else 1,
            item["name"].casefold(),
            item["versionInfo"],
            item["SPDXID"],
        )
    )

    package_ids = [item["SPDXID"] for item in packages]
    if len(package_ids) != len(set(package_ids)):
        raise SBOMError("stable SPDX package identifiers are not unique")

    relationships = [
        {
            "spdxElementId": "SPDXRef-DOCUMENT",
            "relationshipType": "DESCRIBES",
            "relatedSpdxElement": root["SPDXID"],
        }
    ]
    for package_id in sorted(set(package_ids) - {root["SPDXID"]}):
        relationships.append(
            {
                "spdxElementId": root["SPDXID"],
                "relationshipType": (
                    "CONTAINS" if package_id in local_go_ids else "DEPENDS_ON"
                ),
                "relatedSpdxElement": package_id,
            }
        )

    return {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"YunPin-IME-{tag}",
        "documentNamespace": (
            f"{ROOT_REPOSITORY}/releases/tag/{quote(tag, safe='.-')}/spdx/{commit}"
        ),
        "creationInfo": {
            "created": creation_timestamp(),
            "creators": [f"Tool: {TOOL_NAME}"],
            "licenseListVersion": "3.20",
        },
        "documentDescribes": [root["SPDXID"]],
        "comment": (
            "Generated offline exclusively from committed lock metadata; "
            "the worktree, build outputs, and personal data are not scanned."
        ),
        "packages": packages,
        "relationships": relationships,
    }


def canonical_json(document: dict[str, Any]) -> str:
    return json.dumps(
        document,
        ensure_ascii=False,
        indent=2,
        sort_keys=True,
        separators=(",", ": "),
    ) + "\n"


def write_document(document: dict[str, Any], output: Path) -> None:
    resolved_output = output.expanduser().resolve()
    if resolved_output in {path.resolve() for path in LOCK_PATHS}:
        raise SBOMError("refusing to overwrite an SBOM input lock")
    if resolved_output.exists() and resolved_output.is_dir():
        raise SBOMError(f"SBOM output is a directory: {resolved_output}")
    resolved_output.parent.mkdir(parents=True, exist_ok=True)
    encoded = canonical_json(document).encode("utf-8")
    temporary: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="wb",
            dir=resolved_output.parent,
            prefix=f".{resolved_output.name}.",
            suffix=".tmp",
            delete=False,
        ) as stream:
            temporary = Path(stream.name)
            stream.write(encoded)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, resolved_output)
        temporary = None
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True, help="strict vX.Y.Z-preview.N tag")
    parser.add_argument("--commit", required=True, help="full lowercase repository commit")
    parser.add_argument("--output", required=True, type=Path, help="output SPDX JSON path")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        document = build_document(args.tag, args.commit)
        write_document(document, args.output)
    except SBOMError as error:
        print(f"release SBOM generation failed: {error}", file=os.sys.stderr)
        return 1
    print(
        f"release SBOM generated: {args.output} ({len(document['packages'])} packages)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
