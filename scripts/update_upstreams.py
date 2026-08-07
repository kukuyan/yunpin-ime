#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Refresh locked HEAD commits. Intended only for the review-PR workflow."""
from __future__ import annotations

from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parents[1]
LOCK = ROOT / "third_party" / "upstreams.lock.json"
PATHS = {
    "librime": "third_party/librime",
    "weasel": "third_party/weasel",
    "squirrel": "third_party/squirrel",
    "rime-ice": "third_party/rime-ice",
    "rime-essay": "third_party/rime-essay",
    "THUOCL": "third_party/THUOCL",
    "phrase-pinyin-data": "third_party/phrase-pinyin-data",
    "imewlconverter": "third_party/imewlconverter",
}


def head(url: str) -> str:
    output = subprocess.check_output(["git", "ls-remote", url, "HEAD"], text=True)
    value = output.split()[0]
    if len(value) != 40:
        raise RuntimeError(f"unexpected commit for {url}")
    return value


def main() -> None:
    data = json.loads(LOCK.read_text(encoding="utf-8"))
    for item in data["upstreams"]:
        if item.get("update_policy") == "manual-release":
            commit = item["commit"]
        else:
            commit = head(item["url"])
            item["commit"] = commit
        checkout = ROOT / PATHS[item["name"]]
        if checkout.exists():
            subprocess.run(
                ["git", "fetch", "--depth", "1", "origin", commit],
                cwd=checkout,
                check=True,
            )
            subprocess.run(
                ["git", "checkout", "--detach", commit],
                cwd=checkout,
                check=True,
            )
    data["observed_at"] = datetime.now(timezone.utc).date().isoformat()
    LOCK.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
