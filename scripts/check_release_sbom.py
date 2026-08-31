#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Validate a YunPin SPDX 2.3 Release SBOM or run offline self-tests.

Release usage::

    python3 scripts/check_release_sbom.py --tag "$TAG" --commit "$SHA" SBOM.json

With no arguments, the script exercises deterministic generation, negative
identity/timestamp cases, lock coverage, and clean atomic output in a temporary
directory.  It uses only Python's standard library and committed lock files.
"""

from __future__ import annotations

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import tempfile
from typing import Any, Iterator

import generate_release_sbom as generator


SPDX_ID_PATTERN = re.compile(r"^SPDXRef-[A-Za-z0-9.-]+$")
TEST_TAG = "v0.0.0-preview.0"
TEST_COMMIT = "0123456789abcdef0123456789abcdef01234567"


class CheckError(RuntimeError):
    """Raised when the generated document violates the Release SBOM contract."""


@contextmanager
def source_date_epoch(value: str) -> Iterator[None]:
    previous = os.environ.get("SOURCE_DATE_EPOCH")
    os.environ["SOURCE_DATE_EPOCH"] = value
    try:
        yield
    finally:
        if previous is None:
            os.environ.pop("SOURCE_DATE_EPOCH", None)
        else:
            os.environ["SOURCE_DATE_EPOCH"] = previous


def _parse_created(value: Any) -> int:
    if not isinstance(value, str):
        raise CheckError("creationInfo.created must be an SPDX timestamp")
    try:
        moment = datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(
            tzinfo=timezone.utc
        )
    except ValueError as error:
        raise CheckError("creationInfo.created is not canonical UTC") from error
    epoch = int(moment.timestamp())
    if epoch < 0:
        raise CheckError("creationInfo.created predates the Unix epoch")
    if datetime.fromtimestamp(epoch, tz=timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    ) != value:
        raise CheckError("creationInfo.created is not second-precision UTC")
    return epoch


def _purl_pairs(packages: list[dict[str, Any]]) -> set[tuple[str, str]]:
    pairs: set[tuple[str, str]] = set()
    for package in packages:
        refs = package.get("externalRefs", [])
        if not isinstance(refs, list):
            raise CheckError(f"{package.get('name')}: externalRefs must be an array")
        if any(
            isinstance(ref, dict)
            and ref.get("referenceCategory") == "PACKAGE-MANAGER"
            and ref.get("referenceType") == "purl"
            and str(ref.get("referenceLocator", "")).startswith("pkg:golang/")
            for ref in refs
        ):
            pairs.add((str(package.get("name")), str(package.get("versionInfo"))))
    return pairs


def _locked_go_pairs() -> set[tuple[str, str]]:
    lock = generator.load_json(generator.GO_MODULE_LOCK)
    pairs: set[tuple[str, str]] = set()
    for section in ("modules", "local_replacements"):
        rows = lock.get(section)
        if not isinstance(rows, list):
            raise CheckError(f"Go license lock {section} must be an array")
        for row in rows:
            if not isinstance(row, dict):
                raise CheckError(f"Go license lock {section} entry must be an object")
            pair = (str(row.get("module", "")), str(row.get("version", "")))
            if not all(pair):
                raise CheckError(f"Go license lock {section} entry lacks module/version")
            if pair in pairs:
                raise CheckError(f"duplicate locked Go module: {pair[0]}@{pair[1]}")
            pairs.add(pair)
    return pairs


def _package(packages: list[dict[str, Any]], name: str, version: str) -> dict[str, Any]:
    matches = [
        package
        for package in packages
        if package.get("name") == name and package.get("versionInfo") == version
    ]
    if len(matches) != 1:
        raise CheckError(f"expected exactly one {name}@{version} package")
    return matches[0]


def validate_document(document: dict[str, Any], tag: str, commit: str) -> None:
    generator.validate_release_identity(tag, commit)
    if document.get("spdxVersion") != "SPDX-2.3":
        raise CheckError("SBOM must use SPDX-2.3")
    if document.get("dataLicense") != "CC0-1.0":
        raise CheckError("SBOM document dataLicense must be CC0-1.0")
    if document.get("SPDXID") != "SPDXRef-DOCUMENT":
        raise CheckError("SBOM document SPDXID is invalid")
    namespace = document.get("documentNamespace")
    if not isinstance(namespace, str) or tag not in namespace or commit not in namespace:
        raise CheckError("documentNamespace must bind both release tag and commit")

    creation = document.get("creationInfo")
    if not isinstance(creation, dict):
        raise CheckError("SBOM lacks creationInfo")
    epoch = _parse_created(creation.get("created"))
    if creation.get("creators") != [f"Tool: {generator.TOOL_NAME}"]:
        raise CheckError("SBOM creator is not the locked YunPin generator")

    packages = document.get("packages")
    if not isinstance(packages, list) or not packages:
        raise CheckError("SBOM must contain packages")
    package_ids: list[str] = []
    for raw in packages:
        if not isinstance(raw, dict):
            raise CheckError("every SPDX package must be an object")
        package_id = raw.get("SPDXID")
        if not isinstance(package_id, str) or not SPDX_ID_PATTERN.fullmatch(package_id):
            raise CheckError(f"invalid package SPDXID: {package_id}")
        package_ids.append(package_id)
        if raw.get("filesAnalyzed") is not False:
            raise CheckError(f"{raw.get('name')}: release SBOM must not analyze files")
        if raw.get("licenseDeclared") in {None, "", "NOASSERTION"}:
            raise CheckError(f"{raw.get('name')}: missing locked declared license")
        if raw.get("downloadLocation") in {None, "", "NOASSERTION", "NONE"}:
            raise CheckError(f"{raw.get('name')}: missing locked download location")
        if not isinstance(raw.get("versionInfo"), str) or not raw["versionInfo"]:
            raise CheckError(f"{raw.get('name')}: missing locked version")
    if len(package_ids) != len(set(package_ids)):
        raise CheckError("package SPDXIDs must be unique")

    root = _package(packages, "YunPin IME", tag)
    if document.get("documentDescribes") != [root["SPDXID"]]:
        raise CheckError("documentDescribes must contain only the YunPin root package")
    if _purl_pairs(packages) != _locked_go_pairs():
        missing = _locked_go_pairs() - _purl_pairs(packages)
        stale = _purl_pairs(packages) - _locked_go_pairs()
        details = [f"missing {name}@{version}" for name, version in sorted(missing)]
        details.extend(f"stale {name}@{version}" for name, version in sorted(stale))
        raise CheckError("Go module lock coverage mismatch: " + ", ".join(details))

    # Explicit release-critical coverage.  These values are sourced from the
    # structured locks and deliberately fail when a lock is changed without an
    # accompanying regenerated document.
    windows = generator.load_json(generator.WINDOWS_LOCK)
    macos = generator.load_json(generator.MACOS_LOCK)
    if any(row.get("name") == "Sparkle" for row in macos["nested_components"]):
        raise CheckError("removed Sparkle component returned to the macOS lock")
    if any(package.get("name") == "Sparkle" for package in packages):
        raise CheckError("release SBOM must not contain the removed Sparkle runtime")
    for boost_version in (
        str(windows["boost"]["version"]),
        str(macos["boost_version"]),
    ):
        boost = _package(packages, "Boost", boost_version)
        if boost.get("licenseDeclared") != "BSL-1.0":
            raise CheckError(f"Boost@{boost_version} lacks its locked BSL-1.0 license")
    for name in (
        "weasel",
        "squirrel",
        "librime",
        "librime-octagram",
        "rime-ice",
        "rime-essay",
    ):
        if not any(str(package.get("name", "")).lower() == name for package in packages):
            raise CheckError(f"release-critical upstream is absent: {name}")
    octagram_lock = next(
        row
        for row in generator.load_json(generator.UPSTREAM_LOCK)["upstreams"]
        if row["name"] == "librime-octagram"
    )
    octagram = _package(packages, "librime-octagram", octagram_lock["commit"])
    if octagram.get("licenseDeclared") != "BSD-3-Clause":
        raise CheckError("librime-octagram lacks its locked BSD-3-Clause license")
    checksums = octagram.get("checksums")
    if checksums != [
        {
            "algorithm": "SHA256",
            "checksumValue": octagram_lock["archive_sha256"],
        }
    ]:
        raise CheckError("librime-octagram lacks its locked source archive SHA-256")

    relationships = document.get("relationships")
    if not isinstance(relationships, list) or not relationships:
        raise CheckError("SBOM must describe package relationships")
    valid_ids = set(package_ids) | {"SPDXRef-DOCUMENT"}
    for relation in relationships:
        if not isinstance(relation, dict):
            raise CheckError("every SPDX relationship must be an object")
        if relation.get("spdxElementId") not in valid_ids:
            raise CheckError("relationship has an unknown source SPDXID")
        if relation.get("relatedSpdxElement") not in valid_ids:
            raise CheckError("relationship has an unknown target SPDXID")

    # Rebuild from the allow-listed locks and require exact canonical bytes.
    # Deriving SOURCE_DATE_EPOCH from the document makes validation independent
    # of whether separate workflow steps inherit that environment variable.
    with source_date_epoch(str(epoch)):
        expected = generator.build_document(tag, commit)
    if document != expected:
        raise CheckError("SBOM differs from deterministic generation of repository locks")


def load_and_validate(path: Path, tag: str, commit: str) -> dict[str, Any]:
    try:
        raw = path.read_text(encoding="utf-8")
        document = json.loads(raw)
    except (OSError, json.JSONDecodeError) as error:
        raise CheckError(f"cannot read SPDX JSON: {path}") from error
    if not isinstance(document, dict):
        raise CheckError("SPDX JSON root must be an object")
    validate_document(document, tag, commit)
    if raw != generator.canonical_json(document):
        raise CheckError("SPDX JSON is not in canonical deterministic form")
    return document


def _expect_generator_error(action: Any, description: str) -> None:
    try:
        action()
    except generator.SBOMError:
        return
    raise CheckError(f"negative self-test did not reject {description}")


def run_self_tests() -> int:
    with tempfile.TemporaryDirectory(prefix="yunpin-sbom-test-") as raw_directory:
        directory = Path(raw_directory)
        first = directory / "first.spdx.json"
        second = directory / "second.spdx.json"
        with source_date_epoch("1700000000"):
            document = generator.build_document(TEST_TAG, TEST_COMMIT)
            generator.write_document(document, first)
            generator.write_document(generator.build_document(TEST_TAG, TEST_COMMIT), second)
        if first.read_bytes() != second.read_bytes():
            raise CheckError("identical lock inputs did not produce identical SBOM bytes")
        load_and_validate(first, TEST_TAG, TEST_COMMIT)
        if sorted(path.name for path in directory.iterdir()) != [
            "first.spdx.json",
            "second.spdx.json",
        ]:
            raise CheckError("generation left temporary or unrelated output files")

    _expect_generator_error(
        lambda: generator.build_document("preview", TEST_COMMIT), "an invalid tag"
    )
    _expect_generator_error(
        lambda: generator.build_document(TEST_TAG, "deadbeef"), "a short commit"
    )
    with source_date_epoch("not-a-number"):
        _expect_generator_error(generator.creation_timestamp, "an invalid epoch")
    with source_date_epoch("0"):
        if generator.creation_timestamp() != "1970-01-01T00:00:00Z":
            raise CheckError("SOURCE_DATE_EPOCH=0 is not reproducible")
    print(
        f"release SBOM self-test passed: {len(document['packages'])} locked packages, "
        f"{len(_locked_go_pairs())} Go module versions"
    )
    return 0


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", help="strict vX.Y.Z-preview.N tag")
    parser.add_argument("--commit", help="full lowercase repository commit")
    parser.add_argument("document", nargs="?", type=Path, help="SPDX JSON to validate")
    args = parser.parse_args(argv)
    supplied = (args.tag is not None, args.commit is not None, args.document is not None)
    if any(supplied) and not all(supplied):
        parser.error("--tag, --commit and document must be supplied together")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        if args.document is None:
            return run_self_tests()
        document = load_and_validate(args.document, args.tag, args.commit)
    except (CheckError, generator.SBOMError) as error:
        print(f"release SBOM check failed: {error}", file=os.sys.stderr)
        return 1
    print(f"release SBOM passed: {len(document['packages'])} locked packages")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
