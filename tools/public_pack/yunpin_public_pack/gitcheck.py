# SPDX-License-Identifier: Apache-2.0
"""Strict, local-only Git checkout verification."""

from __future__ import annotations

import os
import re
import subprocess
from pathlib import Path


class CheckoutError(RuntimeError):
    pass


def _git_environment() -> dict:
    allowed = ("PATH", "SystemRoot", "WINDIR", "LANG", "LC_ALL", "TMP", "TEMP")
    environment = {name: os.environ[name] for name in allowed if name in os.environ}
    environment.update(
        {
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_TERMINAL_PROMPT": "0",
            "GIT_ALLOW_PROTOCOL": "file",
            "LC_ALL": "C",
        }
    )
    return environment


def _run_git(root: Path, *arguments: str) -> str:
    try:
        completed = subprocess.run(
            [
                "git",
                "-c",
                "core.autocrlf=input",
                "-c",
                "core.safecrlf=false",
                "-C",
                str(root),
                *arguments,
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            timeout=30,
            env=_git_environment(),
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise CheckoutError(f"local Git verification failed ({type(exc).__name__})") from exc
    if completed.returncode != 0:
        raise CheckoutError("source root is not a readable local Git checkout")
    return completed.stdout.decode("utf-8", errors="strict").strip()


def verify_checkout(root: Path, expected_commit: str) -> str:
    root = root.expanduser().resolve(strict=True)
    if not root.is_dir():
        raise CheckoutError("source root is not a directory")
    if not re.fullmatch(r"[0-9a-fA-F]{40}", expected_commit):
        raise CheckoutError("lock commit is not a full 40-character Git object ID")
    actual = _run_git(root, "rev-parse", "--verify", "HEAD^{commit}").lower()
    if actual != expected_commit.lower():
        raise CheckoutError(f"Git HEAD mismatch: expected {expected_commit.lower()}, observed {actual}")
    status = _run_git(
        root,
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
        "--ignore-submodules=all",
    )
    if status:
        raise CheckoutError("pinned source checkout is dirty or contains untracked files")
    return actual


def verify_tracked_file(root: Path, path: Path) -> str:
    root = root.expanduser().resolve(strict=True)
    if path.expanduser().is_symlink():
        raise CheckoutError("symbolic-link dictionary inputs are not accepted")
    path = path.expanduser().resolve(strict=True)
    try:
        relative = path.relative_to(root).as_posix()
    except ValueError as exc:
        raise CheckoutError("source file resolves outside its pinned checkout") from exc
    expected_blob = _run_git(root, "rev-parse", f"HEAD:{relative}").lower()
    actual_blob = _run_git(root, "hash-object", "--path", relative, str(path)).lower()
    if actual_blob != expected_blob:
        raise CheckoutError(f"tracked source content differs from HEAD: {relative}")
    return relative
