#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Assert the Windows subsystem a PE image was linked for.

Go links console-subsystem binaries by default. A scheduled task that starts a
long-running console binary in the user's interactive session gets a console
window allocated for it, and because the resident runs for the life of the
session that window stays on screen. The fix is to link the resident with
-H=windowsgui while the interactive binary -- which prints JSON to stdout --
stays console-subsystem.

Neither property is visible in the source, so CI checks the linked image.

Usage:
  check_pe_subsystem.py gui <image> [<image> ...]
  check_pe_subsystem.py console <image> [<image> ...]
"""

from __future__ import annotations

from pathlib import Path
import struct
import sys


IMAGE_SUBSYSTEM_WINDOWS_GUI = 2
IMAGE_SUBSYSTEM_WINDOWS_CUI = 3

EXPECTED = {
    "gui": IMAGE_SUBSYSTEM_WINDOWS_GUI,
    "console": IMAGE_SUBSYSTEM_WINDOWS_CUI,
}

NAMES = {
    IMAGE_SUBSYSTEM_WINDOWS_GUI: "WINDOWS_GUI",
    IMAGE_SUBSYSTEM_WINDOWS_CUI: "WINDOWS_CUI",
}


def read_subsystem(path: Path) -> int:
    data = path.read_bytes()
    if data[:2] != b"MZ":
        raise SystemExit(f"{path}: not a PE image (missing MZ signature)")
    # The DOS header stores the PE header offset at 0x3C.
    (pe_offset,) = struct.unpack_from("<I", data, 0x3C)
    if data[pe_offset : pe_offset + 4] != b"PE\0\0":
        raise SystemExit(f"{path}: not a PE image (missing PE signature)")
    # PE signature (4) + COFF file header (20) puts the optional header next;
    # Subsystem sits at offset 68 in both PE32 and PE32+ optional headers.
    optional_header = pe_offset + 4 + 20
    (subsystem,) = struct.unpack_from("<H", data, optional_header + 68)
    return subsystem


def main(argv: list[str]) -> int:
    if len(argv) < 3 or argv[1] not in EXPECTED:
        raise SystemExit(__doc__)
    expected = EXPECTED[argv[1]]
    failures = 0
    for name in argv[2:]:
        path = Path(name)
        if not path.is_file():
            print(f"missing image: {path}", file=sys.stderr)
            failures += 1
            continue
        subsystem = read_subsystem(path)
        label = NAMES.get(subsystem, str(subsystem))
        if subsystem != expected:
            print(
                f"{path}: linked for {label}, expected {NAMES[expected]}",
                file=sys.stderr,
            )
            failures += 1
        else:
            print(f"{path}: {label}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
