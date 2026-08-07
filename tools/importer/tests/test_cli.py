# SPDX-License-Identifier: Apache-2.0

import contextlib
import io
import tempfile
import unittest
from pathlib import Path

from yunpin_importer.cli import main


class CliTests(unittest.TestCase):
    def test_preview_does_not_write(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source.txt"
            output = root / "result.tsv"
            source.write_text("星河中心\txing he zhong xin\t2\n", encoding="utf-8")
            stdout = io.StringIO()
            stderr = io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                code = main(["import", str(source), "--kind", "text", "--output", str(output)])
            self.assertEqual(0, code)
            self.assertFalse(output.exists())
            self.assertIn('"preview_is_masked": true', stdout.getvalue())

    def test_confirm_writes_only_four_columns(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source.txt"
            output = root / "result.tsv"
            source.write_text("星河中心\txing he zhong xin\t2\n", encoding="utf-8")
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                code = main(
                    [
                        "import",
                        str(source),
                        "--kind",
                        "text",
                        "--confirm",
                        "IMPORT",
                        "--output",
                        str(output),
                    ]
                )
            self.assertEqual(0, code)
            rows = output.read_text(encoding="utf-8").splitlines()
            self.assertEqual("phrase\tpinyin\tsource\tuse_count", rows[0])
            self.assertEqual(4, len(rows[1].split("\t")))

    def test_repository_output_is_refused(self):
        repository = Path(__file__).resolve().parents[3]
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "source.txt"
            source.write_text("星河中心\txing he zhong xin\t2\n", encoding="utf-8")
            target = repository / "must-not-create-personal.tsv"
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                code = main(
                    [
                        "import",
                        str(source),
                        "--kind",
                        "text",
                        "--confirm",
                        "IMPORT",
                        "--output",
                        str(target),
                    ]
                )
            self.assertEqual(2, code)
            self.assertFalse(target.exists())

    def test_existing_private_output_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source.txt"
            output = root / "result.tsv"
            source.write_text("星河中心\txing he zhong xin\t2\n", encoding="utf-8")
            output.write_text("existing private data\n", encoding="utf-8")
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                code = main(
                    [
                        "import",
                        str(source),
                        "--kind",
                        "text",
                        "--confirm",
                        "IMPORT",
                        "--output",
                        str(output),
                    ]
                )
            self.assertEqual(2, code)
            self.assertEqual("existing private data\n", output.read_text(encoding="utf-8"))

    def test_require_pinyin_discards_unresolved_history_entry(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "summary.md"
            source.write_text("星河中心\n星河中心\n", encoding="utf-8")
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(io.StringIO()):
                code = main(
                    [
                        "import",
                        str(source),
                        "--kind",
                        "codex",
                        "--require-pinyin",
                    ]
                )
            self.assertEqual(0, code)
            preview = stdout.getvalue()
            self.assertIn('"candidate_count": 0', preview)
            self.assertIn('"missing_pinyin_required": 1', preview)

    def test_require_pinyin_retains_entry_resolved_by_local_dictionary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "summary.md"
            pinyin = root / "pinyin.txt"
            source.write_text("星河中心\n星河中心\n", encoding="utf-8")
            pinyin.write_text("星河中心: xīng hé zhōng xīn\n", encoding="utf-8")
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(io.StringIO()):
                code = main(
                    [
                        "import",
                        str(source),
                        "--kind",
                        "codex",
                        "--pinyin-dict",
                        str(pinyin),
                        "--require-pinyin",
                    ]
                )
            self.assertEqual(0, code)
            preview = stdout.getvalue()
            self.assertIn('"candidate_count": 1', preview)
            self.assertIn('"missing_pinyin": 0', preview)
            self.assertIn('"missing_pinyin_required": 0', preview)


if __name__ == "__main__":
    unittest.main()
