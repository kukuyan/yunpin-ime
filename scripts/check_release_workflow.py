#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Static fail-closed contract checks for YunPin preview publishing."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys
from typing import Any, Sequence


ROOT = Path(__file__).resolve().parents[1]
RELEASE_WORKFLOW = ROOT / ".github" / "workflows" / "release.yml"
CI_WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"


RELEASE_SNIPPETS = {
    "workflow_call:": "reusable-only release entry point",
    "release_tag:": "immutable release tag input",
    "release_commit:": "full-CI commit input",
    "permissions:\n  contents: read": "read-only workflow default",
    "release:\n    name: publish-preview-release": "dedicated publisher job",
    "attestations: read": "release-attestation verification permission",
    "contents: write": "publisher-only write permission",
    "cancel-in-progress: false": "non-cancelling upload concurrency",
    "test \"${GITHUB_REF}\" = \"refs/tags/${RELEASE_TAG}\"": "tag-ref identity gate",
    "scripts/generate_release_sbom.py": "offline SPDX generation",
    "scripts/check_release_sbom.py": "SPDX validation",
    "scripts/verify_release_assets.py finalize": "final asset verification",
    "scripts/verify_release_assets.py verify-remote-assets": "remote asset byte verification",
    "YunPin-IME-${RELEASE_TAG}.spdx.json": "tag-bound SPDX asset",
    "persist-credentials: false": "non-persistent checkout credentials",
    "gh release create": "draft release creation",
    "--draft --prerelease --verify-tag": "non-public staged prerelease",
    '"repos/${GITHUB_REPOSITORY}/releases?per_page=100"': "authenticated draft listing",
    "--paginate --slurp": "valid paginated release-list JSON",
    "resolve_owned_draft_with_retry": "bounded draft visibility retry",
    "readonly draft_resolve_attempts=6": "bounded retry attempts",
    "resolve-draft": "unique owned-draft resolution",
    "verify-draft": "owned-draft identity recheck",
    "verify-published": "published title, body, and state recheck",
    '[[ "${release_id}" =~ ^[0-9]+$ ]]': "numeric release ID gate",
    'https://uploads.github.com/repos/${GITHUB_REPOSITORY}/releases/${release_id}/assets': "ID-addressed release asset upload",
    'test "${#assets[@]}" -eq 8': "exact eight-asset release allowlist",
    "draft=false": "atomic public visibility transition",
    "--json isImmutable": "immutable-release publication gate",
    "gh release verify": "GitHub release attestation verification",
    'draft_marker="<!-- ${draft_owner} -->"': "per-run draft ownership marker",
    'cleanup_release_id="$(resolve_owned_draft_with_retry': "fresh cleanup draft resolution",
    "gh api --method DELETE": "failed draft cleanup",
}


class ReleaseWorkflowError(ValueError):
    """The release response does not identify one run-owned draft."""


class DraftNotVisibleError(ReleaseWorkflowError):
    """The exact run-owned draft is not visible in the release list yet."""


def _read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ReleaseWorkflowError(f"invalid release-list JSON: {error}") from error


def _release_rows(payload: Any) -> list[dict[str, Any]]:
    """Flatten `gh api --paginate --slurp` output without accepting objects."""

    if not isinstance(payload, list):
        raise ReleaseWorkflowError("release-list response must be a JSON array")

    rows: list[dict[str, Any]] = []
    for page in payload:
        if not isinstance(page, list):
            raise ReleaseWorkflowError(
                "paginated release-list pages must be JSON arrays"
            )
        for candidate in page:
            if not isinstance(candidate, dict):
                raise ReleaseWorkflowError("release-list rows must be JSON objects")
            rows.append(candidate)
    return rows


def _validated_owned_draft(
    release: dict[str, Any],
    *,
    tag: str,
    title: str,
    owner_marker: str,
    expected_id: str | None = None,
) -> str:
    if release.get("tag_name") != tag:
        raise ReleaseWorkflowError("draft tag does not match this run")
    if release.get("name") != title:
        raise ReleaseWorkflowError("draft title does not match this run")
    if release.get("draft") is not True:
        raise ReleaseWorkflowError("release is not a draft")
    if release.get("prerelease") is not True:
        raise ReleaseWorkflowError("draft is not marked as a prerelease")

    body = release.get("body")
    if not isinstance(body, str) or owner_marker not in {
        line.strip() for line in body.splitlines()
    }:
        raise ReleaseWorkflowError("draft lacks this run's ownership marker")

    return _validated_release_id(release, expected_id=expected_id)


def _validated_release_id(
    release: dict[str, Any], *, expected_id: str | None = None
) -> str:
    release_id = release.get("id")
    # GitHub's REST API emits a positive JSON integer. Reject strings even if
    # they contain digits so command output can never become a shell fragment.
    if type(release_id) is not int or release_id <= 0:  # noqa: E721
        raise ReleaseWorkflowError("draft ID must be a positive JSON integer")
    numeric_id = str(release_id)
    if not numeric_id.isascii() or not numeric_id.isdecimal():
        raise ReleaseWorkflowError("draft ID must contain ASCII digits only")
    if expected_id is not None and numeric_id != expected_id:
        raise ReleaseWorkflowError("draft ID changed during identity recheck")
    return numeric_id


def resolve_owned_draft(
    payload: Any,
    *,
    tag: str,
    title: str,
    owner_marker: str,
) -> str:
    """Resolve exactly one tag+title+draft tuple, then prove run ownership."""

    matches = [
        row
        for row in _release_rows(payload)
        if row.get("tag_name") == tag
        and row.get("name") == title
        and row.get("draft") is True
    ]
    if not matches:
        raise DraftNotVisibleError(
            "run-owned draft is not visible in the authenticated release list"
        )
    if len(matches) != 1:
        raise ReleaseWorkflowError(
            "expected exactly one draft matching tag+title+draft; "
            f"found {len(matches)}"
        )
    return _validated_owned_draft(
        matches[0],
        tag=tag,
        title=title,
        owner_marker=owner_marker,
    )


def verify_owned_draft(
    payload: Any,
    *,
    tag: str,
    title: str,
    owner_marker: str,
    expected_id: str,
) -> str:
    if not isinstance(payload, dict):
        raise ReleaseWorkflowError("release identity response must be a JSON object")
    return _validated_owned_draft(
        payload,
        tag=tag,
        title=title,
        owner_marker=owner_marker,
        expected_id=expected_id,
    )


def verify_published_release(
    payload: Any,
    *,
    tag: str,
    title: str,
    expected_body: str,
    forbidden_marker: str,
    expected_id: str,
) -> str:
    if not isinstance(payload, dict):
        raise ReleaseWorkflowError("published release response must be a JSON object")
    if payload.get("tag_name") != tag:
        raise ReleaseWorkflowError("published release tag does not match this run")
    if payload.get("name") != title:
        raise ReleaseWorkflowError("published release title was not finalized")
    if payload.get("body") != expected_body:
        raise ReleaseWorkflowError("published release body was not finalized exactly")
    if forbidden_marker in expected_body or forbidden_marker in str(payload.get("body")):
        raise ReleaseWorkflowError("published release still contains the draft marker")
    if payload.get("draft") is not False:
        raise ReleaseWorkflowError("published release still reports draft=true")
    if payload.get("prerelease") is not True:
        raise ReleaseWorkflowError("published release is not a prerelease")
    return _validated_release_id(payload, expected_id=expected_id)


CI_SNIPPETS = {
    'tags: ["v*-preview.*"]': "preview tag trigger",
    "python3 scripts/check_release_workflow.py": "PR static release check",
    'python3 -m unittest discover -s tests -p "test_release_*.py" -v': "release verifier tests",
    "python3 -m unittest tests.test_grammar_asset_metadata -v": "mutable grammar asset metadata tests",
    "runs-on: windows-2022": "Windows v143-compatible runner",
    ".\\platform\\windows\\scripts\\Build-Preview.ps1": "Windows package entry point",
    "YunPin-IME-Windows-development-preview.zip": "Windows runtime asset",
    "YunPin-IME-Windows-development-preview-source.zip": "Windows source asset",
    "name: Rebuild extracted Windows source archive offline": "Windows extracted-source acceptance",
    "name: Verify grammar cache exclusive temporary-file safety": "Windows grammar cache reparse negative test",
    "-TestGrammarCacheSafety": "Windows grammar cache safety self-test entry point",
    "-SkipPackage -Offline": "Windows fail-closed offline rebuild mode",
    '$env:GOPROXY = "off"': "Windows offline Go module gate",
    "windows-release-metadata.json": "Windows commit metadata",
    "runs-on: macos-26": "macOS 26 runner",
    'YUNPIN_MACOS_BUILD_JOBS: "2"': "bounded macOS build parallelism",
    "Xcode_26.4.1.app": "pinned Xcode selection",
    "make -C platform/macos dmg BUILD_ROOT=build/macos": "macOS DMG entry point",
    "name: Verify final macOS installer grammar resources": "unconditional macOS installer grammar step",
    "scripts/verify_release_assets.py verify-macos-installer": "expanded macOS installer grammar gate",
    "--evidence build/macos/package/grammar-quality-metrics.json": "external macOS grammar A/B evidence",
    "--commit \"$GITHUB_SHA\"": "macOS grammar evidence commit binding",
    "build/macos/package/grammar-quality-metrics.json": "macOS grammar metrics artifact",
    "YunPin-IME-macOS-development-preview.dmg": "macOS DMG asset",
    "YunPin-IME-development-preview-source.tar.gz": "macOS source asset",
    "macos-release-metadata.json": "macOS commit metadata",
    "name: YunPin-Windows-private-pairing-E2E": "isolated Windows private E2E artifact",
    "path: build/windows/e2e-private/windows": "isolated Windows private E2E path",
    "name: YunPin-macOS-private-pairing-E2E": "isolated macOS private E2E artifact",
    "path: build/macos/e2e-private/macos": "isolated macOS private E2E path",
    "release-preview:\n    name: release-preview": "tag-only caller job",
    "needs: [required, windows-client, macos-client]": "full-CI and two-platform publication gate",
    "attestations: read": "caller release-attestation permission",
    "uses: ./.github/workflows/release.yml": "local reusable publisher",
}


def _uses_values(path: Path, text: str) -> list[tuple[Path, int, str]]:
    values: list[tuple[Path, int, str]] = []
    for number, line in enumerate(text.splitlines(), 1):
        match = re.match(r"^\s*-?\s*uses:\s*([^\s#]+)", line)
        if match:
            values.append((path, number, match.group(1).strip("'\"")))
    return values


def _missing(text: str, snippets: dict[str, str], scope: str) -> list[str]:
    return [
        f"{scope} lacks {description}: {snippet!r}"
        for snippet, description in snippets.items()
        if snippet not in text
    ]


def check_static_contract() -> int:
    missing_files = [
        str(path.relative_to(ROOT))
        for path in (RELEASE_WORKFLOW, CI_WORKFLOW)
        if not path.is_file()
    ]
    if missing_files:
        print("missing workflow: " + ", ".join(missing_files), file=sys.stderr)
        return 1

    release = RELEASE_WORKFLOW.read_text(encoding="utf-8")
    ci = CI_WORKFLOW.read_text(encoding="utf-8")
    errors = _missing(release, RELEASE_SNIPPETS, "release workflow")
    errors.extend(_missing(ci, CI_SNIPPETS, "CI workflow"))

    strict_tag = r"^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$"
    if strict_tag not in release:
        errors.append("release workflow must validate strict vX.Y.Z-preview.N tags")
    if "workflow_dispatch:" in release:
        errors.append("publisher must not bypass full CI through workflow_dispatch")
    if "push:" in release:
        errors.append("publisher must be called by the full CI workflow, not tag-triggered independently")
    if "if: startsWith(github.ref, 'refs/tags/')" not in ci:
        errors.append("CI publisher call must be tag-only")
    windows_source_marker = "      - name: Rebuild extracted Windows source archive offline"
    windows_source_start = ci.find(windows_source_marker)
    if windows_source_start >= 0:
        windows_source_end = ci.find(
            "\n      - ", windows_source_start + len(windows_source_marker)
        )
        if windows_source_end < 0:
            windows_source_end = len(ci)
        windows_source_gate = ci[windows_source_start:windows_source_end]
        for required in (
            "Expand-Archive",
            '-Filter "Build-Preview.ps1"',
            "-SkipPackage -Offline",
            '$env:GOPROXY = "off"',
            '$env:GOSUMDB = "off"',
        ):
            if required not in windows_source_gate:
                errors.append(
                    f"Windows extracted-source acceptance omits {required}"
                )
        if "\n        if:" in windows_source_gate:
            errors.append(
                "Windows extracted-source acceptance must run on ordinary CI"
            )
    macos_gate_marker = "      - name: Verify final macOS installer grammar resources"
    macos_gate_start = ci.find(macos_gate_marker)
    if macos_gate_start >= 0:
        macos_gate_end = ci.find("\n      - ", macos_gate_start + len(macos_gate_marker))
        if macos_gate_end < 0:
            macos_gate_end = len(ci)
        macos_gate = ci[macos_gate_start:macos_gate_end]
        if "verify-macos-installer" not in macos_gate:
            errors.append("macOS installer grammar step does not run its native verifier")
        if "--evidence build/macos/package/grammar-quality-metrics.json" not in macos_gate:
            errors.append("macOS installer grammar step omits external A/B evidence")
        if '--commit "$GITHUB_SHA"' not in macos_gate:
            errors.append("macOS installer grammar evidence is not commit-bound")
        if "\n        if:" in macos_gate:
            errors.append("macOS installer grammar verification must run on ordinary CI branches")
    if "softprops/action-gh-release" in release or "ncipollo/release-action" in release:
        errors.append("release publishing must use the runner's built-in gh CLI")
    for forbidden in ("private-pairing", "e2e-private", "yunpin_pairing_private"):
        if forbidden in release.lower():
            errors.append(
                f"release workflow must not download or publish private E2E material: {forbidden}"
            )

    create_offset = release.find("gh release create")
    if create_offset >= 0:
        after_create = release[create_offset:]
        if "releases/tags/${RELEASE_TAG}" in after_create:
            errors.append(
                "draft publication must not query /releases/tags after creation"
            )
        if "gh release upload" in after_create:
            errors.append(
                "draft assets must upload by numeric release ID, not by tag"
            )
        if "head -n 1" in after_create:
            errors.append("draft resolution must reject ambiguity, not select the first row")

        publish_offset = after_create.find("-F draft=false")
        if publish_offset < 0:
            errors.append("draft publication transition is missing")
        elif "gh release view" in after_create[:publish_offset]:
            errors.append("draft phase must not use tag-based gh release view")

    for path, text in ((RELEASE_WORKFLOW, release), (CI_WORKFLOW, ci)):
        for source, number, value in _uses_values(path, text):
            if value.startswith("./"):
                continue
            if "@" not in value or not re.fullmatch(r"[^@]+@[0-9a-f]{40}", value):
                errors.append(
                    f"{source.relative_to(ROOT)}:{number}: Action must use a full commit SHA: {value}"
                )

    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print("release workflow contract passed: full CI -> draft -> verified prerelease")
    return 0


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command")
    for command in ("resolve-draft", "verify-draft"):
        subparser = subparsers.add_parser(command)
        subparser.add_argument("--response", required=True, type=Path)
        subparser.add_argument("--tag", required=True)
        subparser.add_argument("--title", required=True)
        subparser.add_argument("--owner-marker", required=True)
        if command == "verify-draft":
            subparser.add_argument("--id", required=True)
    published = subparsers.add_parser("verify-published")
    published.add_argument("--response", required=True, type=Path)
    published.add_argument("--tag", required=True)
    published.add_argument("--title", required=True)
    published.add_argument("--notes-file", required=True, type=Path)
    published.add_argument("--forbidden-marker", required=True)
    published.add_argument("--id", required=True)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.command is None:
        return check_static_contract()

    try:
        payload = _read_json(args.response)
        if args.command == "resolve-draft":
            release_id = resolve_owned_draft(
                payload,
                tag=args.tag,
                title=args.title,
                owner_marker=args.owner_marker,
            )
        elif args.command == "verify-draft":
            release_id = verify_owned_draft(
                payload,
                tag=args.tag,
                title=args.title,
                owner_marker=args.owner_marker,
                expected_id=args.id,
            )
        else:
            try:
                expected_body = args.notes_file.read_text(encoding="utf-8")
            except OSError as error:
                raise ReleaseWorkflowError(
                    f"cannot read final release notes: {error}"
                ) from error
            release_id = verify_published_release(
                payload,
                tag=args.tag,
                title=args.title,
                expected_body=expected_body,
                forbidden_marker=args.forbidden_marker,
                expected_id=args.id,
            )
    except DraftNotVisibleError as error:
        print(f"release draft verification pending: {error}", file=sys.stderr)
        return 2
    except ReleaseWorkflowError as error:
        print(f"release draft verification failed: {error}", file=sys.stderr)
        return 1
    print(release_id)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
