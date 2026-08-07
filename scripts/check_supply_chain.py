#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Offline checks for immutable container and GitHub Actions references."""

from __future__ import annotations

from pathlib import Path
import re
import sys


ROOT = Path(__file__).resolve().parents[1]
EXCLUDED_TOP_LEVEL = {".git", ".cache", "build", "dist", "third_party"}
FULL_SHA = re.compile(r"[0-9a-f]{40}")
DIGEST = re.compile(r"[0-9a-f]{64}")


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


def main() -> int:
    errors: list[str] = []
    from_count = check_dockerfiles(errors)
    action_count = check_actions(errors)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"supply-chain pins passed: {from_count} FROM instructions, {action_count} Actions references")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
