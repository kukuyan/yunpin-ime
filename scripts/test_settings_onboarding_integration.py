#!/usr/bin/env python3
"""Test the GUI backend against the real local Relay without shipping it.

Only temporary module metadata and test databases are created. No installed
input method, platform credential store, resident task, or external server is
used. Example: python3 scripts/test_settings_onboarding_integration.py --go /path/to/go
"""

import argparse
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go 1.25.12 executable")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    module = root / "desktopagent"
    environment = dict(os.environ, GOWORK="off", GOTOOLCHAIN="local")
    with tempfile.TemporaryDirectory(prefix="yunpin-onboarding-test-") as directory:
        modfile = Path(directory) / "onboarding.mod"
        modfile.write_bytes((module / "go.mod").read_bytes())
        sums = set((module / "go.sum").read_text().splitlines())
        sums.update((root / "integration" / "go.sum").read_text().splitlines())
        modfile.with_suffix(".sum").write_text("\n".join(sorted(sums)) + "\n")
        edit = [args.go, "mod", "edit", "-modfile", str(modfile),
                "-require=github.com/kukuyan/yunpin-ime/sync@v0.0.0"]
        for name in ("localstore", "protocol", "syncclient", "sync"):
            edit.append(f"-replace=github.com/kukuyan/yunpin-ime/{name}={root / name}")
        subprocess.run(edit, cwd=module, env=environment, check=True, timeout=30)
        subprocess.run(
            [args.go, "test", "-mod=mod", "-modfile", str(modfile),
             "-tags=onboarding_integration", "-run=TestOnboardingHTTPRealRelay",
             "-count=1", "-timeout=120s", "./cmd/yunpin-sync-agent"],
            cwd=module, env=environment, check=True, timeout=180,
        )


if __name__ == "__main__":
    main()
