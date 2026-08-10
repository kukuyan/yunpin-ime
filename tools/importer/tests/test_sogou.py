# SPDX-License-Identifier: Apache-2.0

import hashlib
import contextlib
import io
import os
import sys
import tempfile
import unittest
from pathlib import Path

from yunpin_importer.sogou import SogouConversionError, convert_with_pinned_tool, dispose_artifact, sha256_file
from yunpin_importer.cli import MAX_PRIVATE_SNAPSHOT_ENTRIES, build_parser, main


class SogouBridgeTests(unittest.TestCase):
    def test_default_capacity_matches_complete_private_snapshot(self):
        self.assertEqual(100_000, MAX_PRIVATE_SNAPSHOT_ENTRIES)
        args = build_parser().parse_args(
            [
                "sogou",
                "synthetic.bin",
                "--converter",
                "synthetic-converter",
                "--converter-sha256",
                "0" * 64,
            ]
        )
        self.assertEqual(MAX_PRIVATE_SNAPSHOT_ENTRIES, args.max_sogou_phrases)

    def _fake_converter(self, directory: Path) -> Path:
        # Exercise the supported managed-converter path so the fixture is
        # executable on POSIX and Windows without a platform-specific binary.
        converter = directory / "fake-converter.dll"
        converter.write_text(
            "#!/usr/bin/env python3\n"
            "import pathlib, sys\n"
            "if '--version' in sys.argv:\n"
            "    print('ImeWlConverterCmd 3.4.3')\n"
            "    raise SystemExit(0)\n"
            "destination = pathlib.Path(sys.argv[sys.argv.index('-O') + 1])\n"
            "destination.write_text(\"---\\nname: synthetic\\n...\\n星河中心\\txing he zhong xin\\t3\\n\", encoding='utf-8')\n",
            encoding="utf-8",
        )
        converter.chmod(0o700)
        return converter

    def test_converts_only_copy_and_preserves_source_hash(self):
        with tempfile.TemporaryDirectory() as directory_name:
            directory = Path(directory_name)
            source = directory / "personal.bin"
            source.write_bytes(b"synthetic binary input")
            before = sha256_file(source)
            converter = self._fake_converter(directory)
            artifact = convert_with_pinned_tool(
                source,
                converter,
                sha256_file(converter),
                source_format="sgpybin",
                expected_source_sha256=before,
                dotnet=sys.executable,
            )
            try:
                self.assertTrue(artifact.converted_path.exists())
                self.assertEqual(before, sha256_file(source))
                self.assertEqual(before, artifact.copied_sha256)
            finally:
                dispose_artifact(artifact)

    def test_wrong_converter_hash_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory_name:
            directory = Path(directory_name)
            source = directory / "personal.scel"
            source.write_bytes(b"synthetic binary input")
            converter = self._fake_converter(directory)
            wrong = hashlib.sha256(b"different synthetic file").hexdigest()
            with self.assertRaises(SogouConversionError):
                convert_with_pinned_tool(source, converter, wrong)

    def test_unpinned_converter_version_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory_name:
            directory = Path(directory_name)
            source = directory / "personal.scel"
            source.write_bytes(b"synthetic binary input")
            converter = self._fake_converter(directory)
            converter.write_text(
                converter.read_text(encoding="utf-8").replace("3.4.3", "3.3.0"),
                encoding="utf-8",
            )
            converter.chmod(0o700)
            with self.assertRaises(SogouConversionError):
                convert_with_pinned_tool(
                    source,
                    converter,
                    sha256_file(converter),
                    dotnet=sys.executable,
                )

    def test_cli_caps_sogou_output_by_descending_frequency(self):
        with tempfile.TemporaryDirectory() as directory_name:
            directory = Path(directory_name)
            source = directory / "personal.bin"
            source.write_bytes(b"synthetic binary input")
            converter = self._fake_converter(directory)
            converter.write_text(
                converter.read_text(encoding="utf-8").replace(
                    "星河中心\\txing he zhong xin\\t3\\n",
                    "低频词\\tdi pin ci\\t1\\n"
                    "最高频词\\tzui gao pin ci\\t9\\n"
                    "中频词\\tzhong pin ci\\t4\\n",
                ),
                encoding="utf-8",
            )
            converter.chmod(0o700)
            output = directory / "private.tsv"
            stdout = io.StringIO()
            stderr = io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                code = main(
                    [
                        "sogou",
                        str(source),
                        "--source-format",
                        "sgpybin",
                        "--source-sha256",
                        sha256_file(source),
                        "--converter",
                        str(converter),
                        "--converter-sha256",
                        sha256_file(converter),
                        "--dotnet",
                        sys.executable,
                        "--max-sogou-phrases",
                        "2",
                        "--confirm",
                        "IMPORT",
                        "--output",
                        str(output),
                        "--preview-limit",
                        "0",
                    ]
                )
            self.assertEqual(0, code, stderr.getvalue())
            rows = output.read_text(encoding="utf-8").splitlines()
            self.assertEqual(3, len(rows))
            self.assertIn("最高频词", rows[1])
            self.assertIn("中频词", rows[2])
            self.assertIn('"over_private_snapshot_capacity": 1', stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
