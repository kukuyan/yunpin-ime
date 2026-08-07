# SPDX-License-Identifier: Apache-2.0

import json
import subprocess
import tempfile
import unittest
from pathlib import Path

from yunpin_public_pack.builder import (
    BuildError,
    PublicPackBuilder,
    load_source_specs,
    validate_output_directory,
    write_outputs,
)


class SyntheticSources:
    def __init__(self, root: Path):
        self.root = root
        self.roots = {
            "rime-ice": root / "rime-ice",
            "rime-essay": root / "rime-essay",
            "THUOCL": root / "THUOCL",
            "phrase-pinyin-data": root / "phrase-pinyin-data",
        }
        files = {
            "rime-ice": {
                "cn_dicts/base.dict.yaml": (
                    "---\nname: synthetic\nversion: '1'\n...\n"
                    "共同词\tgong tong ci\t5\n"
                    "星\txing\t20\n"
                    "河\the\t10\n"
                )
            },
            "rime-essay": {"essay.txt": "共同词\t99999999\n星河\t50\n"},
            "THUOCL": {"data/THUOCL_synthetic.txt": "共同词 99999999\n专业术语 500\n星河 100\n"},
            "phrase-pinyin-data": {
                "pinyin.txt": "专业术语: zhuān yè shù yǔ\n共同词: gòng tóng cí\n"
            },
        }
        self.commits = {}
        for name, checkout in self.roots.items():
            checkout.mkdir(parents=True)
            for relative, content in files[name].items():
                path = checkout / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")
            self.commits[name] = self._commit(checkout)

        self.lock = root / "upstreams.lock.json"
        self.write_lock()

    @staticmethod
    def _run(arguments, cwd: Path) -> str:
        completed = subprocess.run(
            arguments,
            cwd=str(cwd),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
        )
        return completed.stdout.decode("utf-8").strip()

    def _commit(self, checkout: Path) -> str:
        self._run(["git", "init", "-q"], checkout)
        self._run(["git", "add", "."], checkout)
        self._run(
            [
                "git",
                "-c",
                "user.name=YunPin Tests",
                "-c",
                "user.email=tests@example.invalid",
                "commit",
                "-q",
                "-m",
                "synthetic public source",
            ],
            checkout,
        )
        return self._run(["git", "rev-parse", "HEAD"], checkout)

    def write_lock(self, overrides=None) -> None:
        commits = {**self.commits, **(overrides or {})}
        licenses = {
            "rime-ice": "GPL-3.0",
            "rime-essay": "LGPL-3.0",
            "THUOCL": "MIT",
            "phrase-pinyin-data": "MIT",
        }
        document = {
            "format": 1,
            "upstreams": [
                {
                    "name": name,
                    "url": f"https://example.invalid/{name}.git",
                    "commit": commits[name],
                    "license": licenses[name],
                }
                for name in ("rime-ice", "rime-essay", "THUOCL", "phrase-pinyin-data")
            ],
        }
        self.lock.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")


class BuilderTests(unittest.TestCase):
    def test_priority_weights_reading_resolution_and_determinism(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = SyntheticSources(Path(directory))
            specs = load_source_specs(fixture.lock, fixture.roots)
            first_dictionary, first_manifest, manifest = PublicPackBuilder(specs).build()
            second_dictionary, second_manifest, _ = PublicPackBuilder(specs).build()

            self.assertEqual(first_dictionary, second_dictionary)
            self.assertEqual(first_manifest, second_manifest)

            rows = {}
            for line in first_dictionary.decode("utf-8").splitlines():
                fields = line.split("\t")
                if len(fields) == 3:
                    rows[(fields[0], fields[1])] = int(fields[2])

            self.assertEqual(300_000_005, rows[("共同词", "gong tong ci")])
            self.assertEqual(200_000_500, rows[("专业术语", "zhuan ye shu yu")])
            self.assertEqual(200_000_100, rows[("星河", "xing he")])
            self.assertEqual(1, sum(phrase == "共同词" for phrase, _ in rows))
            self.assertEqual(["rime-ice", "THUOCL", "rime-essay"], manifest["policy"]["priority"])
            self.assertEqual("GPL-3.0", manifest["license"]["combined_output"])
            self.assertEqual(3, manifest["policy"]["duplicates_merged"])

            output = Path(directory) / "generated"
            dictionary_path, manifest_path = write_outputs(output, first_dictionary, first_manifest)
            self.assertEqual(first_dictionary, dictionary_path.read_bytes())
            self.assertEqual(first_manifest, manifest_path.read_bytes())

    def test_commit_mismatch_is_rejected_before_reading_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = SyntheticSources(Path(directory))
            fixture.write_lock({"rime-essay": "0" * 40})
            with self.assertRaisesRegex(BuildError, "HEAD mismatch"):
                load_source_specs(fixture.lock, fixture.roots)

    def test_dirty_checkout_is_rejected_even_when_head_matches(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = SyntheticSources(Path(directory))
            source = fixture.roots["rime-ice"] / "cn_dicts" / "base.dict.yaml"
            source.write_text(source.read_text(encoding="utf-8") + "新增词\txin zeng ci\t1\n", encoding="utf-8")
            with self.assertRaisesRegex(BuildError, "dirty"):
                load_source_specs(fixture.lock, fixture.roots)

    def test_repository_output_requires_explicit_top_level_build_dir(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repository"
            root.mkdir()
            subprocess.run(["git", "init", "-q"], cwd=str(root), check=True)

            with self.assertRaisesRegex(BuildError, "outside every Git"):
                validate_output_directory(root / "build" / "public", "output", [])
            self.assertEqual(
                (root / "build" / "public").resolve(),
                validate_output_directory(root / "build" / "public", "build", []),
            )
            with self.assertRaisesRegex(BuildError, "top-level build"):
                validate_output_directory(root / "artifacts" / "public", "build", [])


if __name__ == "__main__":
    unittest.main()
