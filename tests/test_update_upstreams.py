# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("update_upstreams", ROOT / "scripts/update_upstreams.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class UpdateUpstreamTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        subprocess.run(["git", "init", "--quiet", str(self.root)], check=True)
        self.lock = self.root / "lock.json"

    def test_manual_release_never_fetches_even_when_directory_exists(self):
        (self.root / "third_party/librime").mkdir(parents=True)
        entry = {"name": "librime", "url": "https://example.invalid/librime", "commit": "a" * 40, "update_policy": "manual-release"}
        self.lock.write_text(json.dumps({"upstreams": [entry]}))
        with mock.patch.object(MODULE, "ROOT", self.root), mock.patch.object(MODULE, "LOCK", self.lock), mock.patch.object(MODULE, "head") as head, mock.patch.object(MODULE.subprocess, "run") as run:
            MODULE.main()
        head.assert_not_called()
        run.assert_not_called()
        self.assertEqual(json.loads(self.lock.read_text())["upstreams"], [entry])

    def test_empty_submodule_does_not_use_parent_git_repository(self):
        checkout = self.root / "third_party/rime-ice"
        checkout.mkdir(parents=True)
        self.lock.write_text(json.dumps({"upstreams": [{"name": "rime-ice", "url": "https://example.invalid/rime-ice", "commit": "a" * 40}]}))
        before = self.lock.read_bytes()
        with mock.patch.object(MODULE, "ROOT", self.root), mock.patch.object(MODULE, "LOCK", self.lock), mock.patch.object(MODULE, "head") as head, mock.patch.object(MODULE.subprocess, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "not initialized"):
                MODULE.main()
        head.assert_not_called()
        run.assert_not_called()
        self.assertEqual(self.lock.read_bytes(), before)

    def test_initialized_public_checkout_updates_only_itself(self):
        checkout = self.root / "third_party/rime-ice"
        checkout.mkdir(parents=True)
        subprocess.run(["git", "init", "--quiet", str(checkout)], check=True)
        self.lock.write_text(json.dumps({"upstreams": [{"name": "rime-ice", "url": "https://example.invalid/rime-ice", "commit": "a" * 40}]}))
        real_run = subprocess.run
        updates = []

        def run_git(args, **kwargs):
            if args[1] in {"fetch", "checkout"}:
                updates.append((args, kwargs["cwd"]))
                return subprocess.CompletedProcess(args, 0)
            return real_run(args, **kwargs)

        with mock.patch.object(MODULE, "ROOT", self.root), mock.patch.object(MODULE, "LOCK", self.lock), mock.patch.object(MODULE, "head", return_value="b" * 40), mock.patch.object(MODULE.subprocess, "run", side_effect=run_git):
            MODULE.main()
        self.assertEqual(updates, [
            (["git", "fetch", "--depth", "1", "origin", "b" * 40], checkout),
            (["git", "checkout", "--detach", "b" * 40], checkout),
        ])
        self.assertEqual(json.loads(self.lock.read_text())["upstreams"][0]["commit"], "b" * 40)

    def test_checkout_git_root_must_match_expected_directory(self):
        checkout = self.root / "third_party/rime-ice"
        checkout.mkdir(parents=True)
        (checkout / ".git").write_text("gitdir: ../../.git\n")
        with mock.patch.object(MODULE.subprocess, "check_output", return_value=str(self.root)):
            with self.assertRaisesRegex(RuntimeError, "another repository"):
                MODULE.require_checkout(checkout)


if __name__ == "__main__":
    unittest.main()
