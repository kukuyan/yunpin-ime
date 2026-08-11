#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import os
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

import check_release_workflow as workflow  # noqa: E402


TAG = "v0.1.0-preview.1"
TITLE = (
    "YunPin IME 0.1.0-preview.1 (unsigned preview) "
    "[draft yunpin-release-run:31476221080:2:0123456789abcdef0123456789abcdef]"
)
MARKER = (
    "<!-- yunpin-release-run:31476221080:2:"
    "0123456789abcdef0123456789abcdef -->"
)
PUBLIC_TITLE = "YunPin IME 0.1.0-preview.1 (unsigned preview)"
PUBLIC_NOTES = "YunPin unsigned preview.\n"
EXPECTED_SHA = "8f46f71243563bdb369553fbec8059bb13f96631"


FAKE_GH = r'''#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import re
import sys
from urllib.parse import parse_qs, unquote, urlparse

state_path = Path(os.environ["FAKE_GH_STATE"])
scenario = os.environ["FAKE_GH_SCENARIO"]


def load_state():
    return json.loads(state_path.read_text(encoding="utf-8"))


def save_state(state):
    state_path.write_text(json.dumps(state, sort_keys=True), encoding="utf-8")


def value_after(arguments, option):
    return arguments[arguments.index(option) + 1]


def api_request(arguments):
    method = "GET"
    input_path = None
    raw_fields = []
    typed_fields = []
    endpoint = None
    index = 1
    while index < len(arguments):
        token = arguments[index]
        if token in {"--method", "-H", "--input", "-f", "-F"}:
            value = arguments[index + 1]
            if token == "--method":
                method = value
            elif token == "--input":
                input_path = value
            elif token == "-f":
                raw_fields.append(value)
            elif token == "-F":
                typed_fields.append(value)
            index += 2
        elif token in {"--paginate", "--slurp", "--silent"}:
            index += 1
        elif token.startswith("-"):
            raise SystemExit(f"unsupported fake-gh option: {token}")
        else:
            if endpoint is not None:
                raise SystemExit("multiple API endpoints")
            endpoint = token
            index += 1
    if endpoint is None:
        raise SystemExit("missing API endpoint")
    return method, endpoint, input_path, raw_fields, typed_fields


def typed_value(value):
    if value == "true":
        return True
    if value == "false":
        return False
    if value == "null":
        return None
    if re.fullmatch(r"-?[0-9]+", value):
        return int(value)
    if value.startswith("@"):
        return Path(value[1:]).read_text(encoding="utf-8")
    return value


def field_document(raw_fields, typed_fields):
    document = {}
    for raw in raw_fields:
        key, value = raw.split("=", 1)
        document[key] = value
    for typed in typed_fields:
        key, value = typed.split("=", 1)
        document[key] = typed_value(value)
    return document


def release_by_id(state, release_id):
    for release in state["releases"]:
        if release["id"] == release_id:
            return release
    raise SystemExit(1)


arguments = sys.argv[1:]
state = load_state()
state["calls"].append(arguments)

if arguments[:2] == ["release", "create"]:
    tag = arguments[2]
    title = value_after(arguments, "--title")
    body = Path(value_after(arguments, "--notes-file")).read_text(encoding="utf-8")
    owned = {
        "id": 501,
        "tag_name": tag,
        "name": title,
        "body": body,
        "draft": True,
        "prerelease": True,
    }
    if scenario in {"success", "patch_remote_wrong_name"}:
        state["releases"].append(owned)
    elif scenario == "create_nonzero_owned":
        state["releases"].extend([
            {
                **owned,
                "id": 600,
                "name": "External operator draft",
                "body": "external\n",
            },
            owned,
        ])
    elif scenario == "external_only":
        state["releases"].append({
            **owned, "id": 601, "name": "External operator draft", "body": "external\n"
        })
    elif scenario == "same_identity_no_marker":
        state["releases"].append({**owned, "id": 602, "body": "external\n"})
    elif scenario == "duplicate_owned":
        state["releases"].extend([owned, {**owned, "id": 502}])
    else:
        raise SystemExit(f"unknown scenario: {scenario}")
    save_state(state)
    if scenario not in {"success", "patch_remote_wrong_name"}:
        raise SystemExit(41)
    print("https://example.invalid/releases/untagged-owned")
    raise SystemExit(0)

if arguments[:2] == ["release", "view"]:
    published = next((row for row in state["releases"] if not row["draft"]), None)
    if published is None:
        raise SystemExit(1)
    field = value_after(arguments, "--json")
    values = {
        "isDraft": published["draft"],
        "isPrerelease": published["prerelease"],
        "isImmutable": True,
    }
    print(json.dumps(values[field]))
    save_state(state)
    raise SystemExit(0)

if arguments[:2] == ["release", "verify"]:
    save_state(state)
    raise SystemExit(0)

if not arguments or arguments[0] != "api":
    raise SystemExit(f"unsupported fake-gh command: {arguments}")

method, endpoint, input_path, raw_fields, typed_fields = api_request(arguments)
state["api_calls"].append({"method": method, "endpoint": endpoint})

if method == "GET" and endpoint.endswith("/releases?per_page=100"):
    print(json.dumps([state["releases"]]))
elif method == "POST" and endpoint.startswith("https://uploads.github.com/"):
    parsed = urlparse(endpoint)
    release_id = int(re.search(r"/releases/([0-9]+)/assets$", parsed.path).group(1))
    release_by_id(state, release_id)
    asset = Path(input_path)
    name = unquote(parse_qs(parsed.query)["name"][0])
    if name != asset.name:
        raise SystemExit("asset name mismatch")
    state["assets"].append({
        "name": name,
        "state": "uploaded",
        "size": asset.stat().st_size,
        "digest": "sha256:" + hashlib.sha256(asset.read_bytes()).hexdigest(),
    })
    print("{}")
elif method == "GET" and re.search(r"/releases/[0-9]+/assets\?per_page=100$", endpoint):
    print(json.dumps(state["assets"]))
elif method == "GET" and re.search(r"/releases/[0-9]+$", endpoint):
    release_id = int(endpoint.rsplit("/", 1)[1])
    print(json.dumps(release_by_id(state, release_id)))
elif method == "DELETE" and re.search(r"/releases/[0-9]+$", endpoint):
    release_id = int(endpoint.rsplit("/", 1)[1])
    release_by_id(state, release_id)
    state["deleted"].append(release_id)
    state["releases"] = [row for row in state["releases"] if row["id"] != release_id]
    print("{}")
elif method == "PATCH" and re.search(r"/releases/[0-9]+$", endpoint):
    release_id = int(endpoint.rsplit("/", 1)[1])
    payload = field_document(raw_fields, typed_fields)
    state["patch_payload"] = payload
    release = release_by_id(state, release_id)
    release.update(payload)
    if scenario == "patch_remote_wrong_name":
        release["name"] = "tampered remote title"
    print(json.dumps(release))
else:
    raise SystemExit(f"unsupported fake API request: {method} {endpoint}")

save_state(state)
'''


FAKE_GIT = r'''#!/usr/bin/env python3
import os
import sys

arguments = sys.argv[1:]
if arguments and arguments[0] == "fetch":
    raise SystemExit(0)
if arguments[:3] == ["rev-list", "-n", "1"]:
    print(os.environ["EXPECTED_SHA"])
    raise SystemExit(0)
raise SystemExit(f"unsupported fake-git command: {arguments}")
'''


def draft(release_id: object = 123456, **changes: object) -> dict[str, object]:
    row: dict[str, object] = {
        "id": release_id,
        "tag_name": TAG,
        "name": TITLE,
        "draft": True,
        "prerelease": True,
        "body": f"Unsigned preview notes.\n\n{MARKER}\n",
    }
    row.update(changes)
    return row


def publish_script() -> str:
    lines = workflow.RELEASE_WORKFLOW.read_text(encoding="utf-8").splitlines()
    step = lines.index("      - name: Publish complete unsigned prerelease")
    run = lines.index("        run: |", step)
    body: list[str] = []
    for line in lines[run + 1 :]:
        if line and not line.startswith("          "):
            break
        body.append(line[10:] if line else "")
    return "\n".join(body) + "\n"


def run_publish_scenario(scenario: str) -> tuple[subprocess.CompletedProcess[str], dict]:
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        (root / "scripts").mkdir()
        shutil.copy2(
            ROOT / "scripts" / "check_release_workflow.py",
            root / "scripts" / "check_release_workflow.py",
        )
        shutil.copy2(
            ROOT / "scripts" / "verify_release_assets.py",
            root / "scripts" / "verify_release_assets.py",
        )

        release_dist = root / "build" / "release-dist"
        release_dist.mkdir(parents=True)
        (release_dist / "RELEASE-NOTES.md").write_text(
            PUBLIC_NOTES, encoding="utf-8"
        )
        for index in range(7):
            (release_dist / f"asset-{index}.bin").write_bytes(
                f"asset-{index}\n".encode("utf-8")
            )

        fake_bin = root / "fake-bin"
        fake_bin.mkdir()
        for name, source in (("gh", FAKE_GH), ("git", FAKE_GIT)):
            executable = fake_bin / name
            executable.write_text(textwrap.dedent(source), encoding="utf-8")
            executable.chmod(0o755)

        runner_temp = root / "runner-temp"
        runner_temp.mkdir()
        state_path = root / "fake-gh-state.json"
        state_path.write_text(
            json.dumps(
                {
                    "releases": [],
                    "assets": [],
                    "deleted": [],
                    "calls": [],
                    "api_calls": [],
                    "patch_payload": None,
                }
            ),
            encoding="utf-8",
        )
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{fake_bin}{os.pathsep}{environment['PATH']}",
                "EXPECTED_SHA": EXPECTED_SHA,
                "RELEASE_TAG": TAG,
                "GITHUB_RUN_ID": "31476221080",
                "GITHUB_RUN_ATTEMPT": "2",
                "GITHUB_REPOSITORY": "kukuyan/yunpin-ime",
                "RUNNER_TEMP": str(runner_temp),
                "GH_TOKEN": "synthetic-test-token",
                "FAKE_GH_STATE": str(state_path),
                "FAKE_GH_SCENARIO": scenario,
                "PYTHONDONTWRITEBYTECODE": "1",
            }
        )
        completed = subprocess.run(
            ["bash", "-c", publish_script()],
            cwd=root,
            env=environment,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        state = json.loads(state_path.read_text(encoding="utf-8"))
        return completed, state


class ReleaseDraftResolutionTests(unittest.TestCase):
    def test_authenticated_paginated_list_resolves_owned_draft(self) -> None:
        response = [
            [
                draft(
                    111,
                    tag_name=TAG,
                    name="unrelated external draft",
                    body="no ownership marker",
                )
            ],
            [draft(987654321)],
        ]
        self.assertEqual(
            "987654321",
            workflow.resolve_owned_draft(
                response, tag=TAG, title=TITLE, owner_marker=MARKER
            ),
        )

    def test_tag_endpoint_404_json_cannot_be_misread_as_a_draft_id(self) -> None:
        # This is the shape returned by GET /releases/tags/{tag} for a draft.
        response = {"message": "Not Found", "status": "404"}
        with self.assertRaisesRegex(
            workflow.ReleaseWorkflowError, "must be a JSON array"
        ):
            workflow.resolve_owned_draft(
                response, tag=TAG, title=TITLE, owner_marker=MARKER
            )

    def test_wrong_json_and_shell_polluted_id_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            response = Path(directory) / "response.json"
            response.write_text('[{"id": 123}', encoding="utf-8")
            with self.assertRaisesRegex(
                workflow.ReleaseWorkflowError, "invalid release-list JSON"
            ):
                workflow._read_json(response)

        for bad_id in (
            "123\n456",
            "123; gh api --method DELETE repos/example/other/releases/9",
            True,
            -1,
        ):
            with self.subTest(release_id=bad_id):
                with self.assertRaisesRegex(
                    workflow.ReleaseWorkflowError,
                    "positive JSON integer",
                ):
                    workflow.resolve_owned_draft(
                        [[draft(bad_id)]],
                        tag=TAG,
                        title=TITLE,
                        owner_marker=MARKER,
                    )

    def test_same_identity_race_is_ambiguous_and_never_selects_first(self) -> None:
        response = [[draft(100), draft(200)]]
        with self.assertRaisesRegex(
            workflow.ReleaseWorkflowError,
            "exactly one draft matching tag\+title\+draft; found 2",
        ):
            workflow.resolve_owned_draft(
                response, tag=TAG, title=TITLE, owner_marker=MARKER
            )

    def test_unique_external_draft_without_run_marker_is_rejected(self) -> None:
        response = [[draft(300, body="matching identity, but created elsewhere")]]
        with self.assertRaisesRegex(
            workflow.ReleaseWorkflowError, "ownership marker"
        ):
            workflow.resolve_owned_draft(
                response, tag=TAG, title=TITLE, owner_marker=MARKER
            )

    def test_identity_recheck_requires_same_numeric_id(self) -> None:
        with self.assertRaisesRegex(workflow.ReleaseWorkflowError, "ID changed"):
            workflow.verify_owned_draft(
                draft(321),
                tag=TAG,
                title=TITLE,
                owner_marker=MARKER,
                expected_id="123",
            )

    def test_resolver_cli_emits_only_ascii_numeric_id(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            response = Path(directory) / "response.json"
            response.write_text(json.dumps([[draft(456789)]]), encoding="utf-8")
            completed = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "scripts" / "check_release_workflow.py"),
                    "resolve-draft",
                    "--response",
                    str(response),
                    "--tag",
                    TAG,
                    "--title",
                    TITLE,
                    "--owner-marker",
                    MARKER,
                ],
                check=True,
                capture_output=True,
                text=True,
            )
        self.assertEqual("456789\n", completed.stdout)
        self.assertEqual("", completed.stderr)

    def test_published_identity_checks_exact_title_body_and_typed_state(self) -> None:
        published = {
            "id": 501,
            "tag_name": TAG,
            "name": PUBLIC_TITLE,
            "body": PUBLIC_NOTES,
            "draft": False,
            "prerelease": True,
        }
        self.assertEqual(
            "501",
            workflow.verify_published_release(
                published,
                tag=TAG,
                title=PUBLIC_TITLE,
                expected_body=PUBLIC_NOTES,
                forbidden_marker=MARKER,
                expected_id="501",
            ),
        )
        for field, value in (
            ("name", "wrong title"),
            ("body", "wrong body\n"),
            ("draft", "false"),
            ("draft", True),
            ("prerelease", "true"),
            ("prerelease", False),
        ):
            with self.subTest(field=field, value=value):
                changed = {**published, field: value}
                with self.assertRaises(workflow.ReleaseWorkflowError):
                    workflow.verify_published_release(
                        changed,
                        tag=TAG,
                        title=PUBLIC_TITLE,
                        expected_body=PUBLIC_NOTES,
                        forbidden_marker=MARKER,
                        expected_id="501",
                    )


@unittest.skipUnless(
    os.name == "posix" and shutil.which("bash") is not None,
    "workflow state-machine harness requires a POSIX Bash runner",
)
class ReleaseWorkflowStateMachineTests(unittest.TestCase):
    def test_create_error_after_server_create_deletes_only_owned_draft(self) -> None:
        completed, state = run_publish_scenario("create_nonzero_owned")
        self.assertNotEqual(0, completed.returncode)
        self.assertEqual([501], state["deleted"])
        self.assertEqual([600], [release["id"] for release in state["releases"]])
        delete_calls = [
            call for call in state["api_calls"] if call["method"] == "DELETE"
        ]
        self.assertEqual(
            [
                {
                    "method": "DELETE",
                    "endpoint": "repos/kukuyan/yunpin-ime/releases/501",
                }
            ],
            delete_calls,
        )

    def test_external_markerless_and_duplicate_drafts_are_never_deleted(self) -> None:
        for scenario in ("external_only", "same_identity_no_marker", "duplicate_owned"):
            with self.subTest(scenario=scenario):
                completed, state = run_publish_scenario(scenario)
                self.assertNotEqual(0, completed.returncode)
                self.assertEqual([], state["deleted"])
                self.assertFalse(
                    any(call["method"] == "DELETE" for call in state["api_calls"])
                )
                self.assertGreaterEqual(len(state["releases"]), 1)

    def test_patch_is_typed_marker_free_and_remotely_reverified(self) -> None:
        completed, state = run_publish_scenario("success")
        self.assertEqual(0, completed.returncode, completed.stderr)
        payload = state["patch_payload"]
        self.assertEqual(PUBLIC_TITLE, payload["name"])
        self.assertEqual(PUBLIC_NOTES, payload["body"])
        self.assertIs(payload["draft"], False)
        self.assertIs(payload["prerelease"], True)
        self.assertNotIn("yunpin-release-run:", payload["name"])
        self.assertNotIn("yunpin-release-run:", payload["body"])

        self.assertEqual(1, len(state["releases"]))
        remote = state["releases"][0]
        self.assertEqual(PUBLIC_TITLE, remote["name"])
        self.assertEqual(PUBLIC_NOTES, remote["body"])
        self.assertIs(remote["draft"], False)
        self.assertIs(remote["prerelease"], True)
        self.assertEqual([], state["deleted"])

    def test_tampered_remote_identity_fails_after_patch(self) -> None:
        completed, state = run_publish_scenario("patch_remote_wrong_name")
        self.assertNotEqual(0, completed.returncode)
        self.assertIn("published release title was not finalized", completed.stderr)
        self.assertEqual([], state["deleted"])


class ReleaseWorkflowStaticTests(unittest.TestCase):
    def test_post_create_path_never_resolves_or_uploads_by_tag(self) -> None:
        release = workflow.RELEASE_WORKFLOW.read_text(encoding="utf-8")
        after_create = release[release.index("gh release create") :]
        draft_phase = after_create[: after_create.index("-F draft=false")]
        self.assertNotIn("releases/tags/${RELEASE_TAG}", after_create)
        self.assertNotIn("gh release upload", after_create)
        self.assertNotIn("gh release view", draft_phase)
        self.assertNotIn("head -n 1", after_create)
        self.assertIn(
            "https://uploads.github.com/repos/${GITHUB_REPOSITORY}/releases/"
            "${release_id}/assets",
            after_create,
        )

    def test_cleanup_uses_only_freshly_resolved_and_reverified_id(self) -> None:
        release = workflow.RELEASE_WORKFLOW.read_text(encoding="utf-8")
        cleanup = release.split("cleanup_failed_draft() {", 1)[1].split(
            "trap cleanup_failed_draft EXIT", 1
        )[0]
        self.assertIn("cleanup_release_id=\"$(python3", cleanup)
        self.assertIn("resolve-draft", cleanup)
        self.assertIn("verify-draft", cleanup)
        self.assertIn("releases/${cleanup_release_id}", cleanup)
        self.assertNotIn("releases/${release_id}", cleanup)
        self.assertNotIn("head -n 1", cleanup)


if __name__ == "__main__":
    unittest.main()
