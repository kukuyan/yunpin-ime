#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

import json
from pathlib import Path
import re
import sys


ROOT = Path(__file__).resolve().parents[1]
LOCK = ROOT / "third_party" / "upstreams.lock.json"
GO_LICENSE_LOCK = ROOT / "third_party" / "go-modules.lock.json"
GO_MODULE_DIRS = (
    "protocol",
    "localstore",
    "sync",
    "syncclient",
    "desktopagent",
    "integration",
)
APPROVED_LICENSES = {
    "Apache-2.0",
    "BSD-2-Clause",
    "BSD-3-Clause",
    "ISC",
    "MIT",
    "MPL-2.0",
}
SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")


def check_grammar_model_license(errors: list[str]) -> None:
    paths = (
        ROOT / "platform" / "macos" / "dependencies.lock.json",
        ROOT / "platform" / "windows" / "dependencies.lock.json",
    )
    try:
        models = [json.loads(path.read_text(encoding="utf-8"))["grammarModel"] for path in paths]
    except (OSError, KeyError, json.JSONDecodeError, TypeError) as error:
        errors.append(f"grammar model license lock is unreadable: {type(error).__name__}")
        return
    if models[0] != models[1]:
        errors.append("grammar model license metadata differs between platform locks")
        return
    model = models[0]
    if model.get("license") != "CC-BY-4.0":
        errors.append("grammar model must retain the reviewed CC-BY-4.0 declaration")
    snapshot = model.get("sourceSnapshotAtAssetUpdate")
    expected_url = (
        "https://raw.githubusercontent.com/amzxyz/RIME-LMDG/"
        f"{snapshot}/LICENSE"
    )
    if model.get("licenseUrl") != expected_url:
        errors.append(
            "grammar model license source is not bound to its reviewed observed snapshot"
        )
    if not SHA256_PATTERN.fullmatch(str(model.get("licenseSha256", ""))):
        errors.append("grammar model license lacks an exact SHA-256")
    if not isinstance(model.get("licenseSize"), int) or model["licenseSize"] <= 0:
        errors.append("grammar model license lacks an exact positive byte size")
    notices = (ROOT / "THIRD_PARTY_NOTICES.md").read_text(encoding="utf-8")
    matrix = (ROOT / "docs" / "LICENSE_MATRIX.md").read_text(encoding="utf-8")
    for label, document in (("third-party notices", notices), ("license matrix", matrix)):
        if "wanxiang-lts-zh-hans" not in document or "CC-BY-4.0" not in document:
            errors.append(f"grammar model is missing from {label}")


def parse_go_sum(path: Path) -> set[tuple[str, str]]:
    locked: set[tuple[str, str]] = set()
    for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        fields = raw.split()
        if len(fields) != 3:
            raise ValueError(f"{path.relative_to(ROOT)}:{number}: malformed go.sum row")
        module, version = fields[0], fields[1]
        if version.endswith("/go.mod"):
            version = version[: -len("/go.mod")]
        locked.add((module, version))
    return locked


def parse_go_mod(path: Path) -> tuple[set[tuple[str, str]], dict[tuple[str, str], str]]:
    required: set[tuple[str, str]] = set()
    replacements_by_module: dict[str, str] = {}
    block = ""
    for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.split("//", 1)[0].strip()
        if not line:
            continue
        if line in {"require (", "replace ("}:
            block = line.split()[0]
            continue
        if line == ")":
            block = ""
            continue
        kind = block
        content = line
        if not block and line.startswith("require "):
            kind, content = "require", line[len("require ") :].strip()
        elif not block and line.startswith("replace "):
            kind, content = "replace", line[len("replace ") :].strip()
        if kind == "require":
            fields = content.split()
            if len(fields) < 2:
                raise ValueError(f"{path.relative_to(ROOT)}:{number}: malformed require")
            required.add((fields[0], fields[1]))
        elif kind == "replace":
            if "=>" not in content:
                raise ValueError(f"{path.relative_to(ROOT)}:{number}: malformed replace")
            left, right = (part.strip() for part in content.split("=>", 1))
            left_fields = left.split()
            right_fields = right.split()
            if not left_fields or len(right_fields) != 1:
                raise ValueError(f"{path.relative_to(ROOT)}:{number}: unsupported replace")
            replacements_by_module[left_fields[0]] = right_fields[0]
    replacements = {
        pair: replacements_by_module[pair[0]]
        for pair in required
        if pair[0] in replacements_by_module
    }
    return required, replacements


def check_go_license_lock(errors: list[str]) -> int:
    try:
        document = json.loads(GO_LICENSE_LOCK.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        errors.append(f"go module license lock is unreadable: {type(exc).__name__}")
        return 0
    if document.get("format") != 1:
        errors.append("go module license lock: unsupported format")
    rows = document.get("modules")
    local_rows = document.get("local_replacements")
    if not isinstance(rows, list) or not isinstance(local_rows, list):
        errors.append("go module license lock: modules/local_replacements must be lists")
        return 0

    manifest: dict[tuple[str, str], dict] = {}
    for row in rows:
        if not isinstance(row, dict):
            errors.append("go module license lock: malformed module row")
            continue
        key = (row.get("module"), row.get("version"))
        if not all(isinstance(value, str) and value for value in key):
            errors.append("go module license lock: missing module/version")
            continue
        if key in manifest:
            errors.append(f"go module license lock: duplicate {key[0]}@{key[1]}")
            continue
        license_id = row.get("license")
        if license_id not in APPROVED_LICENSES:
            errors.append(f"{key[0]}@{key[1]}: unapproved or missing SPDX license {license_id!r}")
        source = row.get("license_source")
        if not isinstance(source, str) or not source.startswith("https://"):
            errors.append(f"{key[0]}@{key[1]}: missing immutable HTTPS license source")
        manifest[key] = row

    local_manifest: dict[tuple[str, str], dict] = {}
    repository_license = (ROOT / "LICENSE").resolve()
    for row in local_rows:
        if not isinstance(row, dict):
            errors.append("go module license lock: malformed local replacement row")
            continue
        key = (row.get("module"), row.get("version"))
        if not all(isinstance(value, str) and value for value in key):
            errors.append("go module license lock: local replacement missing module/version")
            continue
        if key in local_manifest:
            errors.append(f"go module license lock: duplicate local replacement {key[0]}@{key[1]}")
            continue
        if row.get("license") != "Apache-2.0":
            errors.append(f"{key[0]}@{key[1]}: local replacement must declare Apache-2.0")
        source = row.get("license_source")
        if not isinstance(source, str) or not source or Path(source).is_absolute():
            errors.append(f"{key[0]}@{key[1]}: local license source must be repository-relative")
        else:
            resolved_source = (ROOT / source).resolve()
            if resolved_source != repository_license or not resolved_source.is_file():
                errors.append(
                    f"{key[0]}@{key[1]}: local license source must resolve to LICENSE"
                )
        local_manifest[key] = row

    locked: set[tuple[str, str]] = set()
    required_external: set[tuple[str, str]] = set()
    observed_local: dict[tuple[str, str], list[tuple[str, Path]]] = {}
    for module_dir in GO_MODULE_DIRS:
        base = ROOT / module_dir
        try:
            locked.update(parse_go_sum(base / "go.sum"))
            required, replacements = parse_go_mod(base / "go.mod")
        except (OSError, ValueError) as exc:
            errors.append(str(exc))
            continue
        for pair in required:
            replacement = replacements.get(pair)
            if replacement and replacement.startswith("."):
                observed_local.setdefault(pair, []).append((replacement, base))
            else:
                required_external.add(pair)

    for pair in sorted(required_external - locked):
        errors.append(f"{pair[0]}@{pair[1]}: required module is absent from go.sum locks")
    for pair in sorted(locked - set(manifest)):
        errors.append(f"{pair[0]}@{pair[1]}: locked module missing from go-modules.lock.json")
    for pair in sorted(set(manifest) - locked):
        errors.append(f"{pair[0]}@{pair[1]}: stale module in go-modules.lock.json")
    for pair, replacement_locations in sorted(observed_local.items()):
        row = local_manifest.get(pair)
        if row is None:
            errors.append(f"{pair[0]}@{pair[1]}: local replacement missing from license lock")
            continue
        for replacement, module_base in replacement_locations:
            if row.get("replacement") != replacement:
                errors.append(
                    f"{pair[0]}@{pair[1]}: replacement {replacement} in "
                    f"{module_base.relative_to(ROOT)}/go.mod does not match license lock "
                    f"{row.get('replacement')}"
                )
            resolved = (module_base / Path(replacement)).resolve()
            if not resolved.is_dir() or ROOT not in resolved.parents:
                errors.append(
                    f"{pair[0]}@{pair[1]}: local replacement in "
                    f"{module_base.relative_to(ROOT)}/go.mod escapes or is missing"
                )
    for pair in sorted(set(local_manifest) - set(observed_local)):
        errors.append(f"{pair[0]}@{pair[1]}: stale local replacement in license lock")
    return len(locked)


def main() -> int:
    data = json.loads(LOCK.read_text(encoding="utf-8"))
    errors: list[str] = []
    check_grammar_model_license(errors)
    for item in data.get("upstreams", []):
        if len(item.get("commit", "")) != 40:
            errors.append(f"{item.get('name')}: commit is not a full SHA")
        if not item.get("license"):
            errors.append(f"{item.get('name')}: missing license")
        if not item.get("url", "").startswith("https://github.com/"):
            errors.append(f"{item.get('name')}: non-GitHub or insecure URL")
    required = {
        "rime-ice",
        "rime-essay",
        "THUOCL",
        "phrase-pinyin-data",
        "librime",
        "librime-octagram",
        "weasel",
        "squirrel",
        "imewlconverter",
    }
    names = {item.get("name") for item in data.get("upstreams", [])}
    errors.extend(f"missing upstream: {name}" for name in sorted(required - names))
    platform_lock = json.loads((ROOT / "platform" / "upstream-lock.json").read_text(encoding="utf-8"))
    root_commits = {item["name"].lower(): item["commit"] for item in data["upstreams"]}
    for component in platform_lock.get("components", []):
        key = component["name"].lower()
        if key in root_commits and root_commits[key] != component["commit"]:
            errors.append(
                f"{component['name']}: platform commit {component['commit']} "
                f"does not match root lock {root_commits[key]}"
            )
        matching = next(
            (item for item in data["upstreams"] if item["name"].lower() == key),
            None,
        )
        if matching is not None and matching.get("license") != component.get("license"):
            errors.append(
                f"{component['name']}: platform license {component.get('license')} "
                f"does not match root lock {matching.get('license')}"
            )
    go_module_count = check_go_license_lock(errors)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"license lock passed: {len(names)} pinned upstreams, {go_module_count} Go module versions")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
