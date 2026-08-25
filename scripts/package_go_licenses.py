#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Create a deterministic license-text bundle for a linked Go command."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess


ROOT = Path(__file__).resolve().parents[1]
LOCK = ROOT / "third_party" / "go-modules.lock.json"
LICENSE_NAMES = re.compile(r"^(license|copying|notice)(\..*)?$", re.IGNORECASE)


def go_json_objects(
    module: Path, go_package: str
) -> list[dict[str, object]]:
    completed = subprocess.run(
        ["go", "list", "-deps", "-json", go_package],
        cwd=module,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="strict",
    )
    decoder = json.JSONDecoder()
    text = completed.stdout
    cursor = 0
    objects: list[dict[str, object]] = []
    while cursor < len(text):
        while cursor < len(text) and text[cursor].isspace():
            cursor += 1
        if cursor == len(text):
            break
        value, cursor = decoder.raw_decode(text, cursor)
        if isinstance(value, dict):
            objects.append(value)
    return objects


def safe_name(value: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]+", "_", value)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-module", type=Path, required=True)
    parser.add_argument("--go-package", default="./cmd/yunpin-sync-agent")
    parser.add_argument("--artifact", default="yunpin-sync-agent")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    lock = json.loads(LOCK.read_text(encoding="utf-8"))
    locked = {
        (row["module"], row["version"]): row["license"]
        for row in lock["modules"]
    }
    modules: dict[tuple[str, str], Path] = {}
    for package in go_json_objects(
        args.go_module.resolve(), args.go_package
    ):
        module = package.get("Module")
        if not isinstance(module, dict) or module.get("Main") is True:
            continue
        path = module.get("Path")
        version = module.get("Version")
        directory = module.get("Dir")
        replacement = module.get("Replace")
        if isinstance(replacement, dict):
            directory = replacement.get("Dir", directory)
        if not all(isinstance(value, str) and value for value in (path, version, directory)):
            raise SystemExit("linked Go module lacks a stable path, version, or directory")
        if path.startswith("github.com/kukuyan/yunpin-ime/"):
            continue
        key = (path, version)
        if key not in locked:
            raise SystemExit(f"linked Go module is absent from license lock: {path}@{version}")
        modules[key] = Path(directory)

    output = args.output.resolve()
    if output.exists():
        shutil.rmtree(output)
    output.mkdir(parents=True)
    records: list[dict[str, object]] = []

    def copy_record(module: str, version: str, license_id: str, source: Path) -> None:
        candidates = sorted(
            path for path in source.iterdir() if path.is_file() and LICENSE_NAMES.match(path.name)
        )
        if not candidates:
            raise SystemExit(f"linked module has no root license text: {module}@{version}")
        files: list[dict[str, str]] = []
        prefix = safe_name(f"{module}@{version}")
        for candidate in candidates:
            destination = output / f"{prefix}--{safe_name(candidate.name)}"
            destination.write_bytes(candidate.read_bytes())
            files.append(
                {
                    "name": destination.name,
                    "sha256": hashlib.sha256(destination.read_bytes()).hexdigest(),
                }
            )
        records.append(
            {
                "module": module,
                "version": version,
                "license": license_id,
                "files": files,
            }
        )

    copy_record(
        "github.com/kukuyan/yunpin-ime", "repository", "Apache-2.0", ROOT
    )
    goroot = Path(
        subprocess.run(
            ["go", "env", "GOROOT"],
            check=True,
            text=True,
            encoding="utf-8",
            errors="strict",
            stdout=subprocess.PIPE,
        ).stdout.strip()
    )
    copy_record("go.dev/toolchain", "linked", "BSD-3-Clause", goroot)
    for (module, version), directory in sorted(modules.items()):
        copy_record(module, version, locked[(module, version)], directory)

    manifest = {
        "schemaVersion": 1,
        "artifact": args.artifact,
        "modules": records,
    }
    (output / "LICENSES.json").write_text(
        json.dumps(manifest, ensure_ascii=True, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
