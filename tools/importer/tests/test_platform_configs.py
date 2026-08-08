# SPDX-License-Identifier: Apache-2.0

import json
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

    def test_both_desktop_overlays_are_horizontal_numbered_and_dark_aware(self):
        common = (self.repository / "platform" / "rime" / "common" / "default.custom.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn('"menu/page_size": 8', common)
        self.assertIn('"menu/alternative_select_keys": "12345678"', common)

        weasel = (self.repository / "platform" / "rime" / "weasel" / "weasel.custom.yaml").read_text(
            encoding="utf-8"
        )
        squirrel = (self.repository / "platform" / "rime" / "squirrel" / "squirrel.custom.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn('"style/horizontal": true', weasel)
        self.assertIn('"style/candidate_list_layout": linear', squirrel)
        self.assertIn('"menu/page_size": 8', squirrel)
        self.assertIn('"menu/alternative_select_keys": "12345678"', squirrel)
        rime_ice_windows = (self.repository / "platform" / "windows" / "rime" / "rime_ice.custom.yaml").read_text(
            encoding="utf-8"
        )
        rime_ice_squirrel = (self.repository / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml").read_text(
            encoding="utf-8"
        )
        for overlay in (rime_ice_windows, rime_ice_squirrel):
            self.assertIn('"menu/page_size": 8', overlay)
            self.assertIn('"menu/alternative_select_keys": "12345678"', overlay)
            # The expression actions consume two candidate slots and reach the
            # network, so no shipped overlay may enable them.
            self.assertIn('"yunpin/expression_search": false', overlay)
            self.assertNotIn('"yunpin/expression_search": true', overlay)
        for overlay in (weasel, squirrel):
            self.assertIn('"style/color_scheme_dark": yunpin_dark', overlay)
            self.assertNotIn("preset_color_schemes/sogou", overlay.lower())


if __name__ == "__main__":
    unittest.main()
