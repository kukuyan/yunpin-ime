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
        self.assertEqual(6, len(patches))
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
            self.assertIn("set<pair<SyllableId, size_t>> exact_matches", patch_text)
            self.assertIn("exact_matches.find({m.value, m.length})", patch_text)
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
        self.assertIn("encrypted sync is not connected", sources)

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
        self.assertTrue(
            manifest["yunpin_typo_correction_exact_prefix_collision_e2e"]
        )
        self.assertEqual(16, manifest["yunpin_typo_correction_max_edges_per_offset"])
        self.assertFalse(manifest["yunpin_typo_correction_native_host_e2e"])
        self.assertFalse(manifest["yunpin_typo_correction_production_dictionary_e2e"])
        self.assertTrue(manifest["yunpin_session_correction_librime_e2e"])
        self.assertFalse(manifest["yunpin_learning_bridge"])
        self.assertFalse(manifest["yunpin_local_model_sidecar"])
        self.assertFalse(manifest["mixed_chinese_english_input"])
        self.assertFalse(manifest["encrypted_cloud_sync"])
        self.assertFalse(manifest["production_signed"])

    def test_rime_overlay_enables_the_bounded_private_filter(self) -> None:
        overlay = (ROOT / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn("engine/filters/@before 0\": yunpin_filter@yunpin", overlay)
        self.assertIn("yunpin/snapshot\": yunpin/private.tsv", overlay)
        self.assertIn("yunpin/max_candidates\": 2", overlay)
        self.assertIn("yunpin/short_input_guard\": true", overlay)
        self.assertIn("yunpin/session_learning\": true", overlay)
        self.assertIn("translator/enable_correction\": true", overlay)
        self.assertIn(
            "translator/corrector_component\": yunpin_corrector", overlay
        )
        self.assertIn("yunpin/typo_correction\": true", overlay)

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

    def test_generated_bundle_is_signed_bottom_up_and_fails_closed(self) -> None:
        build = (MACOS_DIR / "scripts" / "build-preview.sh").read_text(encoding="utf-8")
        # Xcode 26 still schedules RegisterWithLaunchServices for a macOS app,
        # even when an unsupported similarly named command-line setting is used.
        # The build must therefore remove and verify the exact staging path.
        self.assertNotIn("REGISTER_WITH_LAUNCH_SERVICES", build)
        self.assertNotIn("WRAPPER_EXTENSION", build)
        self.assertIn('"$lsregister" -u "$app"', build)
        self.assertIn('"$lsregister" -dump', build)
        self.assertIn('registration_check_status=("${PIPESTATUS[@]}")', build)
        self.assertIn("unable to inspect LaunchServices", build)
        self.assertIn("the exact YunPin build bundle remains registered", build)
        self.assertNotIn("lsregister -f", build)
        verify_app = build.index('scripts/verify-app.sh" --require-universal "$app"')
        unregister = build.index('"$lsregister" -u "$app"')
        assert_absent = build.index('"$lsregister" -dump')
        self.assertLess(verify_app, unregister)
        self.assertLess(unregister, assert_absent)
        self.assertIn('scripts/sign-app-adhoc.sh" "$app"', build)
        self.assertLess(
            build.index('scripts/verify-app.sh" --require-universal "$app"'),
            build.index('"$lsregister" -u "$app"'),
        )
        self.assertNotIn("warning: bundle signing failed", build)

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

    def test_corresponding_source_exports_only_committed_project_files(self) -> None:
        archive = (MACOS_DIR / "scripts" / "make-source-archive.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('git -C "$REPO_ROOT" archive HEAD --', archive)
        self.assertNotIn('cp -R "${REPO_ROOT}/$path"', archive)
        self.assertIn("require_clean_repository", archive)
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
