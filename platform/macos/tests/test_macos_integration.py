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
        self.assertEqual(3, len(patches))
        for patch in patches:
            text = patch.read_text(encoding="utf-8")
            self.assertIn("SPDX-License-Identifier: GPL-3.0-only", text)
            self.assertIn(f"Base-Commit: {EXPECTED_COMMIT}", text)
        self.assertEqual(EXPECTED_COMMIT, (self.prepared / ".yunpin-base-commit").read_text().strip())

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
        self.assertNotIn("rime.github.io/release/squirrel", sources)
        self.assertIn("encrypted sync is not connected", sources)

    def test_original_artwork_replaces_upstream_visible_assets(self) -> None:
        upstream_logo = SQUIRREL / "Rime.icon" / "Assets" / "logo.svg"
        prepared_logo = self.prepared / "YunPin.icon" / "Assets" / "logo.svg"
        self.assertNotEqual(
            hashlib.sha256(upstream_logo.read_bytes()).digest(),
            hashlib.sha256(prepared_logo.read_bytes()).digest(),
        )
        ET.parse(prepared_logo)
        project = (self.prepared / "Squirrel.xcodeproj" / "project.pbxproj").read_text(encoding="utf-8")
        self.assertIn("resources/yunpin.pdf", project)
        self.assertNotIn("resources/rime.pdf", project)
        self.assertIn("path = YunPin.icon", project)
        self.assertNotIn("path = Rime.icon", project)

    def test_preview_manifest_separates_merged_module_from_host_evidence(self) -> None:
        manifest = json.loads((MACOS_DIR / "preview-manifest.json").read_text(encoding="utf-8"))
        self.assertEqual("development-preview", manifest["channel"])
        self.assertTrue(manifest["yunpin_module_merged"])
        self.assertTrue(manifest["yunpin_ranking_headless_e2e"])
        self.assertFalse(manifest["yunpin_ranking_native_host_e2e"])
        self.assertFalse(manifest["yunpin_learning_bridge"])
        self.assertFalse(manifest["encrypted_cloud_sync"])
        self.assertFalse(manifest["production_signed"])

    def test_rime_overlay_enables_the_bounded_private_filter(self) -> None:
        overlay = (ROOT / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn("engine/filters/@before 0\": yunpin_filter@yunpin", overlay)
        self.assertIn("yunpin/snapshot\": yunpin/private.tsv", overlay)
        self.assertIn("yunpin/max_candidates\": 2", overlay)

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

    def test_generated_bundle_xattrs_are_cleared_before_adhoc_signing(self) -> None:
        build = (MACOS_DIR / "scripts" / "build-preview.sh").read_text(encoding="utf-8")
        clear_xattrs = build.index('xattr -cr "$app"')
        sign_bundle = build.index('codesign --force --deep --sign - "$app"')
        self.assertLess(clear_xattrs, sign_bundle)
        self.assertIn("for attempt in 1 2 3", build)
        self.assertIn("unable to remove generated bundle metadata", build)

    def test_corresponding_source_staging_stays_with_the_build_root(self) -> None:
        archive = (MACOS_DIR / "scripts" / "make-source-archive.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn('mktemp -d "$build_root/.source-archive.XXXXXX"', archive)
        self.assertNotIn('${TMPDIR:-/tmp}/yunpin-source', archive)

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
