# SPDX-License-Identifier: Apache-2.0
"""Fail-closed wrapper around the separately distributed ImeWlConverter."""

from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import List, Optional, Sequence


MAX_SOURCE_BYTES = 512 * 1024 * 1024
PINNED_VERSION = "v3.4.3"
PINNED_COMMIT = "192fd20b04a060f2574943880160b8e6f13024fa"


class SogouConversionError(RuntimeError):
    pass


@dataclass(frozen=True)
class ConversionArtifact:
    converted_path: Path
    source_sha256: str
    copied_sha256: str
    converter_sha256: str
    source_format: str


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def detect_source_format(path: Path, requested: str = "auto") -> str:
    if requested != "auto":
        return requested
    suffix = path.suffix.lower()
    if suffix == ".scel":
        return "scel"
    if suffix == ".bin":
        return "sgpybin"
    raise SogouConversionError("source format must be supplied for an unknown extension")


def _converter_command(converter: Path, arguments: Sequence[str], dotnet: str) -> List[str]:
    if converter.suffix.lower() == ".dll":
        return [dotnet, str(converter), *arguments]
    return [str(converter), *arguments]


def _sanitized_environment() -> dict:
    allowed = ("PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "DOTNET_ROOT", "LANG", "LC_ALL")
    environment = {name: os.environ[name] for name in allowed if name in os.environ}
    environment["DOTNET_CLI_TELEMETRY_OPTOUT"] = "1"
    environment["DOTNET_NOLOGO"] = "1"
    return environment


def convert_with_pinned_tool(
    source: Path,
    converter: Path,
    converter_sha256: str,
    source_format: str = "auto",
    expected_source_sha256: Optional[str] = None,
    dotnet: str = "dotnet",
    timeout_seconds: int = 180,
) -> ConversionArtifact:
    source = source.resolve(strict=True)
    converter = converter.resolve(strict=True)
    if not source.is_file() or not converter.is_file():
        raise SogouConversionError("source and converter must both be regular files")
    if source.stat().st_size > MAX_SOURCE_BYTES:
        raise SogouConversionError("source exceeds the 512 MiB offline safety limit")

    converter_actual = sha256_file(converter)
    if converter_actual.lower() != converter_sha256.lower():
        raise SogouConversionError("converter SHA-256 does not match the explicitly approved local executable")

    source_before = sha256_file(source)
    if expected_source_sha256 and source_before.lower() != expected_source_sha256.lower():
        raise SogouConversionError("source SHA-256 does not match the expected snapshot")
    detected_format = detect_source_format(source, source_format)

    temp_root = Path(tempfile.mkdtemp(prefix="yunpin-sogou-"))
    keep_artifact = False
    try:
        copied_source = temp_root / f"source{source.suffix.lower()}"
        converted = temp_root / "converted.rime.yaml"
        shutil.copy2(source, copied_source)
        copied_hash = sha256_file(copied_source)
        if copied_hash != source_before:
            raise SogouConversionError("staged copy hash differs from the source")

        version_check = subprocess.run(
            _converter_command(converter, ["--version"], dotnet),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            timeout=min(30, max(1, timeout_seconds)),
            env=_sanitized_environment(),
        )
        version_output = (version_check.stdout + version_check.stderr).decode("utf-8", errors="replace")
        if version_check.returncode != 0 or PINNED_VERSION.lstrip("v") not in version_output:
            raise SogouConversionError(f"converter must report pinned version {PINNED_VERSION}")

        arguments = ["-i", detected_format, "-o", "rime", "-O", str(converted), str(copied_source)]
        completed = subprocess.run(
            _converter_command(converter, arguments, dotnet),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            timeout=max(1, timeout_seconds),
            env=_sanitized_environment(),
        )

        source_after = sha256_file(source)
        if source_after != source_before:
            raise SogouConversionError("source changed while converting; output was discarded")
        if completed.returncode != 0:
            raise SogouConversionError(
                f"converter exited with status {completed.returncode}; captured output was suppressed"
            )
        if not converted.is_file() or converted.stat().st_size == 0:
            raise SogouConversionError("converter did not create a non-empty Rime dictionary")

        artifact = ConversionArtifact(
            converted_path=converted,
            source_sha256=source_before,
            copied_sha256=copied_hash,
            converter_sha256=converter_actual,
            source_format=detected_format,
        )
        keep_artifact = True
        return artifact
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise SogouConversionError(f"converter could not complete safely: {type(exc).__name__}") from exc
    finally:
        if not keep_artifact:
            shutil.rmtree(temp_root, ignore_errors=True)


def dispose_artifact(artifact: ConversionArtifact) -> None:
    shutil.rmtree(artifact.converted_path.parent, ignore_errors=True)
