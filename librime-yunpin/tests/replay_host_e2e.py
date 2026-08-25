# SPDX-License-Identifier: Apache-2.0
"""Cross the native spool, persisted trace, and Go report in one test."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
from typing import Optional


def run(
    *command: str, cwd: Optional[Path] = None
) -> subprocess.CompletedProcess:
    completed = subprocess.run(
        command,
        cwd=cwd,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if completed.returncode != 0:
        raise RuntimeError(
            f"command failed ({completed.returncode}): {command!r}\n"
            f"stdout:\n{completed.stdout}\nstderr:\n{completed.stderr}"
        )
    return completed


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixture", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--module", required=True)
    args = parser.parse_args()

    module = Path(args.module).resolve(strict=True)
    fixture = Path(args.fixture).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="yunpin-replay-host-e2e-") as temporary:
        temporary_root = Path(temporary)
        executable = temporary_root / (
            "yunpin-replay-lab.exe" if os.name == "nt" else "yunpin-replay-lab"
        )
        run(
            args.go,
            "build",
            "-trimpath",
            "-buildvcs=false",
            "-o",
            str(executable),
            "./cmd/yunpin-replay-lab",
            cwd=module,
        )
        root = temporary_root / "YunPin" / "ReplayLab"
        run(str(executable), "init", "--root", str(root))
        started = json.loads(
            run(str(executable), "start", "--root", str(root)).stdout
        )
        session_id = started["session_id"]
        run(
            str(fixture),
            "--emit-running-session",
            str(root),
            session_id,
        )
        report = json.loads(
            run(str(executable), "report", "--root", str(root)).stdout
        )
        if report.get("native_event_count") != 1:
            raise AssertionError(f"native frame did not reach report: {report}")
        if report.get("exact_path_correction_first_count") != 1:
            raise AssertionError(
                "report did not recognize correction-first displacement: "
                f"{report}"
            )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
