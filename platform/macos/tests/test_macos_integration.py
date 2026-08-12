#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-only
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET


MACOS_DIR = Path(__file__).resolve().parents[1]
ROOT = MACOS_DIR.parents[1]
SQUIRREL = ROOT / "third_party" / "squirrel"
PATCH_DIR = ROOT / "platform" / "patches" / "squirrel"
EXPECTED_COMMIT = "876adebaf2f612951dcdca8a591de65401222b9a"


def run(*args: str, cwd: Path = ROOT, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        args,
        cwd=cwd,
        env=env,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )


class MacOSIntegrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.temporary = tempfile.TemporaryDirectory(prefix="yunpin-macos-tests-")
        cls.prepared = Path(cls.temporary.name) / "squirrel"
        run(str(MACOS_DIR / "scripts" / "prepare-source.sh"), str(cls.prepared))

    @classmethod
    def tearDownClass(cls) -> None:
        cls.temporary.cleanup()

    def test_squirrel_and_nested_dependency_locks_match_gitlinks(self) -> None:
        lock = json.loads((MACOS_DIR / "dependencies.lock.json").read_text(encoding="utf-8"))
        platform_lock = json.loads((ROOT / "platform" / "upstream-lock.json").read_text(encoding="utf-8"))
        platform_squirrel = next(item for item in platform_lock["components"] if item["name"] == "Squirrel")
        self.assertEqual(EXPECTED_COMMIT, lock["squirrel_commit"])
        self.assertEqual(EXPECTED_COMMIT, platform_squirrel["commit"])
        self.assertEqual(EXPECTED_COMMIT, run("git", "-C", str(SQUIRREL), "rev-parse", "HEAD").stdout.strip())
        self.assertEqual("1.89.0", lock["boost_version"])

        tree = run("git", "-C", str(SQUIRREL), "ls-tree", "HEAD", "librime", "plum", "Sparkle").stdout
        for name, commit in lock["nested_submodules"].items():
            self.assertIn(f"{commit}\t{name}", tree)

        for archive in lock["archives"]:
            self.assertTrue(
                archive["url"].startswith("https://github.com/")
                or archive["url"].startswith("https://archives.boost.io/")
            )
            self.assertEqual(64, len(archive["sha256"]))
            int(archive["sha256"], 16)
            if "plum_package" in archive:
                self.assertEqual(40, len(archive["commit"]))
                int(archive["commit"], 16)
        boost = [archive for archive in lock["archives"] if archive.get("boost_source")]
        self.assertEqual(1, len(boost))
        self.assertEqual("boost_1_89_0.tar.gz", boost[0]["name"])

    def test_ordered_gpl_patch_set_applies_and_records_base(self) -> None:
        patches = sorted(PATCH_DIR.glob("*.patch"))
        self.assertEqual(10, len(patches))
        for patch in patches:
            text = patch.read_text(encoding="utf-8")
            self.assertIn("SPDX-License-Identifier: GPL-3.0-only", text)
            self.assertIn(f"Base-Commit: {EXPECTED_COMMIT}", text)
        self.assertEqual(EXPECTED_COMMIT, (self.prepared / ".yunpin-base-commit").read_text().strip())

    def test_patch_series_is_digest_locked(self) -> None:
        # Base-Commit alone only proves what a patch was written against; it
        # does not detect an edited patch. Windows pins digests in
        # platform/windows/dependencies.lock.json and macOS must match.
        rows = json.loads((MACOS_DIR / "dependencies.lock.json").read_text(encoding="utf-8"))[
            "squirrel_patches"
        ]
        self.assertEqual([ROOT / row["path"] for row in rows], sorted(PATCH_DIR.glob("*.patch")))
        for row in rows:
            with self.subTest(patch=row["path"]):
                digest = hashlib.sha256((ROOT / row["path"]).read_bytes()).hexdigest()
                self.assertEqual(digest, row["sha256"])

    def test_librime_patch_is_version_locked_and_selects_a_unique_corrector(self) -> None:
        lock = json.loads((MACOS_DIR / "dependencies.lock.json").read_text(encoding="utf-8"))
        rows = lock["librime_patches"]
        patch_dir = ROOT / "platform" / "patches" / "librime-1.16"
        self.assertEqual(
            [ROOT / row["path"] for row in rows], sorted(patch_dir.glob("*.patch"))
        )
        for row in rows:
            patch = ROOT / row["path"]
            self.assertEqual(hashlib.sha256(patch.read_bytes()).hexdigest(), row["sha256"])
            patch_text = patch.read_text(encoding="utf-8")
            self.assertIn(lock["nested_submodules"]["librime"], patch_text)
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
            subprocess.run(
                ["git", "-C", str(SQUIRREL / "librime"), "apply", "--check", str(patch)],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )

        module = (ROOT / "librime-yunpin" / "src" / "yunpin_module.cpp").read_text(
            encoding="utf-8"
        )
        self.assertIn('Register("yunpin_corrector"', module)
        self.assertNotIn('Register("corrector"', module)
        adapter = (
            ROOT / "librime-yunpin" / "src" / "rime_yunpin_corrector.cpp"
        ).read_text(encoding="utf-8")
        self.assertIn("kMaxCorrectionsPerOffset = 16", adapter)
        self.assertIn("if (!enabled_ || !results", adapter)
        self.assertIn("return nullptr;", adapter)
        self.assertNotIn("new NearSearchCorrector", adapter)

    def test_preview_has_unique_input_method_identity_and_offline_updates(self) -> None:
        with (self.prepared / "resources" / "Info.plist").open("rb") as stream:
            info = plistlib.load(stream)
        bundle = "io.github.kukuyan.inputmethod.YunPin"
        self.assertEqual(bundle, info["TISInputSourceID"])
        self.assertEqual("YunPin", info["CFBundleExecutable"])
        self.assertEqual("YunPin_Connection", info["InputMethodConnectionName"])
        self.assertEqual("YunPin.SquirrelInputController", info["InputMethodServerControllerClass"])
        self.assertFalse(info["SUEnableAutomaticChecks"])
        self.assertNotIn("SUFeedURL", info)
        self.assertNotIn("SUPublicEDKey", info)
        modes = info["ComponentInputModeDict"]["tsInputModeListKey"]
        self.assertEqual({f"{bundle}.Hans", f"{bundle}.Hant"}, set(modes))

        sources = "\n".join(
            path.read_text(encoding="utf-8")
        for path in (self.prepared / "sources").glob("*.swift")
        )
        self.assertIn("/Library/Input Methods/YunPin.app", sources)
        self.assertIn('"Application Support", "YunPin", "Rime"', sources)
        self.assertIn("SquirrelApp.userDir.path(percentEncoded: false)", sources)
        self.assertIn("SquirrelApp.logDir.path(percentEncoded: false)", sources)
        self.assertNotIn("SquirrelApp.userDir.path()", sources)
        self.assertNotIn("rime.github.io/release/squirrel", sources)
        self.assertNotIn("encrypted sync is not connected", sources)
        self.assertIn("--sync <request nonce>", sources)
        self.assertIn("validMaintenanceNonce", sources)
        self.assertIn("requestNonce: String", sources)
        self.assertIn("join_maintenance_thread", sources)
        self.assertIn('acknowledgementName = "rime-maintenance.ack"', sources)
        self.assertIn("O_DIRECTORY | O_NOFOLLOW", sources)
        self.assertIn("O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW", sources)
        self.assertIn("fchmod(temporaryFD, 0o600)", sources)
        self.assertIn("fstat(temporaryFD, &temporaryMetadata)", sources)
        self.assertIn("temporaryMetadata.st_uid == geteuid()", sources)
        self.assertIn("renameat(directoryFD, temporaryName, directoryFD, acknowledgementName)", sources)
        self.assertIn("fsync(directoryFD)", sources)
        self.assertIn("rimeSyncCompletionQueue.async", sources)
        self.assertIn("rimeSyncCompletionQueue.sync", sources)
        self.assertIn("rimeSyncStateLock", sources)
        self.assertIn("rimeSyncInFlight", sources)
        self.assertIn("Thread.isMainThread", sources)
        self.assertIn("DispatchQueue.main.async", sources)
        self.assertIn("prepareForUserDataMaintenance", sources)
        self.assertIn("maintenanceIdleInterval", sources)
        self.assertIn("liveControllers", sources)
        self.assertIn("controllerCallbacksAreMainThreadOnly", sources)
        self.assertIn("controller.rimeAPI.get_context(session, &context)", sources)
        self.assertIn("context.composition.preedit != nil", sources)
        self.assertIn("controller.rimeAPI.free_context(&context)", sources)
        self.assertIn("controller.sessionLifetime.requestDestroy()", sources)
        self.assertIn("guard controller.session == 0 else", sources)
        self.assertIn("invalidateAllSessionsForRimeShutdown", sources)
        self.assertIn("controllers.allSatisfy { $0.session == 0 }", sources)
        self.assertIn("operationInProgress", sources)
        self.assertIn("controller.chordTimer?.isValid != true", sources)
        self.assertIn("Deferred private Rime maintenance while input is active or recently used.", sources)
        self.assertIn('"busy:"', sources)
        self.assertIn("Rejected overlapping private Rime maintenance request.", sources)
        application_delegate = (
            self.prepared / "sources" / "SquirrelApplicationDelegate.swift"
        ).read_text(encoding="utf-8")
        maintenance = application_delegate[
            application_delegate.index("  func syncUserData(requestNonce: String)") :
            application_delegate.index("  func waitForRimeSyncCompletion()")
        ]
        self.assertLess(
            maintenance.index("prepareForUserDataMaintenance()"),
            maintenance.index("rimeAPI.sync_user_data()"),
        )
        shutdown = application_delegate[
            application_delegate.index("  func shutdownRime() -> Bool") :
            application_delegate.index("  func workspaceWillPowerOff")
        ]
        self.assertLess(
            shutdown.index("invalidateAllSessionsForRimeShutdown()"),
            shutdown.index("rimeAPI.finalize()"),
        )
        deploy = application_delegate[
            application_delegate.index("  func deploy()") :
            application_delegate.index("  private func writeMaintenanceAcknowledgement")
        ]
        self.assertIn("guard Thread.isMainThread", deploy)
        self.assertIn("DispatchQueue.main.async", deploy)
        self.assertIn("guard self.shutdownRime()", deploy)
        self.assertLess(
            sources.index("rimeAPI.sync_user_data()"),
            sources.index("rimeSyncCompletionQueue.async"),
        )
        self.assertLess(
            sources.index("rimeSyncCompletionQueue.async"),
            sources.index("self.rimeAPI.join_maintenance_thread()"),
        )
        self.assertLess(
            sources.index("self.rimeAPI.join_maintenance_thread()"),
            sources.index("self.writeMaintenanceAcknowledgement(requestNonce)"),
        )
        self.assertNotIn("userInfo", sources)
        self.assertNotIn("NSApp.squirrelAppDelegate.syncUserData()", sources)
        self.assertIn('appendingPathComponent("YunPin", isDirectory: true)', sources)
        for component in ("Sync", "native-events", "incoming"):
            self.assertIn(
                f'appendingPathComponent("{component}", isDirectory: true)', sources
            )
        self.assertIn("YunPinStartNativeSelectionSpoolerV1", sources)
        self.assertGreaterEqual(sources.count("YunPinStopNativeSelectionSpoolerV1"), 2)

    def test_registration_refreshes_tis_without_changing_enabled_modes(self) -> None:
        source = (self.prepared / "sources" / "InputSource.swift").read_text(
            encoding="utf-8"
        )
        register = source[
            source.index("  func register() -> Bool {") : source.index("  func enable(")
        ]
        refresh = register.index("TISRegisterInputSource(SquirrelApp.appDir as CFURL)")
        enabled_branch = register.index("if !enabledInputModes.isEmpty")
        self.assertLess(refresh, enabled_branch)
        self.assertIn("preserving enabled YunPin method(s)", register)
        self.assertIn("return false", register)
        self.assertIn("return true", register)
        self.assertNotIn("TISDisableInputSource", register)
        self.assertNotIn("TISSelectInputSource", register)
        self.assertNotIn("Already registered", register)
        main = (self.prepared / "sources" / "Main.swift").read_text(encoding="utf-8")
        self.assertIn("guard installer.register() else", main)
        self.assertIn("exit(EXIT_FAILURE)", main)

    def test_install_reconciles_persistent_input_source_state_in_login_session(self) -> None:
        source = (self.prepared / "sources" / "InputSource.swift").read_text(
            encoding="utf-8"
        )
        reconcile = source[
            source.index("  func reconcile() -> Bool {") : source.index("  func select(")
        ]
        self.assertIn("TISCopyCurrentKeyboardInputSource", reconcile)
        self.assertIn("guard let previousInputSource", reconcile)
        self.assertIn("TISEnableInputSource", reconcile)
        self.assertIn("TISSelectInputSource(inputSource)", reconcile)
        self.assertIn("TISSelectInputSource(previousInputSource)", reconcile)
        self.assertIn("var reconciliationSucceeded = true", reconcile)
        self.assertIn("if !reconciliationSucceeded { return false }", reconcile)
        self.assertLess(
            reconcile.index("TISSelectInputSource(inputSource)"),
            reconcile.index("TISSelectInputSource(previousInputSource)"),
        )
        self.assertNotIn("targetInputSourceIDs", reconcile)
        self.assertIn("Cannot safely restore", reconcile)
        self.assertIn("return false", reconcile)
        main = (self.prepared / "sources" / "Main.swift").read_text(encoding="utf-8")
        self.assertIn('case "--reconcile-input-source":', main)
        self.assertIn("guard installer.reconcile() else", main)

        postinstall = (MACOS_DIR / "package" / "postinstall").read_text(
            encoding="utf-8"
        )
        register = postinstall.index('"$executable" --register-input-source')
        reconcile_call = postinstall.index('"$executable" --reconcile-input-source')
        self.assertLess(register, reconcile_call)
        self.assertIn('/bin/launchctl asuser "$login_uid"', postinstall)
        self.assertIn('/usr/bin/sudo -u "$login_user"', postinstall)
        self.assertNotIn('"$executable" --enable-input-source', postinstall)
        self.assertIn('if [[ ! -x "$executable" ]]', postinstall)
        self.assertIn('if [[ "$login_user" == "root" ]]', postinstall)

    def test_public_sync_agent_is_bundled_but_not_resident_enabled_by_root(self) -> None:
        build = (MACOS_DIR / "scripts" / "build-preview.sh").read_text(
            encoding="utf-8"
        )
        agent_build = (MACOS_DIR / "scripts" / "build-sync-agents.sh").read_text(
            encoding="utf-8"
        )
        sign = (MACOS_DIR / "scripts" / "sign-app-adhoc.sh").read_text(
            encoding="utf-8"
        )
        verify = (MACOS_DIR / "scripts" / "verify-app.sh").read_text(
            encoding="utf-8"
        )
        postinstall = (MACOS_DIR / "package" / "postinstall").read_text(
            encoding="utf-8"
        )
        source_archive = (MACOS_DIR / "scripts" / "make-source-archive.sh").read_text(
            encoding="utf-8"
        )

        self.assertIn('"${MACOS_DIR}/scripts/build-sync-agents.sh"', build)
        self.assertIn('sync-agent/public/yunpin-sync-agent', build)
        self.assertIn('Contents/MacOS/yunpin-sync-agent', build)
        self.assertIn('sync_support="$shared_support/SyncAgent"', build)
        self.assertNotIn("e2e-private", build)
        self.assertIn("-tags=yunpin_pairing_private", agent_build)
        self.assertIn('private_root="$build_root/e2e-private/macos"', agent_build)
        self.assertIn("MACOSX_DEPLOYMENT_TARGET=13.0", agent_build)
        self.assertIn('xcrun vtool -show-build "$output"', agent_build)
        self.assertIn("go mod verify", agent_build)
        self.assertIn("package_go_licenses.py", agent_build)
        self.assertIn('publicReleaseEligible', agent_build)
        self.assertIn('sign_adhoc "$sync_agent"', sign)
        self.assertLess(sign.index('sign_adhoc "$sync_agent"'), sign.index('sign_adhoc "$app"'))
        for required in (
            '"$sync_agent" install-probe',
            '"$sync_agent" pairing-invite',
            'yunpin-sync-agent: unknown command',
            'lipo -archs "$sync_agent"',
        ):
            self.assertIn(required, verify)
        for forbidden in (
            "yunpin-sync-agent",
            "SyncAgent",
            "Library/LaunchAgents",
            "Keychain",
        ):
            self.assertNotIn(forbidden, postinstall)
        for tree in ("desktopagent", "localstore", "protocol", "syncclient"):
            self.assertIn(tree, source_archive)
        self.assertIn("third_party/go-modules.lock.json", source_archive)
        self.assertNotIn("rime_userdb_snapshot", source_archive)

    def test_expression_commit_text_cannot_trigger_platform_side_effects(self) -> None:
        controller = (self.prepared / "sources" / "SquirrelInputController.swift").read_text(
            encoding="utf-8"
        )
        self.assertIn("typed, explicitly armed action", controller)
        self.assertIn("client.insertText(string, replacementRange: .empty)", controller)
        for forbidden in (
            "yunpin-search:",
            "yunpin-fav:",
            "NSWorkspace.shared.open",
            "persistFavorite",
            "favorites.jsonl",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, controller)

        filter_source = (ROOT / "librime-yunpin" / "src" / "rime_yunpin_filter.cpp").read_text(
            encoding="utf-8"
        )
        self.assertNotIn("YunPinSearchCandidate", filter_source)
        self.assertNotIn("YunPinFavoriteCandidate", filter_source)
        self.assertNotIn("yunpin-search:", filter_source)
        self.assertNotIn("yunpin-fav:", filter_source)

    def test_reentrant_client_callbacks_hold_session_through_final_rime_use(self) -> None:
        controller = (
            self.prepared / "sources" / "SquirrelInputController.swift"
        ).read_text(encoding="utf-8")
        self.assertIn("func withSessionOperation<Result>", controller)

        commit = controller[
            controller.index("  override func commitComposition") : controller.index(
                "  override func menu()"
            )
        ]
        self.assertLess(commit.index("withSessionOperation"), commit.index("get_input"))
        self.assertLess(commit.index("commit(string:"), commit.index("clear_composition"))
        self.assertIn("operation lease keeps the same session valid", commit)

        update = controller[
            controller.index("  func rimeUpdate()") : controller.index(
                "  func commit(string:"
            )
        ]
        self.assertLess(update.index("beginOperation()"), update.index("rimeConsumeCommittedText"))
        self.assertIn("defer { lifetime.endOperation() }", update)

        subprocess.run(
            ["/usr/bin/xcrun", "swift", str(MACOS_DIR / "tests" / "session_lifetime_harness.swift")],
            cwd=ROOT,
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

    def test_original_artwork_replaces_upstream_visible_assets(self) -> None:
        upstream_logo = SQUIRREL / "Rime.icon" / "Assets" / "logo.svg"
        prepared_logo = self.prepared / "Rime.icon" / "Assets" / "logo.svg"
        self.assertNotEqual(
            hashlib.sha256(upstream_logo.read_bytes()).digest(),
            hashlib.sha256(prepared_logo.read_bytes()).digest(),
        )
        ET.parse(prepared_logo)
        project = (self.prepared / "Squirrel.xcodeproj" / "project.pbxproj").read_text(encoding="utf-8")
        self.assertIn("resources/yunpin.pdf", project)
        self.assertNotIn("resources/rime.pdf", project)
        self.assertIn("path = Rime.icon", project)

    def test_preview_manifest_separates_merged_module_from_host_evidence(self) -> None:
        manifest = json.loads((MACOS_DIR / "preview-manifest.json").read_text(encoding="utf-8"))
        self.assertEqual("development-preview", manifest["channel"])
        self.assertTrue(manifest["yunpin_module_merged"])
        self.assertTrue(manifest["yunpin_ranking_headless_e2e"])
        self.assertFalse(manifest["yunpin_ranking_native_host_e2e"])
        self.assertTrue(manifest["yunpin_typo_correction_librime_e2e"])
        self.assertTrue(manifest["yunpin_typo_correction_full_exact_zero_expansion_e2e"])
        self.assertTrue(manifest["yunpin_typo_correction_single_bridge_e2e"])
        self.assertTrue(manifest["yunpin_typo_correction_double_error_fail_closed_e2e"])
        self.assertTrue(manifest["yunpin_typo_correction_rank_guard_e2e"])
        self.assertEqual(16, manifest["yunpin_typo_correction_max_edges_per_offset"])
        self.assertEqual(32, manifest["yunpin_typo_correction_max_searches_per_input"])
        self.assertFalse(manifest["yunpin_typo_correction_native_host_e2e"])
        self.assertFalse(manifest["yunpin_typo_correction_production_dictionary_e2e"])
        self.assertTrue(manifest["yunpin_session_correction_librime_e2e"])
        self.assertTrue(manifest["yunpin_candidate_pinyin_toggle_librime_e2e"])
        self.assertFalse(
            manifest["yunpin_candidate_pinyin_toggle_native_host_e2e"]
        )
        self.assertFalse(manifest["yunpin_learning_bridge"])
        self.assertFalse(manifest["yunpin_local_model_sidecar"])
        self.assertFalse(manifest["mixed_chinese_english_input"])
        self.assertFalse(manifest["encrypted_cloud_sync"])
        self.assertFalse(manifest["production_signed"])

        fixture_schema = (
            MACOS_DIR / "tests" / "fixtures" / "yunpin_e2e.schema.yaml"
        ).read_text(encoding="utf-8")
        self.assertIn("- echo_translator", fixture_schema)
        self.assertIn("long_correction_guard: true", fixture_schema)
        self.assertIn("typo_reviewed_confusions: false", fixture_schema)
        fixture_source = (MACOS_DIR / "tests" / "yunpin_rime_e2e.cpp").read_text(
            encoding="utf-8"
        )
        self.assertIn('"shujukushiyongdeshinagebanben"', fixture_source)
        self.assertIn('"shousubijiaokuaideshihouu"', fixture_source)
        self.assertIn('"shouusubijiaokuaideshihouu"', fixture_source)
        self.assertIn('"youceshizhanghaoma"', fixture_source)

    def test_rime_overlay_enables_the_bounded_private_filter(self) -> None:
        overlay = (ROOT / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn("engine/filters/@before 0\": yunpin_filter@yunpin", overlay)
        self.assertIn(
            "engine/filters/@before last\": "
            "yunpin_comment_filter@yunpin_comment_visibility",
            overlay,
        )
        self.assertIn("name: yunpin_show_candidate_pinyin", overlay)
        self.assertIn("states: [拼音关, 拼音开]", overlay)
        self.assertNotIn("reset:", overlay)
        self.assertIn("translator/keep_comments\": true", overlay)
        self.assertIn('corrector\": "［{comment}］"', overlay)
        default_overlay = (
            ROOT / "platform" / "rime" / "squirrel" / "default.custom.yaml"
        ).read_text(encoding="utf-8")
        self.assertIn(
            "switcher/save_options/@after last\": yunpin_show_candidate_pinyin",
            default_overlay,
        )
        self.assertIn("yunpin/snapshot\": yunpin/private.tsv", overlay)
        self.assertIn("yunpin/max_candidates\": 2", overlay)
        self.assertIn("yunpin/short_input_guard\": true", overlay)
        self.assertIn("yunpin/session_learning\": false", overlay)
        self.assertIn("translator/enable_correction\": false", overlay)
        self.assertIn(
            "translator/corrector_component\": yunpin_corrector", overlay
        )
        self.assertIn("yunpin/typo_correction\": false", overlay)
        self.assertIn("yunpin/typo_reviewed_confusions\": false", overlay)

    def test_dependency_fetch_initializes_librime_before_runtime_copy(self) -> None:
        fetch = (MACOS_DIR / "scripts" / "fetch-dependencies.sh").read_text(encoding="utf-8")
        init = fetch.index('submodule update --init librime')
        action_install = fetch.index('./action-install.sh')
        self.assertLess(init, action_install)
        self.assertIn('actual_librime="$(git -C "$source_dir/librime" rev-parse HEAD)"', fetch)

    def test_plugin_staging_copies_adapter_and_portable_engine(self) -> None:
        stage = (MACOS_DIR / "scripts" / "stage-yunpin-plugin.sh").read_text(encoding="utf-8")
        build = (MACOS_DIR / "scripts" / "build-librime-yunpin.sh").read_text(encoding="utf-8")
        self.assertIn("librime/plugins/librime-yunpin", stage)
        self.assertIn('${REPO_ROOT}/engine/include', stage)
        self.assertIn('${REPO_ROOT}/engine/src', stage)
        self.assertIn('failed to atomically refresh librime-yunpin staging', stage)
        self.assertIn('RIME_PLUGINS=librime-yunpin', build)
        self.assertIn('cmake --fresh -S . -B "$build_dir"', build)
        self.assertIn('-DBUILD_MERGED_PLUGINS=ON', build)
        self.assertIn('rime_require_module_yunpin', build)
        self.assertNotIn("grep -Fq 'rime_require_module_yunpin'", build)
        self.assertIn('source_dir="$(cd "$source_dir" && pwd)"', build)
        self.assertIn('scripts/test-merged-ranking.sh', build)
        self.assertIn(".yunpin-librime-patchset", stage)
        self.assertIn("apply --reverse --check", stage)
        self.assertIn('git -C "$source_dir/librime" diff --quiet', stage)
        self.assertIn("tracked changes outside the locked patch series", stage)

    def test_merged_librime_build_has_bounded_parallelism(self) -> None:
        build = (MACOS_DIR / "scripts" / "build-librime-yunpin.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('build_jobs="$(resolve_macos_build_jobs)"', build)
        self.assertIn('cmake --build "$build_dir" --parallel "$build_jobs"', build)
        self.assertNotRegex(
            build,
            r"cmake --build[^\n]*--parallel[ \t]*(?:\n|$)",
        )

        workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )
        macos_job = workflow[
            workflow.index("  macos-client:\n") : workflow.index("  required:\n")
        ]
        self.assertIn('YUNPIN_MACOS_BUILD_JOBS: "2"', macos_job)

    def test_macos_build_jobs_default_override_and_validation(self) -> None:
        common = str(MACOS_DIR / "scripts" / "common.sh")
        command = 'source "$1"; resolve_macos_build_jobs'

        default_env = os.environ.copy()
        default_env.pop("YUNPIN_MACOS_BUILD_JOBS", None)
        default = run(
            "/bin/bash",
            "-c",
            command,
            "yunpin-build-jobs-test",
            common,
            env=default_env,
        )
        self.assertEqual("2", default.stdout.strip())

        override_env = default_env.copy()
        override_env["YUNPIN_MACOS_BUILD_JOBS"] = "7"
        override = run(
            "/bin/bash",
            "-c",
            command,
            "yunpin-build-jobs-test",
            common,
            env=override_env,
        )
        self.assertEqual("7", override.stdout.strip())

        for invalid in ("", "0", "-1", "+2", "2.5", "2x", " 2"):
            with self.subTest(invalid=invalid):
                invalid_env = default_env.copy()
                invalid_env["YUNPIN_MACOS_BUILD_JOBS"] = invalid
                result = subprocess.run(
                    [
                        "/bin/bash",
                        "-c",
                        command,
                        "yunpin-build-jobs-test",
                        common,
                    ],
                    cwd=ROOT,
                    env=invalid_env,
                    check=False,
                    text=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                )
                self.assertNotEqual(0, result.returncode)
                self.assertEqual("", result.stdout)
                self.assertIn(
                    "YUNPIN_MACOS_BUILD_JOBS must be a positive integer",
                    result.stderr,
                )

    def test_xcode_resolver_respects_the_active_versioned_selection(self) -> None:
        with tempfile.TemporaryDirectory(prefix="yunpin-xcode-select-") as temporary:
            root = Path(temporary)
            developer = root / "Xcode_26.3.app" / "Contents" / "Developer"
            developer_bin = developer / "usr" / "bin"
            developer_bin.mkdir(parents=True)
            xcodebuild = developer_bin / "xcodebuild"
            xcodebuild.write_text(
                "#!/bin/sh\nprintf 'Xcode 26.3\\nBuild version TEST\\n'\n",
                encoding="utf-8",
            )
            xcodebuild.chmod(0o755)

            fallback_app = root / "Xcode.app"
            fallback_bin = fallback_app / "Contents" / "Developer" / "usr" / "bin"
            fallback_bin.mkdir(parents=True)
            fallback_xcodebuild = fallback_bin / "xcodebuild"
            fallback_xcodebuild.write_text(
                "#!/bin/sh\nprintf 'Xcode 16.4\\nBuild version FALLBACK\\n'\n",
                encoding="utf-8",
            )
            fallback_xcodebuild.chmod(0o755)

            fake_bin = root / "bin"
            fake_bin.mkdir()
            xcode_select = fake_bin / "xcode-select"
            xcode_select.write_text(
                "#!/bin/sh\nprintf '%s\\n' \"$YUNPIN_TEST_DEVELOPER_DIR\"\n",
                encoding="utf-8",
            )
            xcode_select.chmod(0o755)

            env = os.environ.copy()
            env.pop("DEVELOPER_DIR", None)
            env.pop("YUNPIN_XCODE_APP_PATH", None)
            env["PATH"] = f"{fake_bin}:/usr/bin:/bin"
            env["YUNPIN_TEST_DEVELOPER_DIR"] = str(developer)
            result = run(
                "bash",
                "-c",
                (
                    'source "$1"; YUNPIN_DEFAULT_XCODE_PATHS=("$2"); '
                    'require_full_xcode; printf "%s\\n" "$DEVELOPER_DIR"'
                ),
                "yunpin-xcode-resolver-test",
                str(MACOS_DIR / "scripts" / "common.sh"),
                str(fallback_app),
                env=env,
            )
            self.assertEqual(str(developer), result.stdout.strip())

    def test_ci_pairs_xcode_26_with_the_macos_26_runner(self) -> None:
        workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )
        macos_job = workflow[
            workflow.index("  macos-client:\n") : workflow.index("  required:\n")
        ]
        self.assertIn("runs-on: macos-26", macos_job)
        self.assertIn(
            "sudo xcode-select --switch /Applications/Xcode_26.4.1.app",
            macos_job,
        )
        self.assertNotIn("macos-15", macos_job)
        self.assertNotIn("Xcode_26.3.app", macos_job)

    def test_generated_bundle_is_signed_bottom_up_and_fails_closed(self) -> None:
        build = (MACOS_DIR / "scripts" / "build-preview.sh").read_text(encoding="utf-8")
        # Xcode 26 schedules RegisterWithLaunchServices for the generated app.
        # Remove only that exact staging path, then verify it is not active.
        self.assertNotIn("REGISTER_WITH_LAUNCH_SERVICES", build)
        self.assertNotIn("WRAPPER_EXTENSION", build)
        self.assertNotIn("REGISTER_APP=NO", build)
        self.assertIn('scripts/verify-launchservices-state.sh" --unregister "$app"', build)
        self.assertNotIn('"$lsregister" -dump', build)
        self.assertNotIn("lsregister -f", build)
        verify_app = build.index('scripts/verify-app.sh" --require-universal "$app"')
        launchservices = build.index(
            'scripts/verify-launchservices-state.sh" --unregister "$app"'
        )
        self.assertLess(verify_app, launchservices)
        self.assertIn('scripts/sign-app-adhoc.sh" "$app"', build)
        self.assertNotIn("warning: bundle signing failed", build)

        launchservices_script = (
            MACOS_DIR / "scripts" / "verify-launchservices-state.sh"
        ).read_text(encoding="utf-8")
        self.assertIn("Database is seeded.", launchservices_script)
        self.assertIn("expected_records", launchservices_script)
        self.assertIn("complete_records", launchservices_script)
        self.assertIn("launch-disabled", launchservices_script)
        self.assertIn('"$lsregister" -u "$app"', launchservices_script)
        self.assertIn("unregister_status", launchservices_script)
        self.assertIn("remains actively registered", launchservices_script)
        self.assertIn("incomplete or unrecognized", launchservices_script)

        signing = (MACOS_DIR / "scripts" / "sign-app-adhoc.sh").read_text(
            encoding="utf-8"
        )
        xpc_executable = signing.index("-path '*/Contents/MacOS/*'")
        xpc_bundle = signing.index("-name '*.xpc'")
        updater = signing.index('sign_adhoc "$updater"')
        sparkle = signing.index('sign_adhoc "$sparkle"')
        librime = signing.index("-name 'librime*.dylib'")
        plugin = signing.index("-name '*.dylib'")
        helper = signing.index("-name 'rime*'")
        root = signing.rindex('sign_adhoc "$app"')
        verify = signing.index('codesign --verify --deep --strict --verbose=2 "$app"')
        self.assertEqual(
            sorted([xpc_executable, xpc_bundle, updater, sparkle, librime, plugin, helper, root, verify]),
            [xpc_executable, xpc_bundle, updater, sparkle, librime, plugin, helper, root, verify],
        )

        verifier = (MACOS_DIR / "scripts" / "verify-app.sh").read_text(
            encoding="utf-8"
        )
        package = (MACOS_DIR / "scripts" / "package-preview.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("codesign --verify --deep --strict", verifier)
        self.assertIn("does not have a valid strict deep signature", verifier)
        self.assertIn("codesign --verify --deep --strict", package)
        self.assertIn("does not have a valid strict deep signature", package)
        self.assertNotIn("skipping strict", verifier + package)

        readme = (MACOS_DIR / "README.md").read_text(encoding="utf-8")
        self.assertIn("ad-hoc app signature and unsigned", readme)

    def test_launchservices_gate_distinguishes_active_records_from_tombstones(self) -> None:
        with tempfile.TemporaryDirectory(prefix="yunpin-launchservices-test-") as temporary:
            root = Path(temporary)
            app = root / "YunPin.app"
            app.mkdir()
            dump = root / "dump.txt"
            fake = root / "lsregister"
            fake.write_text(
                "#!/bin/bash\n"
                "case \"${1:-}\" in\n"
                "  -u) exit \"${YUNPIN_TEST_LS_UNREGISTER_STATUS:-0}\" ;;\n"
                "  -dump) /bin/cat \"$YUNPIN_TEST_LS_DUMP\"; "
                "exit \"${YUNPIN_TEST_LS_STATUS:-0}\" ;;\n"
                "  *) exit 64 ;;\n"
                "esac\n",
                encoding="utf-8",
            )
            fake.chmod(0o755)
            env = os.environ.copy()
            env["YUNPIN_LSREGISTER"] = str(fake)
            env["YUNPIN_TEST_LS_DUMP"] = str(dump)
            script = str(MACOS_DIR / "scripts" / "verify-launchservices-state.sh")

            def records(*items: tuple[Path, str | None]) -> str:
                body = [
                    "Database is seeded.\n",
                    f"Bundle:                     1024 ( 1 KB) {len(items)} units\n",
                    "----------------------------------------\n",
                ]
                for path, flags in items:
                    body.extend(
                        [
                            "bundle id:                  YunPin (0x1)\n",
                            f"path:                       {path} (0x2)\n",
                        ]
                    )
                    if flags is not None:
                        body.append(f"bundle flags:               {flags}\n")
                    body.append("----------------------------------------\n")
                return "".join(body)

            def run_dump(contents: str) -> subprocess.CompletedProcess[str]:
                dump.write_text(contents, encoding="utf-8")
                return subprocess.run(
                    [script, str(app)], cwd=ROOT, env=env, text=True,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                )

            def unregister_with_dump(
                contents: str, status: int
            ) -> subprocess.CompletedProcess[str]:
                dump.write_text(contents, encoding="utf-8")
                child_env = env.copy()
                child_env["YUNPIN_TEST_LS_UNREGISTER_STATUS"] = str(status)
                return subprocess.run(
                    [script, "--unregister", str(app)],
                    cwd=ROOT,
                    env=child_env,
                    text=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                )

            active = run_dump(records((app, "ui-element")))
            self.assertNotEqual(0, active.returncode)
            self.assertIn("remains actively registered", active.stderr)

            # A freshly provisioned macOS runner can have a seeded, valid
            # LaunchServices database whose Bundle table is explicitly empty.
            # Zero is a real table cardinality, not the parser's "not seen"
            # sentinel, and therefore proves that the target path is absent.
            empty = run_dump(records())
            self.assertEqual(0, empty.returncode, empty.stderr)

            missing_empty_table = run_dump(
                "Database is seeded.\n"
                "All units:                       0 (   Zero KB) 0 units\n"
            )
            self.assertNotEqual(0, missing_empty_table.returncode)
            self.assertIn("incomplete or unrecognized", missing_empty_table.stderr)

            disabled = run_dump(records((app, "ui-element  launch-disabled")))
            self.assertEqual(0, disabled.returncode, disabled.stderr)

            idempotent = unregister_with_dump(
                records((app, "ui-element  launch-disabled")), 23
            )
            self.assertEqual(0, idempotent.returncode, idempotent.stderr)
            self.assertIn("unregister returned status 23", idempotent.stderr)

            unregister_failed_active = unregister_with_dump(
                records((app, "ui-element")), 23
            )
            self.assertNotEqual(0, unregister_failed_active.returncode)
            self.assertIn("after unregister status 23", unregister_failed_active.stderr)

            mixed = run_dump(
                records(
                    (app, "ui-element  launch-disabled"),
                    (app, "ui-element"),
                )
            )
            self.assertNotEqual(0, mixed.returncode)
            self.assertIn("remains actively registered", mixed.stderr)

            absent = run_dump(records((root / "Other.app", "ui-element")))
            self.assertEqual(0, absent.returncode, absent.stderr)

            # Xcode registers the app recursively. A nested Sparkle helper may
            # remain as its own LaunchServices record after the root input
            # method is unregistered; its descendant path is not the target
            # bundle and must not be mistaken for exact-path format drift.
            nested_helper = (
                app
                / "Contents"
                / "Frameworks"
                / "Sparkle.framework"
                / "Updater.app"
            )
            descendant = run_dump(records((nested_helper, "ui-element")))
            self.assertEqual(0, descendant.returncode, descendant.stderr)

            trailing_root = run_dump(
                "Database is seeded.\n"
                "Bundle:                     1024 ( 1 KB) 1 units\n"
                "----------------------------------------\n"
                "bundle id:                  YunPin (0x1)\n"
                f"path:                       {app}/ (0x2)\n"
                "bundle flags:               ui-element\n"
                "----------------------------------------\n"
            )
            self.assertNotEqual(0, trailing_root.returncode)
            self.assertIn("incomplete or unrecognized", trailing_root.stderr)

            truncated = run_dump(
                "Database is seeded.\n"
                "Bundle:                     1024 ( 1 KB) 1 units\n"
                "----------------------------------------\n"
                "bundle id:                  YunPin (0x1)\n"
            )
            self.assertNotEqual(0, truncated.returncode)
            self.assertIn("incomplete or unrecognized", truncated.stderr)

            missing_path = run_dump(
                "Database is seeded.\n"
                "Bundle:                     1024 ( 1 KB) 1 units\n"
                "----------------------------------------\n"
                "bundle id:                  YunPin (0x1)\n"
                "bundle flags:               ui-element\n"
                "----------------------------------------\n"
            )
            self.assertNotEqual(0, missing_path.returncode)
            self.assertIn("incomplete or unrecognized", missing_path.stderr)
            self.assertIn("bundle_tables=1", missing_path.stderr)
            self.assertIn("expected_bundle_records=1", missing_path.stderr)
            self.assertIn("observed_bundle_records=1", missing_path.stderr)

            missing_flags = run_dump(records((app, None)))
            self.assertNotEqual(0, missing_flags.returncode)
            self.assertIn("incomplete or unrecognized", missing_flags.stderr)

            malformed_target_path = run_dump(
                "Database is seeded.\n"
                "Bundle:                     1024 ( 1 KB) 1 units\n"
                "----------------------------------------\n"
                "bundle id:                  YunPin (0x1)\n"
                f"path => {app} (0x2)\n"
                "bundle flags:               ui-element  launch-disabled\n"
                "----------------------------------------\n"
            )
            self.assertNotEqual(0, malformed_target_path.returncode)
            self.assertIn("incomplete or unrecognized", malformed_target_path.stderr)

            malformed = run_dump("unexpected output\n")
            self.assertNotEqual(0, malformed.returncode)
            self.assertIn("unrecognized database dump", malformed.stderr)

            dump.write_text(
                records((app, "ui-element  launch-disabled")), encoding="utf-8"
            )
            env["YUNPIN_TEST_LS_STATUS"] = "9"
            failed_dump = subprocess.run(
                [script, str(app)], cwd=ROOT, env=env, text=True,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            self.assertNotEqual(0, failed_dump.returncode)
            self.assertIn("unable to inspect LaunchServices", failed_dump.stderr)

    def test_adhoc_signing_script_orders_targets_and_stops_on_failure(self) -> None:
        with tempfile.TemporaryDirectory(prefix="yunpin-signing-test-") as temporary:
            root = Path(temporary)
            app = root / "YunPin.app"
            sparkle = app / "Contents" / "Frameworks" / "Sparkle.framework"
            version = sparkle / "Versions" / "B"
            xpc_root = version / "XPCServices"
            targets = [
                xpc_root / "Downloader.xpc" / "Contents" / "MacOS" / "Downloader",
                xpc_root / "Installer.xpc" / "Contents" / "MacOS" / "Installer",
                version / "Updater.app" / "Contents" / "MacOS" / "Updater",
                version / "Autoupdate",
                app / "Contents" / "Frameworks" / "librime.1.dylib",
                app / "Contents" / "Frameworks" / "rime-plugins" / "librime-lua.dylib",
                app / "Contents" / "MacOS" / "rime_deployer",
                app / "Contents" / "MacOS" / "yunpin-sync-agent",
            ]
            for target in targets:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text("fixture\n", encoding="utf-8")
                target.chmod(0o755)
            (sparkle / "Versions" / "Current").symlink_to("B")

            fake_bin = root / "bin"
            fake_bin.mkdir()
            log = root / "codesign.log"
            fake_codesign = fake_bin / "codesign"
            fake_codesign.write_text(
                "#!/bin/bash\n"
                "set -eu\n"
                "target=\"${!#}\"\n"
                "if [[ \"${1:-}\" == --verify ]]; then\n"
                "  printf 'VERIFY:%s\\n' \"$target\" >> \"$YUNPIN_TEST_CODESIGN_LOG\"\n"
                "  exit 0\n"
                "fi\n"
                "printf '%s\\n' \"$target\" >> \"$YUNPIN_TEST_CODESIGN_LOG\"\n"
                "if [[ -n \"${YUNPIN_TEST_FAIL_TARGET:-}\" && \"$target\" == *\"$YUNPIN_TEST_FAIL_TARGET\"* ]]; then\n"
                "  exit 9\n"
                "fi\n",
                encoding="utf-8",
            )
            fake_codesign.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{fake_bin}:/usr/bin:/bin"
            env["YUNPIN_TEST_CODESIGN_LOG"] = str(log)
            script = str(MACOS_DIR / "scripts" / "sign-app-adhoc.sh")
            run(script, str(app), env=env)

            resolved_version = version.resolve()
            resolved_xpc_root = resolved_version / "XPCServices"
            expected = [
                str(resolved_xpc_root / "Downloader.xpc" / "Contents" / "MacOS" / "Downloader"),
                str(resolved_xpc_root / "Installer.xpc" / "Contents" / "MacOS" / "Installer"),
                str(resolved_xpc_root / "Downloader.xpc"),
                str(resolved_xpc_root / "Installer.xpc"),
                str(resolved_version / "Updater.app"),
                str(resolved_version / "Autoupdate"),
                str(sparkle),
                str(targets[4]),
                str(targets[5]),
                str(targets[6]),
                str(targets[7]),
                str(app),
                f"VERIFY:{app}",
            ]
            self.assertEqual(expected, log.read_text(encoding="utf-8").splitlines())

            log.write_text("", encoding="utf-8")
            env["YUNPIN_TEST_FAIL_TARGET"] = "Updater.app"
            failed = subprocess.run(
                [script, str(app)],
                cwd=ROOT,
                env=env,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertNotEqual(0, failed.returncode)
            self.assertNotIn(str(app), log.read_text(encoding="utf-8").splitlines())

    def test_corresponding_source_staging_stays_with_the_build_root(self) -> None:
        archive = (MACOS_DIR / "scripts" / "make-source-archive.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('mktemp -d "$build_root/.source-archive.XXXXXX"', archive)
        self.assertNotIn('${TMPDIR:-/tmp}/yunpin-source', archive)

    def test_dmg_builder_is_reproducible_allowlisted_and_self_verifying(self) -> None:
        script_path = MACOS_DIR / "scripts" / "make-dmg.sh"
        script = script_path.read_text(encoding="utf-8")
        makefile = (MACOS_DIR / "Makefile").read_text(encoding="utf-8")
        instructions = (MACOS_DIR / "package" / "安装说明.txt").read_text(
            encoding="utf-8"
        )

        self.assertIn("dmg: package", makefile)
        self.assertIn('scripts/make-dmg.sh', makefile)
        self.assertIn(
            "override BUILD_ROOT := $(abspath $(ROOT)/$(BUILD_ROOT))", makefile
        )
        self.assertIn("SOURCE_DATE_EPOCH", script)
        self.assertIn("normalize_hfs_image", script)
        self.assertIn("normalize_udif_segment_id", script)
        self.assertIn('"$hdiutil_bin" verify', script)
        self.assertIn("attach -readonly", script)
        self.assertIn("SHA256SUMS-macOS.txt", script)
        self.assertIn("YunPin-IME-macOS-development-preview.sha256", script)
        self.assertNotIn("YUNPIN_CI", script)
        self.assertIn("未签名开发预览版", instructions)
        self.assertIn("未经 Apple 公证", instructions)

        if os.uname().sysname != "Darwin":
            self.skipTest("native DMG behavior requires macOS hdiutil")

        with tempfile.TemporaryDirectory(prefix="yunpin-dmg-test-") as temporary:
            build_root = Path(temporary) / "build"
            package_dir = build_root / "package"
            package_dir.mkdir(parents=True)
            (package_dir / "YunPin-IME-development-preview.pkg").write_bytes(
                b"unsigned development package fixture\n"
            )
            (
                package_dir / "YunPin-IME-development-preview-source.tar.gz"
            ).write_bytes(b"corresponding source fixture\n")
            env = os.environ.copy()
            env["YUNPIN_MACOS_BUILD_ROOT"] = str(build_root)
            env["SOURCE_DATE_EPOCH"] = "1704067200"

            first = run(str(script_path), env=env)
            dmg = package_dir / "YunPin-IME-macOS-development-preview.dmg"
            checksum = (
                package_dir / "YunPin-IME-macOS-development-preview.sha256"
            )
            self.assertTrue(dmg.is_file())
            self.assertTrue(checksum.is_file())
            first_digest = hashlib.sha256(dmg.read_bytes()).hexdigest()
            self.assertEqual(
                f"{first_digest}  {dmg.name}\n",
                checksum.read_text(encoding="utf-8"),
            )
            self.assertIn("verified read-only DMG contents", first.stdout)
            run("hdiutil", "verify", str(dmg))
            image_info = plistlib.loads(
                run("hdiutil", "imageinfo", "-plist", str(dmg)).stdout.encode()
            )
            self.assertEqual("UDZO", image_info["Format"])

            # Identical inputs plus the fixed source epoch must produce the
            # same byte-for-byte UDZO image, not merely equivalent files.
            run(str(script_path), env=env)
            self.assertEqual(first_digest, hashlib.sha256(dmg.read_bytes()).hexdigest())

    def test_corresponding_source_exports_only_committed_project_files(self) -> None:
        archive = (MACOS_DIR / "scripts" / "make-source-archive.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('git -C "$REPO_ROOT" archive HEAD --', archive)
        self.assertNotIn('cp -R "${REPO_ROOT}/$path"', archive)
        self.assertIn("require_clean_repository", archive)
        self.assertIn("export COPYFILE_DISABLE=1", archive)
        self.assertIn("--exclude='._*'", archive)
        self.assertIn("--exclude='.DS_Store'", archive)
        self.assertIn("contains forbidden macOS metadata files", archive)
        common = (MACOS_DIR / "scripts" / "common.sh").read_text(encoding="utf-8")
        self.assertIn("status --porcelain --untracked-files=normal", common)
        self.assertIn(
            "binaries and corresponding source use the same commit", common
        )

    def test_installer_is_rooted_and_cannot_relocate_to_a_build_copy(self) -> None:
        package = (MACOS_DIR / "scripts" / "package-preview.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('Release/YunPin.app"', package)
        self.assertIn('"$package_root/Library/Input Methods/YunPin.app"', package)
        self.assertIn('mktemp "$build_root/.package-components.XXXXXX"', package)
        self.assertNotIn(".package-components.XXXXXX.plist", package)
        self.assertIn("Set :0:BundleIsRelocatable false", package)
        self.assertIn('--root "$package_root"', package)
        self.assertIn('--component-plist "$component_plist"', package)
        self.assertIn("require_clean_repository", package)
        self.assertNotIn('--component "$app"', package)
        self.assertNotIn('--install-location "/Library/Input Methods"', package)

    def test_public_lunar_database_is_the_only_database_exception(self) -> None:
        verify = (MACOS_DIR / "scripts" / "verify-app.sh").read_text(encoding="utf-8")
        self.assertIn('public_lunar_db="$shared_support/lua/lunar.db"', verify)
        self.assertIn('${REPO_ROOT}/third_party/rime-ice/lua/lunar.db', verify)
        self.assertIn('[[ "$candidate" == "$public_lunar_db" ]]', verify)
        self.assertIn("forbidden personal or credential material", verify)

    def test_config_injection_is_private_atomic_and_non_destructive(self) -> None:
        with tempfile.TemporaryDirectory(prefix="yunpin-rime-config-") as temporary:
            destination = Path(temporary) / "Rime"
            env = os.environ.copy()
            env["YUNPIN_RIME_USER_DIR"] = str(destination)
            script = str(MACOS_DIR / "scripts" / "inject-rime-config.sh")
            run(script, env=env)
            default = destination / "default.custom.yaml"
            self.assertTrue(default.is_file())
            self.assertEqual(0o700, destination.stat().st_mode & 0o777)
            self.assertEqual(0o600, default.stat().st_mode & 0o777)

            default.write_text("preserve-me\n", encoding="utf-8")
            run(script, env=env)
            self.assertEqual("preserve-me\n", default.read_text(encoding="utf-8"))
            run(script, "--force", env=env)
            self.assertIn("schema_list", default.read_text(encoding="utf-8"))

    def test_installer_stages_the_locked_public_lunar_database(self) -> None:
        postinstall = (MACOS_DIR / "package" / "postinstall").read_text(encoding="utf-8")
        self.assertIn('install -d -m 700 -o "$login_user" "$user_rime/lua"', postinstall)
        self.assertIn('"$shared/lua/lunar.db" "$user_rime/lua/lunar.db"', postinstall)
        self.assertIn('install -m 600 -o "$login_user"', postinstall)

    def test_postinstall_migrates_only_the_known_legacy_correction_overlay(self) -> None:
        postinstall = MACOS_DIR / "package" / "postinstall"
        legacy = MACOS_DIR / "tests" / "fixtures" / "legacy_correction_rime_ice.custom.yaml"
        conservative = ROOT / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml"
        previous = (
            MACOS_DIR
            / "tests"
            / "fixtures"
            / "previous_conservative_rime_ice.custom.yaml"
        )
        legacy_hash = hashlib.sha256(legacy.read_bytes()).hexdigest()
        previous_hash = hashlib.sha256(previous.read_bytes()).hexdigest()
        conservative_hash = hashlib.sha256(conservative.read_bytes()).hexdigest()
        source = postinstall.read_text(encoding="utf-8")

        self.assertEqual(
            "19dbc7deda115dbd7e3ea5b28ba9abfe406105d14f3f818d53c0998cf154ca45",
            legacy_hash,
        )
        self.assertEqual(
            "2bb3bf0666495843201a70e226710de18820f4222daf7df15ed94e9e0adcad37",
            previous_hash,
        )
        self.assertEqual(
            "11576819105dc8daa5142413632c9806d1aa7151c82a4cb1db6b8a6b4be0aa6b",
            conservative_hash,
        )
        self.assertIn(f'yunpin_legacy_correction_overlay_sha256="{legacy_hash}"', source)
        self.assertIn(
            f'yunpin_previous_conservative_overlay_sha256="{previous_hash}"',
            source,
        )
        self.assertIn(f'yunpin_conservative_overlay_sha256="{conservative_hash}"', source)
        # The installer accepts the briefly shipped lifecycle candidate only
        # to migrate it back to the current fail-closed overlay.
        self.assertIn(
            'yunpin_lifecycle_candidate_overlay_sha256='
            '"b4289c5cab6db34eba8073f3e15f8718cc57afc9be94640009c181d9c39a835e"',
            source,
        )
        self.assertIn(
            'yunpin_pre_lifecycle_overlay_sha256='
            '"25e07ca2754e0bb67407f44b9f675dd8c54a09b1ee33de94de1016ed7088daa7"',
            source,
        )
        self.assertIn(
            'yunpin_session_learning_hotfix_overlay_sha256='
            '"2aa8d5ec68b18dc0bb887e44e67e1d5d2239d95dead73418b1f3d050279ab093"',
            source,
        )
        self.assertIn(
            'yunpin_previous_default_overlay_sha256='
            '"23039527ee16342493e346cdc80ebf50f6729cc562b6c85436f7f60651e97bfa"',
            source,
        )
        self.assertIn(
            'yunpin_candidate_pinyin_default_overlay_sha256='
            '"857057ff6b5e939c534f644ec24652ae487c85927591e44135e380f61a25fd1b"',
            source,
        )

        with tempfile.TemporaryDirectory(prefix="yunpin-postinstall-migration-") as temporary:
            root = Path(temporary)
            user_overlay = root / "rime_ice.custom.yaml"
            user_overlay.write_bytes(legacy.read_bytes())
            user_overlay.chmod(0o644)
            owner = run("id", "-un").stdout.strip()
            command = (
                'source "$1"; '
                'yunpin_migrate_known_correction_overlay "$2" "$3" "$4"'
            )

            run(
                "bash",
                "-c",
                command,
                "yunpin-postinstall-test",
                str(postinstall),
                str(conservative),
                str(user_overlay),
                owner,
            )

            self.assertEqual(conservative.read_bytes(), user_overlay.read_bytes())
            self.assertEqual(0o600, user_overlay.stat().st_mode & 0o777)
            backups = list(root.glob("rime_ice.custom.yaml.pre-conservative-*"))
            self.assertEqual(1, len(backups))
            self.assertEqual(legacy.read_bytes(), backups[0].read_bytes())
            self.assertEqual(0o600, backups[0].stat().st_mode & 0o777)
            self.assertEqual([], list(root.glob("rime_ice.custom.yaml.migration.*")))

            # The migration is idempotent once the conservative overlay is active.
            run(
                "bash",
                "-c",
                command,
                "yunpin-postinstall-test",
                str(postinstall),
                str(conservative),
                str(user_overlay),
                owner,
            )
            self.assertEqual(backups, list(root.glob("rime_ice.custom.yaml.pre-conservative-*")))

    def test_postinstall_upgrades_the_previous_conservative_overlay(self) -> None:
        postinstall = MACOS_DIR / "package" / "postinstall"
        previous = (
            MACOS_DIR
            / "tests"
            / "fixtures"
            / "previous_conservative_rime_ice.custom.yaml"
        )
        current = ROOT / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml"
        owner = run("id", "-un").stdout.strip()
        command = (
            'source "$1"; '
            'yunpin_migrate_known_correction_overlay "$2" "$3" "$4"'
        )

        with tempfile.TemporaryDirectory(prefix="yunpin-postinstall-menu-") as temporary:
            root = Path(temporary)
            user_overlay = root / "rime_ice.custom.yaml"
            user_overlay.write_bytes(previous.read_bytes())
            run(
                "bash",
                "-c",
                command,
                "yunpin-postinstall-test",
                str(postinstall),
                str(current),
                str(user_overlay),
                owner,
            )
            self.assertEqual(current.read_bytes(), user_overlay.read_bytes())
            backups = list(root.glob("rime_ice.custom.yaml.pre-conservative-*"))
            self.assertEqual(1, len(backups))
            self.assertEqual(previous.read_bytes(), backups[0].read_bytes())

    def test_postinstall_migrates_known_lifecycle_overlays_to_fail_closed(self) -> None:
        postinstall = MACOS_DIR / "package" / "postinstall"
        current = ROOT / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml"
        current_text = current.read_text(encoding="utf-8")
        pre_lifecycle_text = current_text.replace(
            "  # Fail closed until the IMK host supplies a trustworthy positive\n"
            "  # yunpin_learning_allowed signal for a non-secure text field.\n",
            "",
        ).replace('"yunpin/session_learning": false', '"yunpin/session_learning": true')
        hotfix_text = pre_lifecycle_text.replace(
            '"yunpin/session_learning": true',
            '"yunpin/session_learning": false',
        )
        lifecycle_candidate_text = current_text.replace(
            "  # Fail closed until the IMK host supplies a trustworthy positive\n"
            "  # yunpin_learning_allowed signal for a non-secure text field.\n",
            "  # Bounded in-process word learning is on by default. Protected-context\n"
            "  # commits never update habits or enter the native event queue.\n",
        ).replace('"yunpin/session_learning": false', '"yunpin/session_learning": true')
        self.assertEqual(
            "25e07ca2754e0bb67407f44b9f675dd8c54a09b1ee33de94de1016ed7088daa7",
            hashlib.sha256(pre_lifecycle_text.encode("utf-8")).hexdigest(),
        )
        self.assertEqual(
            "2aa8d5ec68b18dc0bb887e44e67e1d5d2239d95dead73418b1f3d050279ab093",
            hashlib.sha256(hotfix_text.encode("utf-8")).hexdigest(),
        )
        self.assertEqual(
            "b4289c5cab6db34eba8073f3e15f8718cc57afc9be94640009c181d9c39a835e",
            hashlib.sha256(lifecycle_candidate_text.encode("utf-8")).hexdigest(),
        )
        owner = run("id", "-un").stdout.strip()
        command = (
            'source "$1"; '
            'yunpin_migrate_known_correction_overlay "$2" "$3" "$4"'
        )

        for label, old_text in (
            ("pre-lifecycle", pre_lifecycle_text),
            ("session-hotfix", hotfix_text),
            ("lifecycle-candidate", lifecycle_candidate_text),
        ):
            with self.subTest(label=label), tempfile.TemporaryDirectory(
                prefix=f"yunpin-postinstall-{label}-"
            ) as temporary:
                root = Path(temporary)
                user_overlay = root / "rime_ice.custom.yaml"
                user_overlay.write_text(old_text, encoding="utf-8")
                run(
                    "bash",
                    "-c",
                    command,
                    "yunpin-postinstall-test",
                    str(postinstall),
                    str(current),
                    str(user_overlay),
                    owner,
                )
                self.assertEqual(current.read_bytes(), user_overlay.read_bytes())
                backups = list(
                    root.glob("rime_ice.custom.yaml.pre-conservative-*")
                )
                self.assertEqual(1, len(backups))
                self.assertEqual(old_text.encode("utf-8"), backups[0].read_bytes())

    def test_postinstall_upgrades_the_known_default_overlay_for_saved_option(self) -> None:
        postinstall = MACOS_DIR / "package" / "postinstall"
        current = ROOT / "platform" / "rime" / "squirrel" / "default.custom.yaml"
        previous_bytes = (
            "# SPDX-License-Identifier: GPL-3.0-only\n"
            "# YunPin macOS development-preview defaults. Personal data is never bundled.\n\n"
            "patch:\n"
            '  "schema_list":\n'
            "    - schema: rime_ice\n"
            '  "menu/page_size": 8\n'
            '  "menu/alternative_select_keys": "12345678"\n'
        ).encode("utf-8")
        self.assertEqual(
            "23039527ee16342493e346cdc80ebf50f6729cc562b6c85436f7f60651e97bfa",
            hashlib.sha256(previous_bytes).hexdigest(),
        )
        self.assertEqual(
            "857057ff6b5e939c534f644ec24652ae487c85927591e44135e380f61a25fd1b",
            hashlib.sha256(current.read_bytes()).hexdigest(),
        )
        owner = run("id", "-un").stdout.strip()
        command = (
            'source "$1"; '
            'yunpin_migrate_known_default_overlay "$2" "$3" "$4"'
        )

        with tempfile.TemporaryDirectory(prefix="yunpin-postinstall-default-") as temporary:
            root = Path(temporary)
            user_overlay = root / "default.custom.yaml"
            user_overlay.write_bytes(previous_bytes)
            run(
                "bash",
                "-c",
                command,
                "yunpin-postinstall-test",
                str(postinstall),
                str(current),
                str(user_overlay),
                owner,
            )
            self.assertEqual(current.read_bytes(), user_overlay.read_bytes())
            backups = list(root.glob("default.custom.yaml.pre-candidate-pinyin-*"))
            self.assertEqual(1, len(backups))
            self.assertEqual(previous_bytes, backups[0].read_bytes())

            # Once upgraded, a repeated installer run must not create another backup.
            run(
                "bash",
                "-c",
                command,
                "yunpin-postinstall-test",
                str(postinstall),
                str(current),
                str(user_overlay),
                owner,
            )
            self.assertEqual(
                backups,
                list(root.glob("default.custom.yaml.pre-candidate-pinyin-*")),
            )

    def test_postinstall_preserves_custom_missing_and_linked_overlays(self) -> None:
        postinstall = MACOS_DIR / "package" / "postinstall"
        conservative = ROOT / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml"
        owner = run("id", "-un").stdout.strip()
        command = (
            'source "$1"; '
            'yunpin_migrate_known_correction_overlay "$2" "$3" "$4"'
        )
        source = postinstall.read_text(encoding="utf-8")
        self.assertIn(
            'if [[ ! -e "$user_rime/$overlay" && ! -L "$user_rime/$overlay" ]]; then',
            source,
        )

        with tempfile.TemporaryDirectory(prefix="yunpin-postinstall-preserve-") as temporary:
            root = Path(temporary)
            custom = root / "custom.yaml"
            custom.write_text("user-owned: true\n", encoding="utf-8")
            before = custom.read_bytes()
            missing = root / "missing.yaml"
            linked = root / "linked.yaml"
            linked.symlink_to(custom)
            dangling_target = root / "must-not-be-created.yaml"
            dangling = root / "dangling.yaml"
            dangling.symlink_to(dangling_target)

            for target in (custom, missing, linked, dangling):
                run(
                    "bash",
                    "-c",
                    command,
                    "yunpin-postinstall-test",
                    str(postinstall),
                    str(conservative),
                    str(target),
                    owner,
                )

            self.assertEqual(before, custom.read_bytes())
            self.assertFalse(missing.exists())
            self.assertTrue(linked.is_symlink())
            self.assertTrue(dangling.is_symlink())
            self.assertEqual(str(dangling_target), os.readlink(dangling))
            self.assertFalse(dangling_target.exists())
            self.assertEqual([], list(root.glob("*.pre-conservative-*")))
            self.assertEqual([], list(root.glob("*.migration.*")))

    def test_postinstall_fails_closed_for_an_unreviewed_replacement(self) -> None:
        postinstall = MACOS_DIR / "package" / "postinstall"
        legacy = MACOS_DIR / "tests" / "fixtures" / "legacy_correction_rime_ice.custom.yaml"

        with tempfile.TemporaryDirectory(prefix="yunpin-postinstall-fail-closed-") as temporary:
            root = Path(temporary)
            user_overlay = root / "rime_ice.custom.yaml"
            user_overlay.write_bytes(legacy.read_bytes())
            unexpected = root / "unexpected.yaml"
            unexpected.write_text("patch: {}\n", encoding="utf-8")
            owner = run("id", "-un").stdout.strip()
            failed = subprocess.run(
                [
                    "bash",
                    "-c",
                    'source "$1"; yunpin_migrate_known_correction_overlay "$2" "$3" "$4"',
                    "yunpin-postinstall-test",
                    str(postinstall),
                    str(unexpected),
                    str(user_overlay),
                    owner,
                ],
                cwd=ROOT,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertNotEqual(0, failed.returncode)
            self.assertIn("hash mismatch", failed.stderr)
            self.assertEqual(legacy.read_bytes(), user_overlay.read_bytes())
            self.assertEqual([], list(root.glob("*.pre-conservative-*")))
            self.assertEqual([], list(root.glob("*.migration.*")))

    def test_shell_scripts_parse(self) -> None:
        scripts = sorted((MACOS_DIR / "scripts").glob("*.sh"))
        scripts.append(MACOS_DIR / "package" / "postinstall")
        for script in scripts:
            with self.subTest(script=script.name):
                run("bash", "-n", str(script))

    @unittest.skipUnless(shutil.which("swift") and os.uname().sysname == "Darwin", "requires macOS Swift")
    def test_input_source_icon_renders_as_pdf(self) -> None:
        with tempfile.TemporaryDirectory(prefix="yunpin-icon-") as temporary:
            output = Path(temporary) / "yunpin.pdf"
            run("xcrun", "swift", str(MACOS_DIR / "scripts" / "render-input-icon.swift"), str(output))
            self.assertTrue(output.read_bytes().startswith(b"%PDF"))


if __name__ == "__main__":
    unittest.main()
