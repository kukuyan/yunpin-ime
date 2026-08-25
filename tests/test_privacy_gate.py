#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Negative tests for the privacy gate itself.

scripts/check_private_data.sh is the only automated gate that keeps private
vocabulary, replay traces and credential material out of the repository and the
release artifacts. A gate that reports a pass without having scanned anything is
worse than no gate at all, so these cases assert the failure paths rather than
the happy path: a missing scanner, a real-sized private snapshot, and the
file-sync conflict copies that silently break Go builds and patch-series
verification.

Every fixture here is synthetic. No real vocabulary is read or written.
"""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
GATE = ROOT / "scripts" / "check_private_data.sh"


def run_gate(cwd: Path, path_override: str | None = None) -> subprocess.CompletedProcess[str]:
    environment = dict(os.environ)
    if path_override is not None:
        environment["PATH"] = path_override
    return subprocess.run(
        ["bash", str(cwd / "scripts" / "check_private_data.sh")],
        cwd=cwd,
        env=environment,
        capture_output=True,
        text=True,
        check=False,
    )


class PrivacyGateTests(unittest.TestCase):
    """Each case runs the real gate against a disposable repository copy."""

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.repository = Path(self.temporary.name) / "repository"
        (self.repository / "scripts").mkdir(parents=True)
        shutil.copy2(GATE, self.repository / "scripts" / "check_private_data.sh")
        self.addCleanup(self.temporary.cleanup)

    def test_missing_ripgrep_fails_closed(self) -> None:
        """A scan that cannot run must never be reported as a pass."""
        result = run_gate(self.repository, path_override="/usr/bin:/bin")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ripgrep", result.stderr)
        self.assertNotIn("privacy scan passed", result.stdout)

    def test_clean_tree_passes(self) -> None:
        if shutil.which("rg") is None:
            self.skipTest("ripgrep is unavailable on this host")
        result = run_gate(self.repository)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("privacy scan passed", result.stdout)

    def test_oversized_private_vocabulary_is_rejected(self) -> None:
        if shutil.which("rg") is None:
            self.skipTest("ripgrep is unavailable on this host")
        snapshot = self.repository / "yunpin-private.tsv"
        snapshot.write_text("synthetic\tsynthetic\tfixture\t1\n" * 4000, encoding="utf-8")
        result = run_gate(self.repository)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("private vocabulary", result.stderr)

    def test_small_example_template_is_accepted(self) -> None:
        """The committed .example placeholder must stay usable."""
        if shutil.which("rg") is None:
            self.skipTest("ripgrep is unavailable on this host")
        template = self.repository / "platform" / "windows" / "rime"
        template.mkdir(parents=True)
        (template / "yunpin-private.tsv.example").write_text(
            "phrase\tpinyin\tsource\tuse_count\n", encoding="utf-8"
        )
        result = run_gate(self.repository)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_sync_conflict_copies_are_rejected(self) -> None:
        if shutil.which("rg") is None:
            self.skipTest("ripgrep is unavailable on this host")
        (self.repository / "client 2.go").write_text("package main\n", encoding="utf-8")
        result = run_gate(self.repository)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("conflict copies", result.stderr)


if __name__ == "__main__":
    unittest.main()
