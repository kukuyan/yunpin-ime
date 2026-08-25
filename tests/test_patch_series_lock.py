#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Bind every ordered GPL patch directory to its dependency lock.

Two properties matter, and the platforms enforced different subsets of them:

  * every locked patch is present and intact -- both platforms hashed this;
  * the directory contains nothing the lock does not mention -- only macOS
    compared the directory listing, which is what made it refuse to build when
    a file-sync client dropped a directory full of " 2.patch" conflict copies.
    Windows enumerated the lock instead, so an unlocked patch would have been
    applied to nobody's knowledge, or silently skipped.

Checking both properties here means the invariant is verified on every CI run
rather than only when the platform's own build script happens to execute.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
MACOS_LOCK = ROOT / "platform" / "macos" / "dependencies.lock.json"
WINDOWS_LOCK = ROOT / "platform" / "windows" / "dependencies.lock.json"


def locked_rows(lock: Path, *keys: str) -> list[dict[str, str]]:
    data = json.loads(lock.read_text(encoding="utf-8"))
    for key in keys:
        data = data[key]
    return list(data)


class PatchSeriesLockTests(unittest.TestCase):
    def assert_series_matches(self, rows: list[dict[str, str]], directory: Path) -> None:
        self.assertTrue(rows, f"dependency lock records no patches for {directory}")
        self.assertTrue(directory.is_dir(), f"patch directory is missing: {directory}")

        on_disk = sorted(path.name for path in directory.glob("*.patch"))
        locked = sorted(Path(row["path"]).name for row in rows)
        self.assertEqual(
            on_disk,
            locked,
            f"{directory.relative_to(ROOT)} does not match its lock; an unlocked "
            f"patch or a file-sync conflict copy changes what gets applied",
        )

        for row in rows:
            path = ROOT / row["path"]
            self.assertTrue(path.is_file(), f"locked patch is missing: {row['path']}")
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            self.assertEqual(
                digest, row["sha256"], f"locked patch digest differs: {row['path']}"
            )

    def test_macos_squirrel_series(self) -> None:
        self.assert_series_matches(
            locked_rows(MACOS_LOCK, "squirrel_patches"),
            ROOT / "platform" / "patches" / "squirrel",
        )

    def test_macos_librime_series(self) -> None:
        rows = locked_rows(MACOS_LOCK, "librime_patches")
        directory = (ROOT / rows[0]["path"]).parent
        self.assert_series_matches(rows, directory)

    def test_windows_weasel_series(self) -> None:
        self.assert_series_matches(
            locked_rows(WINDOWS_LOCK, "weasel", "patches"),
            ROOT / "platform" / "patches" / "weasel",
        )

    def test_windows_librime_series(self) -> None:
        rows = locked_rows(WINDOWS_LOCK, "librime", "patches")
        directory = (ROOT / rows[0]["path"]).parent
        self.assert_series_matches(rows, directory)

    def test_patch_ordering_is_stable(self) -> None:
        """The series is applied in lock order, so the lock must stay sorted.

        A patch inserted in the middle of the numbering but appended at the end
        of the lock would apply in a different order than its filename implies.
        """
        for rows in (
            locked_rows(MACOS_LOCK, "squirrel_patches"),
            locked_rows(MACOS_LOCK, "librime_patches"),
            locked_rows(WINDOWS_LOCK, "weasel", "patches"),
            locked_rows(WINDOWS_LOCK, "librime", "patches"),
        ):
            names = [Path(row["path"]).name for row in rows]
            self.assertEqual(names, sorted(names), f"lock order is not filename order: {names}")


if __name__ == "__main__":
    unittest.main()
