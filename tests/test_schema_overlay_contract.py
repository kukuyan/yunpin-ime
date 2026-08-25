#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Bind the shipped Rime overlays to the options the filter actually reads.

The peer review found that YunPinFilter required a host capability option that
no shipped Squirrel/Weasel patch ever set, so the whole filter was inert in the
configuration users actually received while its unit tests -- which set the
option unconditionally -- stayed green. Nothing detected the gap because no test
compared the shipped overlay against the code that consumes it.

These cases close that loop in both directions:

  * every "yunpin/<option>" the production sources read must be set by every
    shipped overlay, so a newly consumed option cannot silently default;
  * every "yunpin/<option>" a shipped overlay sets must be read by some
    production source, so a renamed or removed option cannot leave a dead key
    that looks like it still configures something.

Reserved options that are deliberately declared before their consumer exists are
listed explicitly, with the reason, rather than being skipped by a wildcard.
"""

from __future__ import annotations

from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]

# Overlays shipped to users. Both platforms must configure the same option set;
# only the values differ.
OVERLAYS = (
    ROOT / "platform" / "rime" / "squirrel" / "rime_ice.custom.yaml",
    ROOT / "platform" / "windows" / "rime" / "rime_ice.custom.yaml",
)

# Production sources that consume the namespace. Tests are deliberately not
# scanned: a key that only a test reads is exactly the drift being guarded
# against.
SOURCE_DIR = ROOT / "librime-yunpin" / "src"

# Declared ahead of its consumer, with the reason it cannot simply be dropped.
RESERVED_OPTIONS = {
    # platform/patches/*/0005-yunpin-expression-search-and-favorite.patch keeps
    # expression actions disconnected because candidate commit text is untrusted
    # dictionary data. The option stays declared and false so a stale user
    # override cannot turn it on before a typed action channel exists.
    "expression_search",
}

OVERLAY_OPTION = re.compile(r'"yunpin/([a-z_]+)"')

# The filter binds its options through the schema namespace it was ticketed
# with. Those have no meaningful in-code default -- an unset option silently
# changes shipped behaviour -- so every overlay must declare all of them.
NAMESPACED_READ = re.compile(r'name_space_ \+ "/([a-z_]+)"')

# The corrector reads a few options by literal name through ConfigBool(key,
# default), which supplies an explicit default. Those are optional sub-tuning
# knobs: an overlay may leave them out. They still must not be *set* to
# something nothing reads, which the reverse direction covers.
DIRECT_READ = re.compile(r'ConfigBool\(ticket, "yunpin/([a-z_]+)"')


def overlay_options(path: Path) -> set[str]:
    return set(OVERLAY_OPTION.findall(path.read_text(encoding="utf-8")))


def _scan(pattern: re.Pattern[str]) -> set[str]:
    options: set[str] = set()
    for source in sorted(SOURCE_DIR.rglob("*.cpp")):
        options.update(pattern.findall(source.read_text(encoding="utf-8")))
    return options


def required_options() -> set[str]:
    return _scan(NAMESPACED_READ)


def consumed_options() -> set[str]:
    return _scan(NAMESPACED_READ) | _scan(DIRECT_READ)


class SchemaOverlayContractTests(unittest.TestCase):
    def test_overlays_exist_and_declare_options(self) -> None:
        for overlay in OVERLAYS:
            self.assertTrue(overlay.is_file(), f"missing shipped overlay: {overlay}")
            self.assertTrue(
                overlay_options(overlay),
                f"shipped overlay declares no yunpin options: {overlay}",
            )

    def test_every_required_option_is_set_by_every_overlay(self) -> None:
        required = required_options()
        self.assertIn("enabled", required, "sanity: the scan found no known option")
        self.assertIn("short_input_guard", required)
        for overlay in OVERLAYS:
            missing = sorted(required - overlay_options(overlay))
            self.assertEqual(
                missing,
                [],
                f"{overlay.relative_to(ROOT)} does not set namespace-bound options "
                f"the filter reads: {missing}",
            )

    def test_every_overlay_option_is_read_by_production_code(self) -> None:
        consumed = consumed_options()
        for overlay in OVERLAYS:
            declared = overlay_options(overlay)
            unread = sorted(declared - consumed - RESERVED_OPTIONS)
            self.assertEqual(
                unread,
                [],
                f"{overlay.relative_to(ROOT)} sets options no production source reads: {unread}",
            )

    def test_both_overlays_declare_the_same_option_set(self) -> None:
        squirrel, weasel = (overlay_options(overlay) for overlay in OVERLAYS)
        self.assertEqual(
            sorted(squirrel ^ weasel),
            [],
            "the macOS and Windows overlays configure different option sets; "
            "values may differ per platform but the option set must not",
        )

    def test_host_capability_option_has_a_setter(self) -> None:
        """The option that made the filter inert must be set by a host patch.

        The filter only reads yunpin_learning_allowed; the value has to come from
        a Squirrel/Weasel patch. If the patch series ever loses that setter the
        filter silently stops injecting personal candidates again, which is
        precisely the regression this suite exists to catch.
        """
        setters = [
            patch
            for patch in sorted((ROOT / "platform" / "patches").rglob("*.patch"))
            if "yunpin_learning_allowed" in patch.read_text(encoding="utf-8", errors="replace")
        ]
        self.assertTrue(
            setters,
            "no Squirrel/Weasel patch sets yunpin_learning_allowed; personal "
            "candidates and session learning would be inert in shipped builds",
        )


if __name__ == "__main__":
    unittest.main()
