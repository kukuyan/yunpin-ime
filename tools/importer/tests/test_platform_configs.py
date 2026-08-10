# SPDX-License-Identifier: Apache-2.0

import json
import re
import unittest
from pathlib import Path


class PlatformConfigTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.repository = Path(__file__).resolve().parents[3]

    def test_upstreams_are_commit_pinned(self):
        lock = json.loads((self.repository / "platform" / "upstream-lock.json").read_text(encoding="utf-8"))
        for component in lock["components"]:
            with self.subTest(component=component["name"]):
                self.assertRegex(component["commit"], r"^[0-9a-f]{40}$")
                self.assertTrue(component["tag"])
                self.assertTrue(component["license"])

    def test_rime_stub_headers_track_the_locked_librime(self):
        # librime-yunpin/tests/rime_stubs re-declares part of the librime API so
        # the filter can be tested without librime. A librime bump silently
        # invalidates those signatures, so every stub records the commit it was
        # written against and this pins it to the lock.
        lock = json.loads((self.repository / "platform" / "upstream-lock.json").read_text(encoding="utf-8"))
        librime = next(item for item in lock["components"] if item["name"] == "librime")
        stubs = sorted((self.repository / "librime-yunpin" / "tests" / "rime_stubs").rglob("*.h"))
        self.assertGreaterEqual(len(stubs), 10)
        for stub in stubs:
            with self.subTest(stub=stub.name):
                self.assertIn(librime["commit"], stub.read_text(encoding="utf-8"))

    def overlay_paths(self):
        roots = (
            self.repository / "platform" / "rime",
            self.repository / "platform" / "windows" / "rime",
        )
        found = []
        for root in roots:
            found.extend(sorted(root.rglob("*.custom.yaml")))
        return found

    def test_every_shipped_overlay_pins_the_eight_key_layout(self):
        overlays = self.overlay_paths()
        # A new overlay that nobody asserts on is how "my machine still shows
        # five candidates" comes back, so the count is pinned deliberately.
        self.assertEqual(
            [
                "squirrel/default.custom.yaml",
                "squirrel/rime_ice.custom.yaml",
                "squirrel/squirrel.custom.yaml",
                "weasel/default.custom.yaml",
                "weasel/weasel.custom.yaml",
                "rime/rime_ice.custom.yaml",
            ],
            [f"{path.parent.name}/{path.name}" for path in overlays],
        )
        for path in overlays:
            text = path.read_text(encoding="utf-8")
            with self.subTest(overlay=f"{path.parent.name}/{path.name}"):
                sizes = re.findall(r'"menu/page_size":\s*(\d+)', text)
                self.assertTrue(sizes, "overlay must pin the candidate page size")
                self.assertEqual({"8"}, set(sizes))
                self.assertIn('"menu/alternative_select_keys": "12345678"', text)

    def test_the_two_default_overlays_agree(self):
        # macOS installs squirrel/default.custom.yaml and Windows installs
        # weasel/default.custom.yaml. They are separate files that must not
        # drift apart on the menu settings.
        def menu_lines(path):
            text = (self.repository / "platform" / "rime" / path).read_text(encoding="utf-8")
            return sorted(re.findall(r'^\s*"menu/[^"]+":.*$', text, flags=re.MULTILINE))

        self.assertEqual(
            menu_lines("squirrel/default.custom.yaml"),
            menu_lines("weasel/default.custom.yaml"),
        )

    def test_desktop_themes_are_horizontal_and_dark_aware(self):
        weasel = (self.repository / "platform" / "rime" / "weasel" / "weasel.custom.yaml").read_text(
            encoding="utf-8"
        )
        squirrel = (self.repository / "platform" / "rime" / "squirrel" / "squirrel.custom.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn('"style/horizontal": true', weasel)
        self.assertIn('"style/candidate_list_layout": linear', squirrel)
        for overlay in (weasel, squirrel):
            self.assertIn('"style/color_scheme_dark": yunpin_dark', overlay)
            self.assertNotIn("preset_color_schemes/sogou", overlay.lower())

    def test_no_shipped_overlay_enables_the_expression_actions(self):
        for name in (
            self.repository / "platform" / "windows" / "rime" / "rime_ice.custom.yaml",
            self.repository / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml",
        ):
            overlay = name.read_text(encoding="utf-8")
            with self.subTest(overlay=name.name):
                # The expression actions consume two candidate slots and reach
                # the network, so no shipped overlay may enable them.
                self.assertIn('"yunpin/expression_search": false', overlay)
                self.assertNotIn('"yunpin/expression_search": true', overlay)

    def test_desktop_overlays_keep_native_typo_correction_experimental(self):
        for path in (
            self.repository / "platform" / "windows" / "rime" / "rime_ice.custom.yaml",
            self.repository / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml",
        ):
            overlay = path.read_text(encoding="utf-8")
            with self.subTest(overlay=path):
                self.assertIn('"translator/enable_correction": false', overlay)
                self.assertIn(
                    '"translator/corrector_component": yunpin_corrector', overlay
                )
                self.assertIn('"yunpin/typo_correction": false', overlay)
                self.assertIn('"yunpin/typo_reviewed_confusions": false', overlay)


if __name__ == "__main__":
    unittest.main()
