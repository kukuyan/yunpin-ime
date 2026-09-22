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


def require_checkout(checkout: Path) -> None:
    """Do not let Git discover the parent repository of an empty submodule."""
    if not (checkout / ".git").exists():
        raise RuntimeError(f"upstream checkout is not initialized: {checkout.name}")
    root = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], cwd=checkout, text=True
    ).strip()
    if Path(root).resolve() != checkout.resolve():
        raise RuntimeError(f"upstream checkout resolves to another repository: {checkout.name}")


def main() -> None:
    data = json.loads(LOCK.read_text(encoding="utf-8"))
    for item in data["upstreams"]:
        if item.get("update_policy") == "manual-release":
            # These versions are intentionally pinned and their source trees
            # are not initialized by the public-data update workflow.
            continue
        checkout = ROOT / PATHS[item["name"]]
        require_checkout(checkout)
        commit = head(item["url"])
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
        item["commit"] = commit
    data["observed_at"] = datetime.now(timezone.utc).date().isoformat()
    LOCK.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
