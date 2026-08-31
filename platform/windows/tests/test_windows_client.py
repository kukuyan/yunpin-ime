# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]
WINDOWS = ROOT / "platform" / "windows"
LOCK_PATH = WINDOWS / "dependencies.lock.json"
EXPECTED_GRAMMAR_MODEL = {
    "name": "wanxiang-lts-zh-hans",
    "filename": "wanxiang-lts-zh-hans.gram",
    "repository": "https://github.com/amzxyz/RIME-LMDG",
    "release": "LTS",
    "immutable": False,
    "assetId": 536587145,
    "assetUpdatedAt": "2026-08-30T12:25:59Z",
    "tagRef": "c78463a521aee2681db6cd6424a75a9b413237a3",
    "sourceSnapshotAtAssetUpdate": "5850e982a73537b1510afc4f99dcb37b335815d0",
    "url": "https://github.com/amzxyz/RIME-LMDG/releases/download/LTS/wanxiang-lts-zh-hans.gram",
    "sha256": "1635588006d79cc6955fbcf3d8de12822a36856eb5408735a8b4a2952b16cadf",
    "size": 420248620,
    "license": "CC-BY-4.0",
    "licenseFilename": "RIME-LMDG-LICENSE.CC-BY-4.0",
    "licenseUrl": "https://raw.githubusercontent.com/amzxyz/RIME-LMDG/5850e982a73537b1510afc4f99dcb37b335815d0/LICENSE",
    "licenseSha256": "9e5f1b3c610b9c2da5c313bf81d577a7d1acec686bdb0384edefa6df0f90cd94",
    "licenseSize": 18656,
}


def run(*args: str, cwd: Path = ROOT) -> str:
    completed = subprocess.run(
        args,
        cwd=cwd,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return completed.stdout.decode("utf-8", errors="replace").strip()


def require_initialized_submodule(relative: str, root: Path = ROOT) -> Path:
    checkout = (root / relative).resolve()
    completed = subprocess.run(
        ["git", "-C", str(checkout), "rev-parse", "--show-toplevel"],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    top_level = completed.stdout.decode("utf-8", errors="replace").strip()
    if (
        completed.returncode != 0
        or not top_level
        or Path(top_level).resolve() != checkout
    ):
        raise AssertionError(
            f"submodule is not initialized: {relative}; "
            "run git submodule update --init --recursive"
        )
    return checkout


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
        octagram = self.lock["librimeOctagram"]
        self.assertEqual(octagram["commit"], pinned["librime-octagram"])
        self.assertEqual(
            "57d18b9f58e5284bd891d559f6bdd16cf60341e9",
            octagram["commit"],
        )
        self.assertEqual(
            "7b9c77bcf17566b64204791b72cdb1b4471e22efec5eef9b79ca764ab99a1576",
            octagram["sha256"],
        )
        self.assertEqual("BSD-3-Clause", octagram["license"])
        self.assertEqual(
            "f67d27a6d2d586fcfed4b4c886a83747095396a39b6641e18e855086be2ec400",
            octagram["licenseSha256"],
        )
        for name, relative in (
            ("weasel", "third_party/weasel"),
            ("librime", "third_party/librime"),
            ("rimeIce", "third_party/rime-ice"),
        ):
            checkout = require_initialized_submodule(relative)
            self.assertEqual(
                run("git", "-C", str(checkout), "rev-parse", "HEAD"),
                self.lock[name]["commit"],
            )
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

    def test_full_grammar_model_lock_is_shared_and_exact(self) -> None:
        macos = json.loads(
            (ROOT / "platform" / "macos" / "dependencies.lock.json").read_text(
                encoding="utf-8"
            )
        )["grammarModel"]
        self.assertEqual(EXPECTED_GRAMMAR_MODEL, self.lock["grammarModel"])
        self.assertEqual(macos, self.lock["grammarModel"])

    def test_full_grammar_model_is_packaged_and_headless_gated(self) -> None:
        build = (WINDOWS / "scripts" / "Build-Preview.ps1").read_text(
            encoding="utf-8"
        )
        package = (WINDOWS / "scripts" / "Package-Preview.ps1").read_text(
            encoding="utf-8"
        )
        package_test = (WINDOWS / "scripts" / "Test-Package.ps1").read_text(
            encoding="utf-8"
        )
        probe = (
            WINDOWS / "tests" / "rime-grammar-quality-probe" / "main.cpp"
        ).read_text(encoding="utf-8")
        for script in (build, package, package_test):
            self.assertIn("grammarModel.sha256", script)
            self.assertIn("grammarModel.size", script)
            self.assertIn("grammarModel.licenseSha256", script)
            self.assertIn("grammarModel.licenseSize", script)
            self.assertIn("ReparsePoint", script)
        self.assertIn("SOURCE-MANIFEST.sha256", build)
        self.assertIn("Assert-SourceManifest -Root $repoRoot", build)
        self.assertIn("Assert-OnlineGrammarAssetMetadata", build)
        self.assertIn("verify_grammar_asset_metadata.py", build)
        self.assertIn("releases/tags/LTS", build)
        self.assertIn("git/ref/tags/LTS", build)
        self.assertIn("$env:GITHUB_TOKEN", build)
        self.assertIn("$headers.Authorization", build)
        self.assertIn("-DependencyLock $lockPath -ScratchRoot $scratchRoot", build)
        self.assertIn("[switch]$Offline", build)
        self.assertIn("$script:YunPinOfflineBuild", build)
        self.assertIn("[switch]$TestGrammarCacheSafety", build)
        self.assertIn("[IO.FileMode]::CreateNew", build)
        self.assertIn("[IO.FileShare]::None", build)
        self.assertIn('[guid]::NewGuid().ToString("N")', build)
        self.assertIn("Assert-SafeCacheTemporaryFile", build)
        self.assertIn("$sourceStream.CopyTo($temporaryStream)", build)
        self.assertIn("$httpStream.CopyTo($temporaryStream)", build)
        self.assertIn("$temporaryStream.Flush($true)", build)
        self.assertNotIn("-OutFile $temporary", build)
        self.assertNotIn("Copy-Item -LiteralPath $Bundled -Destination $temporary", build)
        self.assertIn(
            "[IO.Path]::GetDirectoryName($fullPath),\n"
            "            $fullDirectory,\n"
            "            [StringComparison]::OrdinalIgnoreCase",
            build,
        )
        self.assertIn("[IO.File]::Move($temporary, $Destination)", build)
        self.assertIn("Invoke-GrammarCacheSafetySelfTest", build)
        self.assertIn("Predictable partial reparse point", build)
        self.assertIn("Tampered locked cache resource", build)
        self.assertNotIn('$grammarModelPath + ".part"', build)
        self.assertNotIn('$grammarLicensePath + ".part"', build)
        self.assertIn("BUILD-SOURCE-METADATA.json", package)
        self.assertIn("Source export is missing required subtree", package)
        self.assertIn("exactly one locked grammar model", package)
        self.assertIn("synthetic-public-ranking.tsv", package)
        self.assertIn("synthetic_private_fixture=pass", package)
        self.assertIn("synthetic_private_counterfactual=pass", package)
        self.assertIn('"prepare-baseline"', package)
        self.assertIn('"prepare-model"', package)
        self.assertIn('"private-off"', package)
        self.assertIn("deploymentPhase", package)
        self.assertIn("measurementPhase", package)
        self.assertIn("loadStageEvidence", package)
        self.assertIn("fresh-process-after-deployment", package)
        expected_holdout = (
            ("accept_origin_image", "youyuantuma", "有原图吗", "有原图吗"),
            ("accept_semantic_account", "youceshizhanghaoma", "右侧是账号吗", "有测试账号吗"),
            ("accept_database_version", "shujukushiyongdeshinagebanben", "数据库使用的是哪个版本", "数据库使用的是哪个版本"),
            ("short_weather", "jintiantianqihenhao", "今天天气很好", "今天天气很好"),
            ("short_availability", "qingwenyoukongma", "请问有空吗", "请问有空吗"),
            ("short_how_to", "zhegeshizenmeyongde", "这个是怎么用的", "这个是怎么用的"),
            ("homophone_retry", "qingzaishiyici", "请再试一次", "请再试一次"),
            ("homophone_usage", "shiyongfangfa", "使用方法", "使用方法"),
            ("homophone_which", "yinggaishinage", "应该是那个", "应该是那个"),
            ("long_email", "qingbaowenjianfadaowodeyouxiang", "情报文件发到我的邮箱", "情报文件发到我的邮箱"),
            ("long_code", "zhegedaimaweishenmewufayunxing", "这个代码为什么无法运行", "这个代码为什么无法运行"),
            ("long_meeting", "qingquerenhuiyishijianhedidian", "请确认会议时间和地点", "请确认会议时间和地点"),
            ("circle_zero", "erlingyilingnianfabu", "二〇一〇年发布", "二〇一〇年发布"),
            ("ordinary_zero", "lingduyixia", "零度以下", "零度以下"),
            ("heldout_tomorrow", "womenmingtianjian", "我们明天见", "我们明天见"),
            ("heldout_feedback", "qingjishifankui", "请及时反馈", "请及时反馈"),
            ("heldout_network", "wangluolianjiezhengchang", "网络连接正常", "网络连接正常"),
            ("heldout_open_file", "zhegewenjianzenmedakai", "这个文件怎么打开", "这个文件怎么打开"),
            ("heldout_send_address", "qingbadizhifageiwo", "请把地址发给我", "请把地址发给我"),
            ("heldout_received", "woyijingshoudaole", "我已经收到了", "我已经受到了"),
        )
        self.assertIn("std::array<HoldoutCase, 20>", probe)
        for case_id, public_input, model_first, baseline_first in expected_holdout:
            self.assertIn(f'{{"{case_id}", "{public_input}"', probe)
            self.assertRegex(
                probe,
                re.escape(f'"{model_first}"') + r",\s*" +
                re.escape(f'"{baseline_first}"'),
            )
        self.assertIn("kP95GateMicroseconds = 20000", probe)
        self.assertLess(
            probe.index("#include <windows.h>"),
            probe.index("#include <psapi.h>"),
        )
        self.assertIn('"yunpingongcexianhuanqihao"', probe)
        self.assertIn("CandidatePageContains", probe)
        self.assertIn("bool* found", probe)
        self.assertIn("synthetic_private_fixture=pass", probe)
        self.assertIn("synthetic_private_counterfactual=pass", probe)
        self.assertIn('mode == "prepare-model"', probe)
        self.assertIn('mode == "prepare-baseline"', probe)
        self.assertIn("traits.min_log_level = 0", probe)
        self.assertIn("if (prepare_mode)", probe)
        self.assertEqual(1, probe.count("start_maintenance(True)"))
        prepare_block = probe[
            probe.index("if (prepare_mode)") : probe.index(
                "const auto schema_started"
            )
        ]
        self.assertIn("start_maintenance(True)", prepare_block)
        self.assertIn('std::cerr << "schema_select_begin', probe)
        self.assertIn('std::cerr << "schema_select_end', probe)
        self.assertIn("rss_after_initialize_bytes=", probe)
        self.assertIn("rss_after_schema_select_bytes=", probe)
        self.assertIn("measurement_max_rss_bytes=", probe)
        self.assertIn("measurement_process_elapsed_us=", probe)
        self.assertNotIn("maintenance_us=", probe)
        self.assertIn("process-cold-deployed-user-data-os-warm", probe)
        self.assertIn("acceptedQualityCases", package)
        self.assertIn("modelMinusBaseline", package)
        self.assertIn("rssAfterHoldoutBytes", package)
        self.assertIn("rssAfterInitializeBytes", package)
        self.assertIn("rssAfterSchemaSelectBytes", package)
        self.assertIn("loading gram db:", package)
        self.assertIn("modelFileOpenObservedStage", package)
        self.assertIn("largestResidentGrowthStage", package)
        self.assertIn("modelMinusBaselineRssIncreaseAtHoldoutBytes", package)
        cmake = (
            WINDOWS / "tests" / "rime-grammar-quality-probe" / "CMakeLists.txt"
        ).read_text(encoding="utf-8")
        self.assertIn("Psapi.lib", cmake)

    def test_patch_series_hashes_apply_and_isolate_preview(self) -> None:
        for row in self.lock["weasel"]["patches"]:
            patch = ROOT / row["path"]
            self.assertEqual(hashlib.sha256(patch.read_bytes()).hexdigest(), row["sha256"])

        weasel = require_initialized_submodule("third_party/weasel")
        archive = subprocess.run(
            ["git", "-C", str(weasel), "archive", "HEAD"],
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
            self.assertIn('L"yunpin-settings.exe"', server)
            self.assertIn('std::wstring(L"settings")', server)
            self.assertNotIn(
                'ID_WEASELTRAY_SETTINGS,\n      std::bind(execute, dir / L"YunPinDeployer.exe"',
                server,
            )
            self.assertIn("typed, explicitly armed action", composition)
            self.assertIn(
                "YunPinStartDefaultNativeSelectionSpoolerV1()",
                rime_with_weasel,
            )
            self.assertIn(
                "YunPinStartDefaultReplaySpoolerV1()",
                rime_with_weasel,
            )
            capability = 'rime_api->set_option(session_id, "yunpin_learning_allowed", True);'
            self.assertEqual(1, rime_with_weasel.count(capability))
            client_info = rime_with_weasel[
                rime_with_weasel.index("void RimeWithWeaselHandler::_ReadClientInfo") :
                rime_with_weasel.index("void RimeWithWeaselHandler::_GetCandidateInfo")
            ]
            self.assertLess(
                client_info.index(capability),
                client_info.index("// set app specific options"),
            )
            self.assertNotIn("LOCALAPPDATA", rime_with_weasel)
            self.assertNotIn("_wdupenv_s", rime_with_weasel)
            self.assertLess(
                rime_with_weasel.index("StartYunPinNativeSelectionSpooler();"),
                rime_with_weasel.index("#if 0", rime_with_weasel.index("void RimeWithWeaselHandler::Initialize")),
            )
            self.assertLess(
                rime_with_weasel.index("StartYunPinReplaySpooler();"),
                rime_with_weasel.index("#if 0", rime_with_weasel.index("void RimeWithWeaselHandler::Initialize")),
            )
            self.assertLess(
                rime_with_weasel.index("YunPinStopNativeSelectionSpoolerV1();"),
                rime_with_weasel.index("rime_api->finalize();"),
            )
            self.assertLess(
                rime_with_weasel.index("YunPinStopReplaySpoolerV1();"),
                rime_with_weasel.index("rime_api->finalize();"),
            )
            self.assertIn("bool RimeWithWeaselHandler::_SessionsAreIdle()", rime_with_weasel)
            self.assertIn("!status.is_composing", rime_with_weasel)
            self.assertIn("if (!session_id || !rime_api->find_session(session_id))", rime_with_weasel)
            self.assertIn("if (!rime_api->get_status(session_id, &status))", rime_with_weasel)
            find_session = rime_with_weasel[
                rime_with_weasel.index("DWORD RimeWithWeaselHandler::FindSession") :
                rime_with_weasel.index("DWORD RimeWithWeaselHandler::AddSession")
            ]
            self.assertIn("m_session_status_map.find(ipc_id)", find_session)
            self.assertIn("m_session_status_map.erase(found)", find_session)
            self.assertNotIn("to_session_id(ipc_id)", find_session)
            idle_sessions = rime_with_weasel[
                rime_with_weasel.index("bool RimeWithWeaselHandler::_SessionsAreIdle()") :
                rime_with_weasel.index("bool RimeWithWeaselHandler::TryStartMaintenance()")
            ]
            self.assertIn("entry = m_session_status_map.erase(entry)", idle_sessions)
            self.assertIn("continue;", idle_sessions)
            uninspectable = idle_sessions[
                idle_sessions.index("if (!rime_api->get_status(session_id, &status))") :
                idle_sessions.index("const bool idle = !status.is_composing")
            ]
            self.assertIn("return false", uninspectable)
            self.assertNotIn("erase", uninspectable)
            resolver = rime_with_weasel_header[
                rime_with_weasel_header.index("RimeSessionId to_session_id") :
                rime_with_weasel_header.index("SessionStatus& get_session_status")
            ]
            self.assertIn("m_session_status_map.find(ipc_id)", resolver)
            self.assertNotIn("m_session_status_map[ipc_id]", resolver)
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

        librime = require_initialized_submodule("third_party/librime")
        librime_archive = subprocess.run(
            ["git", "-C", str(librime), "archive", "HEAD"],
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

    def test_uninitialized_submodule_is_not_mistaken_for_superproject(self) -> None:
        with tempfile.TemporaryDirectory(prefix="yunpin-submodule-preflight-") as directory:
            root = Path(directory)
            subprocess.run(
                ["git", "init", "-q", str(root)],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            checkout = root / "third_party" / "weasel"
            checkout.mkdir(parents=True)

            with self.assertRaisesRegex(
                AssertionError,
                r"submodule is not initialized: third_party/weasel; "
                r"run git submodule update --init --recursive",
            ):
                require_initialized_submodule("third_party/weasel", root)

            subprocess.run(
                ["git", "init", "-q", str(checkout)],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(
                checkout.resolve(),
                require_initialized_submodule("third_party/weasel", root),
            )

    def test_build_stages_real_merged_plugin_for_both_architectures(self) -> None:
        build = (WINDOWS / "scripts" / "Build-Preview.ps1").read_text(encoding="utf-8")
        for required in (
            '"plugins\\librime-yunpin"',
            '"plugins\\octagram"',
            '$env:RIME_PLUGINS = "librime-yunpin octagram"',
            'exit code ${LASTEXITCODE}:',
            'Join-Path $engineRoot "include"',
            'Join-Path $engineRoot "src"',
            '".yunpin-source-commit"',
            '"sources\\boost_1_84_0.7z"',
            '"New-PreviewIcon.ps1"',
            '"resource\\weasel.ico"',
            '"WeaselSetup\\WeaselSetup.ico"',
            '"x64", "Win32"',
            '"yunpin", "octagram"',
            "('Q\\(' + $module + '\\)')",
            "rime-octagram-objs.vcxproj",
            "gram_encoding.cc",
            "grammar_module.cc",
            "u <<= 7;",
            '"x64"',
            '"x86"',
            "rime-module-probe",
            "octagram_module_registered=true",
            "grammar_module_registered=true",
            "rime_runtime_identity_exact=true",
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
        probe = (
            WINDOWS / "tests" / "rime-module-probe" / "main.cpp"
        ).read_text(encoding="utf-8")
        self.assertIn('api->find_module("octagram")', probe)
        self.assertIn('api->find_module("grammar")', probe)
        self.assertIn('api->find_module("yunpin")', probe)
        self.assertIn("GetModuleFileNameW", probe)
        self.assertIn("std::filesystem::equivalent", probe)
        self.assertIn("rime_runtime_identity_exact", probe)
        probe_cmake = (
            WINDOWS / "tests" / "rime-module-probe" / "CMakeLists.txt"
        ).read_text(encoding="utf-8")
        self.assertIn("RIME_IMPORT_LIBRARY", probe_cmake)
        self.assertIn("/W4 /WX /permissive-", probe_cmake)

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
        self.assertIn('"yunpin-settings.exe"', agent_build)
        self.assertIn('gui $settingsBinary', agent_build)
        self.assertIn('"yunpin-replay-lab.exe"', agent_build)
        self.assertIn('console $replayBinary', agent_build)
        self.assertIn('./cmd/yunpin-replay-lab', agent_build)
        self.assertIn('replaylab\\licenses', agent_build)
        self.assertIn('--go-package ./cmd/yunpin-replay-lab', agent_build)
        self.assertIn('--artifact yunpin-replay-lab', agent_build)
        self.assertIn('"e2e-private\\windows"', agent_build)
        self.assertIn('BuildTag "yunpin_pairing_private"', agent_build)
        self.assertIn("go mod verify", agent_build)
        self.assertIn("package_go_licenses.py", agent_build)
        self.assertIn('"yunpin-settings.exe"', package)
        self.assertIn('sync-agent/yunpin-settings.exe', installer)
        self.assertIn('"yunpin-replay-lab.exe"', package)
        self.assertIn('sync-agent/yunpin-replay-lab.exe', installer)
        self.assertIn('gui $settingsLauncher', package_test)
        self.assertIn('console $replayLab', package_test)
        self.assertIn('replayLicenseManifest.artifact', package_test)
        self.assertIn('"yunpin-replay-lab"', package_test)
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
        for tree in ("desktopagent", "localstore", "protocol", "replaylab", "syncclient"):
            self.assertIn(f'"{tree}"', package)
        self.assertIn("third_party\\go-modules.lock.json", package)
        for required in (
            'Get-PeMachine -Path $syncAgent',
            'Invoke-AgentCapture -Executable $syncAgent -Arguments @("install-probe")',
            'Invoke-AgentCapture -Executable $syncAgent -Arguments @("pairing-invite")',
            'Invoke-AgentCapture -Executable $syncAgent -Arguments @("e2e-init-empty-baseline")',
            'yunpin-sync-agent: unknown command',
            'Invoke-AgentCapture -Executable $replayLab -Arguments @("help")',
        ):
            self.assertIn(required, package_test)
        self.assertIn('"Install-SyncAgent.ps1"', installer)
        self.assertIn('"Verify-SyncAgent.ps1"', installer)
        self.assertIn("-ExpectedSha256 $bundleManifest[$syncManifestPath]", installer)
        self.assertIn('syncAgentRegistration = "disabled"', installer)
        for marker in (
            "function Read-YunPinStrictUtf8File",
            "function Get-YunPinBooleanOptIn",
            "function Preserve-YunPinBooleanOptIns",
            "$preservePrivateCandidates = Get-YunPinBooleanOptIn",
            "$preserveSessionLearning = Get-YunPinBooleanOptIn",
            "[IO.File]::Replace($temporary, $Path, $metadataBackup, $true)",
        ):
            self.assertIn(marker, installer)
        self.assertIn("New-Object Text.UTF8Encoding($false, $true)", installer)
        self.assertIn("[IO.File]::ReadAllText($Path, $strictUtf8)", installer)
        self.assertIn("contains the Unicode replacement character", installer)
        self.assertIn(
            "Read-YunPinStrictUtf8File -Path (Join-Path $bundleRoot",
            installer,
        )
        self.assertNotIn("$content = Get-Content -LiteralPath $Path -Raw", installer)
        utf8_fixture = (
            WINDOWS / "tests" / "Test-Install-Preview-Utf8.ps1"
        ).read_text(encoding="utf-8")
        for evidence in (
            "states: [拼音关, 拼音开]",
            '"corrector": "［{comment}］"',
            "Installer accepted malformed UTF-8",
            "Installer accepted an already-corrupted replacement character",
        ):
            self.assertIn(evidence, utf8_fixture)
        self.assertLess(
            installer.index("$preservePrivateCandidates = Get-YunPinBooleanOptIn"),
            installer.index("Copy-OverlayWithBackup -SourceRoot"),
        )
        self.assertLess(
            installer.index("Copy-OverlayWithBackup -SourceRoot"),
            installer.index("Preserve-YunPinBooleanOptIns -Path"),
        )
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
        for grammar_setting in (
            '"grammar/language": wanxiang-lts-zh-hans',
            '"grammar/collocation_max_length": 6',
            '"grammar/collocation_min_length": 3',
            '"grammar/collocation_penalty": -14',
            '"grammar/non_collocation_penalty": -6',
            '"grammar/weak_collocation_penalty": -100',
            '"grammar/rear_penalty": -20',
            '"translator/contextual_suggestions": true',
            '"translator/max_homophones": 8',
        ):
            self.assertIn(grammar_setting, config)
        self.assertNotIn("max_homographs", config)

        package = (WINDOWS / "scripts" / "Package-Preview.ps1").read_text(
            encoding="utf-8"
        )
        for serialized_penalty_pattern in (
            'collocation_penalty:\\s*(?:-14|"-14")',
            'non_collocation_penalty:\\s*(?:-6|"-6")',
            'weak_collocation_penalty:\\s*(?:-100|"-100")',
            'rear_penalty:\\s*(?:-20|"-20")',
        ):
            self.assertIn(serialized_penalty_pattern, package)
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
        self.assertIn("librimeOctagram.archiveName", package)
        self.assertIn("librime-octagram-BSD-3-Clause.txt", package)
        self.assertIn("librimeOctagramSourceSha256", package)
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
        self.assertIn('mergedPlugins = @("librime-yunpin", "librime-octagram")', package)
        self.assertIn('mergedModules = @("yunpin", "octagram", "grammar")', package)
        package_test = (
            WINDOWS / "scripts" / "Test-Package.ps1"
        ).read_text(encoding="utf-8")
        self.assertIn("Packaged setup binary has the wrong runtime identity", package_test)
        self.assertIn('"translator/enable_correction": false', package_test)
        self.assertIn('"yunpin/typo_correction": false', package_test)
        self.assertNotIn('"translator/enable_correction": true', package_test)
        self.assertNotIn('"yunpin/typo_correction": true', package_test)
        self.assertIn("Packaged librime-octagram license", package_test)

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

    def test_background_resident_is_windowless_and_wired_end_to_end(self) -> None:
        """The scheduled task must run a GUI-subsystem binary.

        Go links console-subsystem binaries by default, so a scheduled task that
        starts a long-running console binary in the user's interactive session
        gets a console window allocated for it -- and because the resident runs
        for the life of the session, that window stays on screen rather than
        flashing. The interactive agent prints JSON to stdout and must remain
        console-subsystem, so the two are separate binaries. Every link in that
        chain is asserted here because none of it is observable until a user
        actually logs in on Windows.
        """
        build = (WINDOWS / "scripts" / "Build-SyncAgents.ps1").read_text(encoding="utf-8")
        package = (WINDOWS / "scripts" / "Package-Preview.ps1").read_text(encoding="utf-8")
        installer = (WINDOWS / "package" / "Install-Preview.ps1").read_text(encoding="utf-8")
        install_agent = (
            ROOT / "desktopagent" / "install" / "windows" / "Install-SyncAgent.ps1"
        ).read_text(encoding="utf-8")
        enable_agent = (
            ROOT / "desktopagent" / "install" / "windows" / "Enable-SyncAgent.ps1"
        ).read_text(encoding="utf-8")

        # Built as a separate GUI-subsystem image, and the linked image is
        # verified rather than the build flags being trusted.
        self.assertIn('"-ldflags", "-H=windowsgui"', build)
        self.assertIn('-Package "./cmd/yunpin-sync-resident" -WindowsGui', build)
        self.assertIn("check_pe_subsystem.py", build)
        self.assertIn("gui $residentBinary", build)
        self.assertIn("gui $settingsBinary", build)
        self.assertIn("console $publicBinary", build)
        self.assertIn("console $replayBinary", build)

        # Staged into the bundle and installed next to the interactive agent.
        self.assertIn("yunpin-sync-resident.exe", package)
        self.assertIn("yunpin-settings.exe", package)
        self.assertIn("yunpin-replay-lab.exe", package)
        self.assertIn("sync-agent/yunpin-sync-resident.exe", installer)
        self.assertIn("sync-agent/yunpin-settings.exe", installer)
        self.assertIn("sync-agent/yunpin-replay-lab.exe", installer)
        self.assertIn("-ResidentExpectedSha256", installer)
        self.assertIn("$ResidentPath", install_agent)
        self.assertIn("$ResidentExpectedSha256", install_agent)

        # The task runs the resident, not the interactive agent. The resident
        # implements only `run`, so the subcommand leaves the argument string.
        self.assertIn(
            'New-ScheduledTaskAction -Execute $residentDestination -Argument "--interval 1m"',
            install_agent,
        )
        # The legacy argument string still appears in the installer: it has to
        # recognise the previous generation's task in order to replace it during
        # an upgrade. What must not appear is a registration on the interactive
        # binary, which is what would put the console window back.
        self.assertNotIn(
            "New-ScheduledTaskAction -Execute $destination", install_agent
        )
        self.assertNotIn('"run --interval 1m"', enable_agent)
        self.assertIn('Execute = $destination; Arguments = "run --interval 1m"', install_agent)
        self.assertIn("$residentDestination", enable_agent)
        self.assertIn("yunpin-sync-resident.exe", enable_agent)

    def test_source_archive_binds_every_subtree_to_the_recorded_commit(self) -> None:
        """engine/ must come from the git tree like every other subtree.

        BUILD-SOURCE-METADATA.json records $repoCommit. When engine/ was copied
        from the working tree instead of exported from git, that recorded commit
        and the shipped engine sources could disagree, and any untracked file
        under engine/ would have been packaged with them.
        """
        package = (WINDOWS / "scripts" / "Package-Preview.ps1").read_text(encoding="utf-8")
        self.assertIn('-Tree "engine"', package)
        self.assertNotIn(
            'Copy-TreeContent -Source (Join-Path (Join-Path $repoRoot "engine")',
            package,
        )
        self.assertIn("repositoryCommit = $repoCommit", package)

    def test_source_subtree_export_keeps_root_eol_attributes(self) -> None:
        """Subtree archives must retain the repository root attributes.

        ``git archive HEAD:path`` treats the subtree as an attribute root. On a
        Windows host with ``core.autocrlf=true``, that converted LF-locked patch
        blobs to CRLF and made the source archive fail Build-Preview.ps1's raw
        patch hash gate. Archive with a root-relative pathspec, then strip the
        retained leading path components during extraction.
        """
        package = (WINDOWS / "scripts" / "Package-Preview.ps1").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            '"archive", "--format=tar", "--output=$archive", "HEAD", "--", $Tree',
            package,
        )
        self.assertIn('("--strip-components=" + $treeComponents.Count)', package)
        self.assertNotIn('("HEAD:" + $Tree)', package)

        archive = subprocess.run(
            [
                "git",
                "-c",
                "core.autocrlf=true",
                "archive",
                "--format=tar",
                "HEAD",
                "--",
                "platform/patches/weasel",
                "platform/patches/librime-1.17",
                "platform/windows/scripts/Build-Preview.ps1",
            ],
            cwd=ROOT,
            check=True,
            stdout=subprocess.PIPE,
        ).stdout
        with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as stream:
            for row in self.lock["weasel"]["patches"] + self.lock["librime"]["patches"]:
                exported = stream.extractfile(row["path"])
                self.assertIsNotNone(exported)
                self.assertEqual(
                    hashlib.sha256(exported.read()).hexdigest(), row["sha256"]
                )

            # Do not solve the patch problem with a global LF rewrite: explicit
            # Windows-script CRLF attributes must still be honored.
            script = stream.extractfile(
                "platform/windows/scripts/Build-Preview.ps1"
            )
            self.assertIsNotNone(script)
            script_bytes = script.read()
            self.assertIn(b"\r\n", script_bytes)
            self.assertNotIn(b"\n", script_bytes.replace(b"\r\n", b""))

    def test_windows_patch_directories_must_match_the_lock(self) -> None:
        """Hashing the locked entries does not notice an unlocked patch.

        macOS compares the whole directory listing against its lock, which is
        what made it refuse to build from a tree full of file-sync conflict
        copies. Windows enumerated the lock only.
        """
        build = (WINDOWS / "scripts" / "Build-Preview.ps1").read_text(encoding="utf-8")
        self.assertIn("does not match the lock", build)
        self.assertIn('Directory = "platform\\patches\\weasel"', build)
        self.assertIn('Directory = "platform\\patches\\librime-1.17"', build)
        self.assertIn("Compare-Object -ReferenceObject $locked", build)

    def test_pe_subsystem_checker_reads_the_optional_header(self) -> None:
        checker = (ROOT / "scripts" / "check_pe_subsystem.py").read_text(encoding="utf-8")
        self.assertIn("IMAGE_SUBSYSTEM_WINDOWS_GUI = 2", checker)
        self.assertIn("IMAGE_SUBSYSTEM_WINDOWS_CUI = 3", checker)
        # PE signature (4) + COFF header (20); Subsystem sits at optional-header
        # offset 68 in both PE32 and PE32+.
        self.assertIn("pe_offset + 4 + 20", checker)
        self.assertIn("optional_header + 68", checker)



if __name__ == "__main__":
    unittest.main()
