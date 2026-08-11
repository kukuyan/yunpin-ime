#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Static fail-closed contract checks for YunPin preview publishing."""

from __future__ import annotations

from pathlib import Path
import re
import sys


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
    "gh release upload": "release asset upload",
    "draft=false": "atomic public visibility transition",
    "--json isImmutable": "immutable-release publication gate",
    "gh release verify": "GitHub release attestation verification",
    "(.draft|tostring)": "draft-only failure cleanup gate",
    "gh api --method DELETE": "failed draft cleanup",
}

CI_SNIPPETS = {
    'tags: ["v*-preview.*"]': "preview tag trigger",
    "python3 scripts/check_release_workflow.py": "PR static release check",
    'python3 -m unittest discover -s tests -p "test_release_*.py" -v': "release verifier tests",
    "runs-on: windows-2022": "Windows v143-compatible runner",
    ".\\platform\\windows\\scripts\\Build-Preview.ps1": "Windows package entry point",
    "YunPin-IME-Windows-development-preview.zip": "Windows runtime asset",
    "YunPin-IME-Windows-development-preview-source.zip": "Windows source asset",
    "windows-release-metadata.json": "Windows commit metadata",
    "runs-on: macos-26": "macOS 26 runner",
    'YUNPIN_MACOS_BUILD_JOBS: "2"': "bounded macOS build parallelism",
    "Xcode_26.4.1.app": "pinned Xcode selection",
    "make -C platform/macos dmg BUILD_ROOT=build/macos": "macOS DMG entry point",
    "YunPin-IME-macOS-development-preview.dmg": "macOS DMG asset",
    "YunPin-IME-development-preview-source.tar.gz": "macOS source asset",
    "macos-release-metadata.json": "macOS commit metadata",
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


def main() -> int:
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
    if "softprops/action-gh-release" in release or "ncipollo/release-action" in release:
        errors.append("release publishing must use the runner's built-in gh CLI")

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


if __name__ == "__main__":
    raise SystemExit(main())
