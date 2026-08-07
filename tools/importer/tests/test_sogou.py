# SPDX-License-Identifier: Apache-2.0

import hashlib
import os
import tempfile
import unittest
from pathlib import Path

from yunpin_importer.sogou import SogouConversionError, convert_with_pinned_tool, dispose_artifact, sha256_file


class SogouBridgeTests(unittest.TestCase):
    def _fake_converter(self, directory: Path) -> Path:
        converter = directory / "fake-converter"
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
                convert_with_pinned_tool(source, converter, sha256_file(converter))


if __name__ == "__main__":
    unittest.main()
