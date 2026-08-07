#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

import json
from pathlib import Path
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]


def main() -> int:
    lock = json.loads((ROOT / "third_party/upstreams.lock.json").read_text(encoding="utf-8"))
    expected = {item["name"]: item["commit"] for item in lock["upstreams"]}
    path_to_name = {
        "third_party/librime": "librime",
        "third_party/weasel": "weasel",
        "third_party/squirrel": "squirrel",
        "third_party/rime-ice": "rime-ice",
        "third_party/rime-essay": "rime-essay",
        "third_party/THUOCL": "THUOCL",
        "third_party/phrase-pinyin-data": "phrase-pinyin-data",
        "third_party/imewlconverter": "imewlconverter",
    }
    errors: list[str] = []
    for path, name in path_to_name.items():
        process = subprocess.run(
            ["git", "ls-files", "--stage", "--", path],
            cwd=ROOT,
            text=True,
            capture_output=True,
            check=False,
        )
        if not process.stdout.strip():
            # The bootstrap working tree can be checked before the initial index
            # is assembled; CI and every committed revision must contain it.
            if (ROOT / ".git").exists() and subprocess.run(
                ["git", "rev-parse", "--verify", "HEAD"], cwd=ROOT,
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            ).returncode == 0:
                errors.append(f"missing gitlink: {path}")
            continue
        mode, commit, _stage_path = process.stdout.split(maxsplit=2)
        if mode != "160000":
            errors.append(f"{path}: expected gitlink mode, found {mode}")
        checkout = ROOT / path
        # An uninitialized submodule may exist as an empty directory. Running
        # `git rev-parse` there would walk up into the superproject and compare
        # its HEAD by mistake, so only inspect a checkout with its own .git
        # marker (normally a gitfile).
        working = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=checkout, text=True,
            capture_output=True, check=False,
        ) if (checkout / ".git").exists() else None
        observed = working.stdout.strip() if working and working.returncode == 0 else commit
        if observed != expected[name]:
            errors.append(f"{path}: checked commit {observed} != lock {expected[name]}")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print("submodule lock check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
