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
            utility = (target / "include" / "WeaselUtility.h").read_text(
                encoding="utf-8-sig"
            )
            server = (target / "WeaselServer" / "WeaselServerApp.cpp").read_text(
                encoding="utf-8-sig"
            )
            server_main = (target / "WeaselServer" / "WeaselServer.cpp").read_text(
                encoding="utf-8-sig"
            )
            composition = (target / "WeaselTSF" / "Composition.cpp").read_text(
                encoding="utf-8-sig"
            )
            rime_with_weasel = (
                target / "RimeWithWeasel" / "RimeWithWeasel.cpp"
            ).read_text(encoding="utf-8-sig")
            rime_with_weasel_header = (
                target / "include" / "RimeWithWeasel.h"
            ).read_text(encoding="utf-8-sig")
            configurator = (
                target / "WeaselDeployer" / "Configurator.cpp"
            ).read_text(encoding="utf-8-sig")
            ipc_client = (
                target / "WeaselIPC" / "WeaselClientImpl.cpp"
            ).read_text(encoding="utf-8-sig")
            ipc_server = (
                target / "WeaselIPCServer" / "WeaselServerImpl.cpp"
            ).read_text(encoding="utf-8-sig")
            self.assertIn("YunPin", constants)
            self.assertIn(self.lock["identity"]["pipeName"], ipc)
            self.assertIn("0x1c4fbfe5", globals_cpp.lower())
            self.assertNotIn("0xa3f4cded", globals_cpp.lower())
            self.assertNotIn("EVERYONE_FILE_ACCESS", security)
            self.assertIn("TOKEN_USER", security)
            self.assertIn("#include <WeaselConstants.h>", utility)
            self.assertNotIn("win_sparkle_init", server)
            self.assertNotIn("winsparkle.h", server_main.lower())
            self.assertIn("YunPinDeployer.exe", server)
            self.assertIn("typed, explicitly armed action", composition)
            self.assertIn(
                "YunPinStartDefaultNativeSelectionSpoolerV1()",
                rime_with_weasel,
            )
            self.assertNotIn("LOCALAPPDATA", rime_with_weasel)
            self.assertNotIn("_wdupenv_s", rime_with_weasel)
            self.assertLess(
                rime_with_weasel.index("StartYunPinNativeSelectionSpooler();"),
                rime_with_weasel.index("#if 0", rime_with_weasel.index("void RimeWithWeaselHandler::Initialize")),
            )
            self.assertLess(
                rime_with_weasel.index("YunPinStopNativeSelectionSpoolerV1();"),
                rime_with_weasel.index("rime_api->finalize();"),
            )
            self.assertIn("bool RimeWithWeaselHandler::_SessionsAreIdle()", rime_with_weasel)
            self.assertIn("!status.is_composing", rime_with_weasel)
            self.assertIn("if (!session_id || !rime_api->find_session(session_id))", rime_with_weasel)
            self.assertIn("if (!rime_api->get_status(session_id, &status))", rime_with_weasel)
            idle_gate = rime_with_weasel.index("bool RimeWithWeaselHandler::TryStartMaintenance()")
            self.assertLess(
                rime_with_weasel.index("if (!_SessionsAreIdle())", idle_gate),
                rime_with_weasel.index("StartMaintenance();", idle_gate),
            )
            session_allocator = rime_with_weasel[
                rime_with_weasel.index("WeaselSessionId _GenerateNewWeaselSessionId") :
                rime_with_weasel.index("int expand_ibus_modifier")
            ]
            self.assertIn("const SessionStatusMap& sm", session_allocator)
            self.assertIn("WeaselSessionId& next_id", session_allocator)
            self.assertIn("const WeaselSessionId candidate = next_id++", session_allocator)
            self.assertIn("return 0", session_allocator)
            self.assertNotIn("sm.empty()", session_allocator)
            self.assertNotIn("sm.rbegin()", session_allocator)
            self.assertIn("WeaselSessionId m_next_session_id = 0", rime_with_weasel_header)
            self.assertLess(
                rime_with_weasel.index("m_pid = (m_pid << (31 - msbit))"),
                rime_with_weasel.index("m_next_session_id = m_pid + 1"),
            )
            self.assertLess(
                rime_with_weasel.index("m_next_session_id = m_pid + 1"),
                rime_with_weasel.index("_Setup();"),
            )
            add_session = rime_with_weasel[
                rime_with_weasel.index("DWORD RimeWithWeaselHandler::AddSession") :
                rime_with_weasel.index("DWORD RimeWithWeaselHandler::RemoveSession")
            ]
            self.assertIn(
                "_GenerateNewWeaselSessionId(m_session_status_map, m_next_session_id)",
                add_session,
            )
            exhausted = add_session[add_session.index("if (!ipc_id)") :]
            self.assertLess(
                exhausted.index("rime_api->destroy_session(session_id)"),
                exhausted.index("return 0"),
            )
            finalize = rime_with_weasel[
                rime_with_weasel.index("void RimeWithWeaselHandler::Finalize()") :
                rime_with_weasel.index("DWORD RimeWithWeaselHandler::FindSession")
            ]
            self.assertLess(finalize.index("if (m_disabled)"), finalize.index("m_disabled = true"))
            self.assertLess(
                finalize.index("pair.second.session_id = 0"),
                finalize.index("rime_api->destroy_session(session_id)"),
            )
            self.assertLess(
                finalize.index("rime_api->destroy_session(session_id)"),
                finalize.index("m_session_status_map.clear()"),
            )
            start_maintenance = rime_with_weasel[
                rime_with_weasel.index("void RimeWithWeaselHandler::StartMaintenance()") :
                rime_with_weasel.index("bool RimeWithWeaselHandler::_SessionsAreIdle()")
            ]
            self.assertIn("Finalize();", start_maintenance)
            self.assertNotIn("m_session_status_map.clear()", start_maintenance)
            sync_method = configurator[configurator.index("int Configurator::SyncUserData()") :]
            self.assertIn("constexpr int kMaintenanceBusyExitCode = 75", sync_method)
            self.assertIn("client.TryStartMaintenance()", sync_method)
            connect_gate = sync_method[
                sync_method.index("if (!client.Connect())") :
                sync_method.index("LOG(INFO) << \"Requesting idle-only")
            ]
            self.assertIn("return kMaintenanceBusyExitCode", connect_gate)
            self.assertLess(
                sync_method.index("client.TryStartMaintenance()"),
                sync_method.index("rime->sync_user_data()"),
            )
            self.assertNotIn("client.StartMaintenance()", sync_method)
            self.assertIn("WEASEL_IPC_MAINTENANCE_IF_IDLE", ipc_client)
            self.assertIn("m_pRequestHandler->TryStartMaintenance()", ipc_server)
            self.assertIn("std::lock_guard guard(g_api_mutex)", ipc_server)
            for forbidden in (
                "yunpin-search:",
                "yunpin-fav:",
                "ShellExecuteW",
                "CreateFileW",
                "favorites.jsonl",
            ):
                with self.subTest(forbidden=forbidden):
                    self.assertNotIn(forbidden, composition)

            filter_source = (
                ROOT / "librime-yunpin" / "src" / "rime_yunpin_filter.cpp"
            ).read_text(encoding="utf-8")
            self.assertNotIn("YunPinSearchCandidate", filter_source)
            self.assertNotIn("YunPinFavoriteCandidate", filter_source)
            self.assertNotIn("yunpin-search:", filter_source)
            self.assertNotIn("yunpin-fav:", filter_source)

        for row in self.lock["librime"]["patches"]:
            patch = ROOT / row["path"]
            self.assertEqual(hashlib.sha256(patch.read_bytes()).hexdigest(), row["sha256"])
            patch_text = patch.read_text(encoding="utf-8")
            self.assertIn(self.lock["librime"]["commit"], patch_text)
            if "0001-configurable-corrector" in row["path"]:
                self.assertIn("set<pair<SyllableId, size_t>> exact_matches", patch_text)
                self.assertIn("exact_matches.find({m.value, m.length})", patch_text)
                self.assertIn("bool correction_offset_used = false", patch_text)
                self.assertIn("kMaxCorrectionInputBytes = 128", patch_text)
                self.assertIn("kMaxCorrectionSearchesPerInput = 32", patch_text)
                self.assertIn("if (correction_analysis_enabled)", patch_text)
                self.assertIn("vector<vector<size_t>> normal_exact_ends", patch_text)
                self.assertIn("exact_path_reachable[current_pos]", patch_text)
                self.assertIn("exact_suffix_reachable[correction_end]", patch_text)
                self.assertIn("has_full_normal_exact_path", patch_text)
                self.assertIn("++correction_searches", patch_text)
                self.assertNotIn("!has_exact_match", patch_text)
                self.assertIn("if (correction_added)", patch_text)
            else:
                self.assertIn("commit_connection_.disconnect()", patch_text)
                self.assertIn("filters_.clear()", patch_text)

        librime_archive = subprocess.run(
            ["git", "-C", str(ROOT / "third_party" / "librime"), "archive", "HEAD"],
            check=True,
            stdout=subprocess.PIPE,
        ).stdout
        with tempfile.TemporaryDirectory(prefix="yunpin-librime-patches-") as directory:
            target = Path(directory)
            with tarfile.open(fileobj=io.BytesIO(librime_archive), mode="r:") as stream:
                stream.extractall(target)
            for row in self.lock["librime"]["patches"]:
                subprocess.run(
                    ["git", "apply", "--check", str(ROOT / row["path"])],
                    cwd=target,
                    check=True,
                )
                subprocess.run(
                    ["git", "apply", str(ROOT / row["path"])],
                    cwd=target,
                    check=True,
                )
            translator = (target / "src" / "rime" / "gear" / "script_translator.cc").read_text(
                encoding="utf-8"
            )
            self.assertIn("corrector_component", translator)

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
            "YunPinStartNativeSelectionSpoolerV1",
            '"dumpbin.exe" /nologo /exports',
            'libboost_wserialization-vc143-mt-s-x32-1_84.lib',
            'libboost_wserialization-vc143-mt-s-x64-1_84.lib',
            'boost-msvc-user-config.jam',
            '<setup>`"$vcvarsAllForJam`"',
            '"set BOOST_COMPILED_LIBS=$boostBuildOptions"',
            'Boost build did not produce required x86/x64 libraries',
            'Join-Path $boostRoot "bin.v2"',
            'Join-Path $boostRoot "stage"',
            'GIT_CEILING_DIRECTORIES',
            'Staged librime does not expose the locked corrector component selector',
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
        module = (ROOT / "librime-yunpin" / "src" / "yunpin_module.cpp").read_text(
            encoding="utf-8"
        )
        self.assertIn('Register("yunpin_corrector"', module)
        self.assertNotIn('Register("corrector"', module)
        corrector = (
            ROOT / "librime-yunpin" / "src" / "rime_yunpin_corrector.cpp"
        ).read_text(encoding="utf-8")
        self.assertIn("kMaxCorrectionsPerOffset = 16", corrector)
        self.assertIn("return nullptr;", corrector)
        self.assertNotIn("new NearSearchCorrector", corrector)
        self.assertTrue((ROOT / "engine" / "src" / "phrase_engine.cpp").is_file())

    def test_sync_agent_package_and_private_e2e_artifact_are_separate(self) -> None:
        build = (WINDOWS / "scripts" / "Build-Preview.ps1").read_text(
            encoding="utf-8"
        )
        agent_build = (WINDOWS / "scripts" / "Build-SyncAgents.ps1").read_text(
            encoding="utf-8"
        )
        package = (WINDOWS / "scripts" / "Package-Preview.ps1").read_text(
            encoding="utf-8"
        )
        package_test = (WINDOWS / "scripts" / "Test-Package.ps1").read_text(
            encoding="utf-8"
        )
        installer = (WINDOWS / "package" / "Install-Preview.ps1").read_text(
            encoding="utf-8"
        )
        uninstaller = (WINDOWS / "package" / "Uninstall-Preview.ps1").read_text(
            encoding="utf-8"
        )

        self.assertIn('"Build-SyncAgents.ps1"', build)
        self.assertIn('"desktopagent\\public"', agent_build)
        self.assertIn('"e2e-private\\windows"', agent_build)
        self.assertIn('BuildTag "yunpin_pairing_private"', agent_build)
        self.assertIn("go mod verify", agent_build)
        self.assertIn("package_go_licenses.py", agent_build)
        self.assertIn('$publicBaseline = Invoke-AgentCapture -Executable $publicBinary -Arguments @("e2e-init-empty-baseline")', agent_build)
        self.assertIn('$publicBaseline.Output -cne "yunpin-sync-agent: unknown command"', agent_build)
        self.assertIn('$privateBaseline = Invoke-AgentCapture -Executable $privateBinary -Arguments @("e2e-init-empty-baseline")', agent_build)
        self.assertIn("e2e-init-empty-baseline requires --confirm-create-empty-baseline", agent_build)
        self.assertNotIn('@("e2e-init-empty-baseline", "--confirm-create-empty-baseline")', agent_build)
        self.assertIn('publicReleaseEligible = $false', agent_build)
        self.assertIn("function Reset-PrivateE2EOutput", agent_build)
        self.assertIn(".yunpin-private-e2e-generated", agent_build)
        self.assertIn("Refusing to reset an unmarked private E2E output root", agent_build)
        self.assertIn("Refusing a nested reparse point in private E2E output", agent_build)
        self.assertIn("Private E2E output path contains a reparse point", agent_build)
        self.assertIn("Private E2E output path contains a non-directory component", agent_build)
        self.assertIn("Unknown file in private E2E generated output", agent_build)
        self.assertIn("Unknown directory in private E2E generated output", agent_build)
        self.assertIn("$isPublicSourceExport", agent_build)
        self.assertIn("$hasPrivateE2ESupport", agent_build)
        self.assertIn("intentionally absent from this public corresponding-source export", agent_build)
        self.assertIn('sameRunPublicOverlay = [ordered]@{', agent_build)
        self.assertIn('activationGate = "private-snapshot-e2e-only"', agent_build)
        for private_support in (
            "Private-Snapshot-E2E.Common.ps1",
            "Enable-Private-Snapshot-E2E.ps1",
            "Disable-Private-Snapshot-E2E.ps1",
            "README.md",
        ):
            self.assertIn(f'"{private_support}"', agent_build)
        self.assertIn('"desktopagent\\public\\yunpin-sync-agent.exe"', package)
        self.assertNotIn("e2e-private", package)
        self.assertIn('$privateE2ESource = [IO.Path]::GetFullPath', package)
        self.assertIn("Refusing to exclude a private E2E source path outside", package)
        self.assertIn("[IO.FileAttributes]::ReparsePoint", package)
        self.assertIn("Remove-Item -LiteralPath $privateE2ESource -Recurse -Force", package)
        self.assertIn("Private E2E activation script entered the public runtime", package_test)
        self.assertIn("Public runtime manifest references a private E2E activation script", package_test)
        for tree in ("desktopagent", "localstore", "protocol", "syncclient"):
            self.assertIn(f'"{tree}"', package)
        self.assertIn("third_party\\go-modules.lock.json", package)
        for required in (
            'Get-PeMachine -Path $syncAgent',
            'Invoke-AgentCapture -Executable $syncAgent -Arguments @("install-probe")',
            'Invoke-AgentCapture -Executable $syncAgent -Arguments @("pairing-invite")',
            'Invoke-AgentCapture -Executable $syncAgent -Arguments @("e2e-init-empty-baseline")',
            'yunpin-sync-agent: unknown command',
        ):
            self.assertIn(required, package_test)
        self.assertIn('"Install-SyncAgent.ps1"', installer)
        self.assertIn('"Verify-SyncAgent.ps1"', installer)
        self.assertIn("-ExpectedSha256 $bundleManifest[$syncManifestPath]", installer)
        self.assertIn('syncAgentRegistration = "disabled"', installer)
        self.assertNotIn('Enable-SyncAgent.ps1")', installer)
        self.assertIn('"support\\sync-agent\\Uninstall-SyncAgent.ps1"', uninstaller)

    def test_private_snapshot_e2e_overlay_gate_is_private_and_fail_closed(self) -> None:
        e2e = WINDOWS / "e2e"
        common = (e2e / "Private-Snapshot-E2E.Common.ps1").read_text(
            encoding="utf-8"
        )
        enable = (e2e / "Enable-Private-Snapshot-E2E.ps1").read_text(
            encoding="utf-8"
        )
        disable = (e2e / "Disable-Private-Snapshot-E2E.ps1").read_text(
            encoding="utf-8"
        )
        fixture = (e2e / "Test-Private-Snapshot-E2E.ps1").read_text(
            encoding="utf-8"
        )
        private_readme = (e2e / "README.md").read_text(encoding="utf-8")
        agent_build = (WINDOWS / "scripts" / "Build-SyncAgents.ps1").read_text(
            encoding="utf-8"
        )
        ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )

        for entry, confirmation in (
            (enable, "ConfirmPrivateSnapshotE2E"),
            (disable, "ConfirmDisablePrivateSnapshotE2E"),
        ):
            self.assertIn(confirmation, entry)
            self.assertIn("ExpectedPublicOverlaySha256", entry)
            self.assertIn("Assert-YunPinPrivateArtifactBinding", entry)
            for forbidden_parameter in (
                "InstallRoot",
                "UserDataRoot",
                "OverlayPath",
                "PrivateSnapshotPath",
                "DeployerPath",
            ):
                self.assertNotIn(f"[string]${forbidden_parameter}", entry)

        for required in (
            "[Environment+SpecialFolder]::ApplicationData",
            "[Environment+SpecialFolder]::LocalApplicationData",
            'Join-Path $appData "YunPin\\Rime"',
            'Join-Path $localAppData "Programs\\YunPinIME\\Preview"',
            "[IO.FileAttributes]::ReparsePoint",
            "Get-YunPinCurrentUserSid",
            "GetOwner(",
            "Expected exactly one yunpin/enabled: false and no true value",
            "Expected exactly one yunpin/session_learning: false and no true value",
            '[IO.FileOptions]::WriteThrough',
            "$stream.Flush($true)",
            "[IO.File]::Replace",
            "function Remove-YunPinStaleGateTemporaryFiles",
            "Local\\YunPinIME.PrivateSnapshotE2E.",
            "$mutex.WaitOne(0)",
            "AbandonedMutexException",
            '"backup-ready"',
            '"overlay-enabled-pending-deploy"',
            '"disabled-pending-deploy"',
            'Arguments = "/deploy"',
            "$process.WaitForExit($TimeoutSeconds * 1000)",
            "$process.Kill()",
            '"build-metadata.json"',
            '"enable-private-snapshot-e2e.ps1"',
            '"disable-private-snapshot-e2e.ps1"',
        ):
            self.assertIn(required, common)
        self.assertNotIn("$env:APPDATA", common)
        self.assertNotIn("$env:LOCALAPPDATA", common)
        self.assertNotIn(
            "Read-YunPinStrictUtf8File -Path $Paths.PrivateSnapshotPath", common
        )
        self.assertNotIn(
            "Get-YunPinFileSha256 -Path $Paths.PrivateSnapshotPath", common
        )
        cleanup = common[common.index("function Remove-YunPinGateStateAfterDeploy") :]
        self.assertLess(cleanup.index("$Paths.StatePath"), cleanup.index("$Paths.BackupPath"))
        self.assertNotIn('"yunpin/session_learning": true', common + enable + disable)
        self.assertIn("yunpin_learning_allowed", private_readme)
        self.assertIn("insufficient to make private candidates visible", private_readme)
        self.assertIn('hostCapabilityProvided = $false', agent_build)
        self.assertIn('realCandidateVisibilityClaimed = $false', agent_build)

        for required_fixture in (
            "fixture deploy failure",
            "Enable resume left a stale durable-backup temporary file",
            "fixture disable deploy failure",
            "Cleanup-order crash fixture did not retain the durable backup",
            "Duplicate enabled gate created a backup",
            "not owned by the current user",
            "reparse point",
            "Another private snapshot E2E gate process is active",
        ):
            self.assertIn(required_fixture, fixture)

        for public_surface in (
            WINDOWS / "scripts" / "Package-Preview.ps1",
            WINDOWS / "package" / "Install-Preview.ps1",
            WINDOWS / "package" / "README.txt",
            ROOT / ".github" / "workflows" / "release.yml",
        ):
            text = public_surface.read_text(encoding="utf-8")
            self.assertNotIn("Enable-Private-Snapshot-E2E.ps1", text)
            self.assertNotIn("Disable-Private-Snapshot-E2E.ps1", text)
            self.assertNotIn("private-snapshot-e2e-only", text)

        self.assertIn("Test-Private-Snapshot-E2E.ps1", ci)
        self.assertIn("sameRunPublicOverlay.sha256", ci)

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
        self.assertIn(
            '"engine/filters/@before last": '
            "yunpin_comment_filter@yunpin_comment_visibility",
            config,
        )
        self.assertIn("name: yunpin_show_candidate_pinyin", config)
        self.assertIn("states: [拼音关, 拼音开]", config)
        self.assertNotIn("reset:", config)
        self.assertIn('"translator/keep_comments": true', config)
        self.assertIn('"corrector": "［{comment}］"', config)
        package_test = (WINDOWS / "scripts" / "Test-Package.ps1").read_text(
            encoding="utf-8"
        )
        self.assertIn("yunpin_comment_filter@yunpin_comment_visibility", package_test)
        self.assertIn("yunpin_show_candidate_pinyin", package_test)
        default_overlay = (
            ROOT / "platform" / "rime" / "weasel" / "default.custom.yaml"
        ).read_text(encoding="utf-8")
        self.assertIn(
            '"switcher/save_options/@after last": yunpin_show_candidate_pinyin',
            default_overlay,
        )
        self.assertIn('"yunpin/snapshot": "yunpin/private.tsv"', config)
        self.assertIn('"yunpin/max_candidates": 2', config)
        self.assertIn('"yunpin/enabled": false', config)
        self.assertIn('"yunpin/short_input_guard": true', config)
        self.assertIn('"yunpin/session_learning": false', config)
        self.assertIn('"translator/enable_correction": false', config)
        self.assertIn('"yunpin/typo_correction": false', config)
        self.assertIn('"yunpin/typo_reviewed_confusions": false', config)
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
            "ConvertTo-NativeCommandLineArgument",
            "Diagnostics.ProcessStartInfo",
            "$process.WaitForExit()",
            "Set-YunPinMachineRegistry64",
            "Registry64",
            "registry64Runtime = $current",
            "$process.ExitCode",
            '"YunPinIMEPreview"',
        ):
            self.assertIn(required, installer)
        self.assertNotIn("$LASTEXITCODE", installer)
        self.assertNotIn("Start-Process @startParameters", installer)
        self.assertRegex(installer, r"-Arguments\s+@\((?:'/du'|\"/du\")\)")
        self.assertIn("ConfirmUninstall", uninstaller)
        self.assertIn("ConvertTo-NativeCommandLineArgument", uninstaller)
        self.assertIn("Diagnostics.ProcessStartInfo", uninstaller)
        self.assertIn("$process.WaitForExit()", uninstaller)
        self.assertIn("Remove-YunPinMachineRegistry64", uninstaller)
        self.assertIn("Registry64", uninstaller)
        self.assertIn("$process.ExitCode", uninstaller)
        self.assertNotIn("$LASTEXITCODE", uninstaller)
        self.assertNotIn("Start-Process @startParameters", uninstaller)
        self.assertIn("userDataRetained = $true", uninstaller)
        self.assertIn("MANIFEST.sha256", package)
        self.assertIn("development-preview-source.zip", package)
        self.assertIn("boost_1_84_0.7z", package)
        self.assertIn("SOURCE-MANIFEST.sha256", package)
        self.assertIn("Write-SourceCommitMarker", package)
        self.assertIn("Export-GitSubtree", package)
        self.assertIn('-Tree "platform/windows"', package)
        self.assertIn('-Tree "librime-yunpin"', package)
        self.assertIn("status --porcelain --untracked-files=normal", package)
        self.assertIn(
            "binaries and corresponding source use the same commit", package
        )
        self.assertIn("privateCandidateSnapshotEnabled = $false", package)
        package_test = (
            WINDOWS / "scripts" / "Test-Package.ps1"
        ).read_text(encoding="utf-8")
        self.assertIn("Packaged setup binary has the wrong runtime identity", package_test)
        self.assertIn('"translator/enable_correction": false', package_test)
        self.assertIn('"yunpin/typo_correction": false', package_test)
        self.assertNotIn('"translator/enable_correction": true', package_test)
        self.assertNotIn('"yunpin/typo_correction": true', package_test)

    def test_runtime_rename_map_has_no_stock_identity(self) -> None:
        mapping = self.lock["package"]["runtimeFiles"]
        self.assertEqual(mapping["weasel.dll"], "yunpin.dll")
        self.assertEqual(mapping["weaselx64.dll"], "yunpinx64.dll")
        self.assertEqual(mapping["WeaselServer.exe"], "YunPinServer.exe")
        self.assertEqual(mapping["WeaselDeployer.exe"], "YunPinDeployer.exe")
        self.assertEqual(mapping["WeaselSetup.exe"], "YunPinSetup.exe")
        self.assertEqual(len(set(mapping.values())), len(mapping))

    def test_native_spool_producer_matches_windows_consumer_contract(self) -> None:
        source = (
            ROOT / "librime-yunpin" / "src" / "native_selection_events.cpp"
        ).read_text(encoding="utf-8")
        header = (
            ROOT
            / "librime-yunpin"
            / "include"
            / "yunpin"
            / "native_selection_events.hpp"
        ).read_text(encoding="utf-8")
        cmake = (ROOT / "librime-yunpin" / "CMakeLists.txt").read_text(
            encoding="utf-8"
        )
        for required in (
            "SHGetKnownFolderPath",
            "FOLDERID_LocalAppData",
            "KF_FLAG_CREATE | KF_FLAG_NO_ALIAS",
            "kPrivateWindowsFullControl = 0x001f01ff",
            "WinLocalSystemSid",
            "OBJECT_INHERIT_ACE | CONTAINER_INHERIT_ACE",
            "SE_DACL_PROTECTED",
            "SE_DACL_AUTO_INHERIT_REQ",
            "FILE_FLAG_OPEN_REPARSE_POINT",
            "GetFinalPathNameByHandleW",
            "LockFileEx",
            "final_identity == created_identity",
        ):
            with self.subTest(required=required):
                self.assertIn(required, source)
        self.assertNotIn('getenv("LOCALAPPDATA")', source)
        self.assertNotIn("_wdupenv_s", source)
        self.assertIn("YunPinStartDefaultNativeSelectionSpoolerV1", header)
        for library in ("Advapi32", "Shell32", "Ole32", "Uuid"):
            self.assertIn(library, cmake)


if __name__ == "__main__":
    unittest.main()
