# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]
WINDOWS = ROOT / "platform" / "windows"
LOCK_PATH = WINDOWS / "dependencies.lock.json"


def run(*args: str, cwd: Path = ROOT) -> str:
    completed = subprocess.run(
        args,
        cwd=cwd,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return completed.stdout.decode("utf-8", errors="replace").strip()


class WindowsClientTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.lock = json.loads(LOCK_PATH.read_text(encoding="utf-8"))

    def test_upstream_locks_and_checkouts_are_exact(self) -> None:
        root_lock = json.loads(
            (ROOT / "third_party" / "upstreams.lock.json").read_text(encoding="utf-8")
        )
        pinned = {row["name"].lower(): row["commit"] for row in root_lock["upstreams"]}
        self.assertEqual(self.lock["weasel"]["commit"], pinned["weasel"])
        self.assertEqual(self.lock["librime"]["commit"], pinned["librime"])
        self.assertEqual(self.lock["rimeIce"]["commit"], pinned["rime-ice"])
        for name, relative in (
            ("weasel", "third_party/weasel"),
            ("librime", "third_party/librime"),
            ("rimeIce", "third_party/rime-ice"),
        ):
            self.assertEqual(run("git", "-C", relative, "rev-parse", "HEAD"), self.lock[name]["commit"])
        for relative, expected in self.lock["librime"]["dependencies"].items():
            checkout = ROOT / "third_party" / "librime" / relative
            tree_row = run(
                "git",
                "-C",
                "third_party/librime",
                "ls-tree",
                "HEAD",
                "--",
                relative,
            )
            fields = tree_row.split()
            self.assertGreaterEqual(len(fields), 3, f"missing gitlink {relative}")
            self.assertEqual(fields[0], "160000")
            self.assertEqual(fields[1], "commit")
            self.assertEqual(fields[2], expected)
            if (checkout / ".git").exists():
                self.assertEqual(run("git", "-C", str(checkout), "rev-parse", "HEAD"), expected)

    def test_patch_series_hashes_apply_and_isolate_preview(self) -> None:
        for row in self.lock["weasel"]["patches"]:
            patch = ROOT / row["path"]
            self.assertEqual(hashlib.sha256(patch.read_bytes()).hexdigest(), row["sha256"])

        archive = subprocess.run(
            ["git", "-C", str(ROOT / "third_party" / "weasel"), "archive", "HEAD"],
            check=True,
            stdout=subprocess.PIPE,
        ).stdout
        with tempfile.TemporaryDirectory(prefix="yunpin-weasel-patches-") as directory:
            target = Path(directory)
            with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as stream:
                stream.extractall(target)
            for row in self.lock["weasel"]["patches"]:
                subprocess.run(
                    [
                        "git",
                        "-c",
                        "core.whitespace=cr-at-eol",
                        "apply",
                        "--ignore-space-change",
                        "--whitespace=error-all",
                        str(ROOT / row["path"]),
                    ],
                    cwd=target,
                    check=True,
                )

            constants = (target / "include" / "WeaselConstants.h").read_text(
                encoding="utf-8-sig"
            )
            ipc = (target / "include" / "WeaselIPC.h").read_text(encoding="utf-8-sig")
            globals_cpp = (target / "WeaselTSF" / "Globals.cpp").read_text(
                encoding="utf-8-sig"
            )
            security = (target / "WeaselIPCServer" / "SecurityAttribute.cpp").read_text(
                encoding="utf-8-sig"
            )
            server = (target / "WeaselServer" / "WeaselServerApp.cpp").read_text(
                encoding="utf-8-sig"
            )
            server_main = (target / "WeaselServer" / "WeaselServer.cpp").read_text(
                encoding="utf-8-sig"
            )
            self.assertIn("YunPin", constants)
            self.assertIn(self.lock["identity"]["pipeName"], ipc)
            self.assertIn("0x1c4fbfe5", globals_cpp.lower())
            self.assertNotIn("0xa3f4cded", globals_cpp.lower())
            self.assertNotIn("EVERYONE_FILE_ACCESS", security)
            self.assertIn("TOKEN_USER", security)
            self.assertNotIn("win_sparkle_init", server)
            self.assertNotIn("winsparkle.h", server_main.lower())
            self.assertIn("YunPinDeployer.exe", server)

    def test_build_stages_real_merged_plugin_for_both_architectures(self) -> None:
        build = (WINDOWS / "scripts" / "Build-Preview.ps1").read_text(encoding="utf-8")
        for required in (
            '"plugins\\librime-yunpin"',
            '$env:RIME_PLUGINS = "librime-yunpin"',
            'exit code ${LASTEXITCODE}:',
            'Join-Path $engineRoot "include"',
            'Join-Path $engineRoot "src"',
            '".yunpin-source-commit"',
            '"sources\\boost_1_84_0.7z"',
            '"New-PreviewIcon.ps1"',
            '"resource\\weasel.ico"',
            '"WeaselSetup\\WeaselSetup.ico"',
            '"x64", "Win32"',
            "Q\\(yunpin\\)",
            'libboost_wserialization-vc143-mt-s-x32-1_84.lib',
            'libboost_wserialization-vc143-mt-s-x64-1_84.lib',
            'boost-msvc-user-config.jam',
            '<setup>`"$vcvarsAllForJam`"',
            '"set BOOST_COMPILED_LIBS=$boostBuildOptions"',
            'Boost build did not produce required x86/x64 libraries',
            'Join-Path $boostRoot "bin.v2"',
            'Join-Path $boostRoot "stage"',
            'GIT_CEILING_DIRECTORIES',
            'Generated Weasel source is missing YunPin patch marker',
            'Built setup binary does not carry the isolated YunPin runtime identity',
            'Merged librime build did not produce',
            '"weasel.sln", "/m:1"',
            '"/p:Platform=x64"',
            '"/p:Platform=Win32"',
        ):
            self.assertIn(required, build)
        self.assertNotIn('"weasel.sln", "/m",', build)
        self.assertNotIn("exit code $LASTEXITCODE:", build)
        filter_source = (
            ROOT / "librime-yunpin" / "src" / "rime_yunpin_filter.cpp"
        ).read_text(encoding="utf-8")
        self.assertIn('"\\xE2\\x98\\x85"', filter_source)
        self.assertNotIn('"★"', filter_source)
        self.assertTrue((ROOT / "librime-yunpin" / "src" / "yunpin_module.cpp").is_file())
        self.assertTrue((ROOT / "engine" / "src" / "phrase_engine.cpp").is_file())

    def test_original_windows_icon_replaces_upstream_brand_asset(self) -> None:
        svg = (WINDOWS / "assets" / "yunpin-mark.svg").read_text(encoding="utf-8")
        generator = (WINDOWS / "scripts" / "New-PreviewIcon.ps1").read_text(
            encoding="utf-8"
        )
        self.assertIn("#3478F6", svg)
        self.assertIn("Original YunPin mark", svg)
        self.assertIn("New-YunPinPng", generator)
        self.assertIn("[System.Drawing.Color]::Transparent", generator)
        self.assertNotIn("Sogou", svg)
        self.assertNotIn("Weasel", svg)

    def test_private_filter_is_present_but_safely_disabled(self) -> None:
        config = (WINDOWS / "rime" / "rime_ice.custom.yaml").read_text(encoding="utf-8")
        self.assertIn('"engine/filters/@before 0": yunpin_filter@yunpin', config)
        self.assertIn('"yunpin/snapshot": "yunpin/private.tsv"', config)
        self.assertIn('"yunpin/max_candidates": 2', config)
        self.assertIn('"yunpin/enabled": false', config)
        self.assertFalse((WINDOWS / "rime" / "private.tsv").exists())
        self.assertFalse((WINDOWS / "rime" / "yunpin" / "private.tsv").exists())
        example = (WINDOWS / "rime" / "yunpin-private.tsv.example").read_text(
            encoding="utf-8"
        )
        data_rows = [
            line
            for line in example.splitlines()
            if line and not line.startswith("#") and line != "phrase\tpinyin\tsource\tuse_count"
        ]
        self.assertEqual(data_rows, [])

    def test_package_is_explicitly_unsigned_and_recoverable(self) -> None:
        installer = (WINDOWS / "package" / "Install-Preview.ps1").read_text(
            encoding="utf-8"
        )
        uninstaller = (WINDOWS / "package" / "Uninstall-Preview.ps1").read_text(
            encoding="utf-8"
        )
        package = (WINDOWS / "scripts" / "Package-Preview.ps1").read_text(
            encoding="utf-8"
        )
        for required in (
            "AcceptUnsignedDevelopmentBuild",
            "Assert-BundleManifest",
            "Copy-OverlayWithBackup",
            "Start-Process @startParameters",
            "PassThru = $true",
            "Wait = $true",
            "$process.ExitCode",
            '"YunPinIMEPreview"',
        ):
            self.assertIn(required, installer)
        self.assertNotIn("$LASTEXITCODE", installer)
        self.assertRegex(installer, r"-Arguments\s+@\((?:'/du'|\"/du\")\)")
        self.assertIn("ConfirmUninstall", uninstaller)
        self.assertIn("Start-Process @startParameters", uninstaller)
        self.assertIn("$process.ExitCode", uninstaller)
        self.assertNotIn("$LASTEXITCODE", uninstaller)
        self.assertIn("userDataRetained = $true", uninstaller)
        self.assertIn("MANIFEST.sha256", package)
        self.assertIn("development-preview-source.zip", package)
        self.assertIn("boost_1_84_0.7z", package)
        self.assertIn("SOURCE-MANIFEST.sha256", package)
        self.assertIn("Write-SourceCommitMarker", package)
        self.assertIn("Export-GitSubtree", package)
        self.assertIn('-Tree "platform/windows"', package)
        self.assertIn('-Tree "librime-yunpin"', package)
        self.assertIn("privateCandidateSnapshotEnabled = $false", package)
        self.assertIn("Packaged setup binary has the wrong runtime identity", (
            WINDOWS / "scripts" / "Test-Package.ps1"
        ).read_text(encoding="utf-8"))

    def test_runtime_rename_map_has_no_stock_identity(self) -> None:
        mapping = self.lock["package"]["runtimeFiles"]
        self.assertEqual(mapping["weasel.dll"], "yunpin.dll")
        self.assertEqual(mapping["weaselx64.dll"], "yunpinx64.dll")
        self.assertEqual(mapping["WeaselServer.exe"], "YunPinServer.exe")
        self.assertEqual(mapping["WeaselDeployer.exe"], "YunPinDeployer.exe")
        self.assertEqual(mapping["WeaselSetup.exe"], "YunPinSetup.exe")
        self.assertEqual(len(set(mapping.values())), len(mapping))


if __name__ == "__main__":
    unittest.main()
