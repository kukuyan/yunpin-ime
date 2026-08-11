#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Verify and assemble YunPin unsigned-preview release assets.

The release workflow uses this module at three boundaries:

* scan each platform archive before it leaves the build runner;
* bind every platform artifact to the immutable tag commit;
* assemble one exact, checksum-covered release directory.

DMG integrity is checked with ``hdiutil verify`` on the macOS build runner.  A
Linux runner cannot inspect that filesystem image without adding another
unlocked parser, so this module treats the DMG as opaque and binds its bytes to
the macOS job metadata instead.  All ZIP and tar source/runtime archives are
opened and inspected here.
"""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import sys
import tarfile
import zipfile


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
        "b0e81e6a2b933ae9b2638c2527747b11af2d023c53c48b7514623fa24740c44c",
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
MACOS_SOURCE = "YunPin-IME-development-preview-source.tar.gz"
MACOS_METADATA = "macos-release-metadata.json"
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


def _scan_zip(path: Path) -> None:
    try:
        with zipfile.ZipFile(path) as archive:
            corrupt = archive.testzip()
            if corrupt is not None:
                raise VerificationError(f"corrupt ZIP member in {path.name}: {corrupt}")
            for member in archive.infolist():
                if member.filename.replace("\\", "/") in {".", "./"}:
                    continue
                name = _check_member_name(member.filename, path)
                if member.is_dir() or (
                    member.file_size > MAX_SECRET_SCAN_BYTES
                    and _locked_fixture_sha256(name) is None
                ):
                    continue
                _scan_member_bytes(name, archive.read(member), path)
    except zipfile.BadZipFile as error:
        raise VerificationError(f"invalid ZIP archive: {path}") from error


def _scan_tar(path: Path) -> None:
    try:
        with tarfile.open(path, mode="r:*") as archive:
            for member in archive:
                if member.name.replace("\\", "/") in {".", "./"}:
                    continue
                name = _check_member_name(member.name, path)
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
    except (tarfile.TarError, OSError) as error:
        raise VerificationError(f"invalid tar archive: {path}") from error


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
        if runtime_metadata and (
            document.get("signed") is not False
            or document.get("productionReady") is not False
        ):
            raise VerificationError(
                f"{archive_path.name} is not marked as an unsigned development build"
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
    expected_macos_names = {MACOS_DMG, MACOS_SOURCE, MACOS_METADATA}
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
    _read_metadata(
        windows[WINDOWS_METADATA], "windows", tag, commit, windows_assets
    )
    _read_metadata(macos[MACOS_METADATA], "macos", tag, commit, macos_assets)

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
    for asset in core_assets + [sbom]:
        shutil.copy2(asset, output_dir / asset.name)

    roles = {
        WINDOWS_RUNTIME: ("windows", "runtime"),
        WINDOWS_SOURCE: ("windows", "corresponding-source"),
        MACOS_DMG: ("macos", "installer-image"),
        MACOS_SOURCE: ("macos", "corresponding-source"),
        expected_sbom_name: ("all", "sbom"),
    }
    release_document = {
        "schemaVersion": 1,
        "product": "YunPin IME",
        "channel": "development-preview",
        "releaseTag": tag,
        "repositoryCommit": commit,
        "signed": False,
        "productionReady": False,
        "assets": [
            {
                "name": asset.name,
                "platform": roles[asset.name][0],
                "role": roles[asset.name][1],
                "sha256": sha256_file(asset),
            }
            for asset in sorted(core_assets + [sbom], key=lambda item: item.name)
        ],
    }
    metadata_path = output_dir / RELEASE_METADATA
    metadata_path.write_text(
        json.dumps(release_document, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    checksum_assets = sorted(
        [*(output_dir / item.name for item in core_assets + [sbom]), metadata_path],
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
    if len(local_assets) != 7:
        raise VerificationError("local release directory must contain exactly seven assets")
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
