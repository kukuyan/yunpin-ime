# SPDX-License-Identifier: Apache-2.0

import json
import tempfile
import unittest
from pathlib import Path

from yunpin_importer.parsers import PinyinResolver, load_pinyin_resolver, parse_dictionary, parse_history


class ParserTests(unittest.TestCase):
    def test_rime_rows_are_merged(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "synthetic.dict.yaml"
            source.write_text(
                "---\nname: synthetic\nversion: '1'\n...\n"
                "星河中心\txing he zhong xin\t3\n"
                "星河中心\txing he zhong xin\t4\n",
                encoding="utf-8",
            )
            result = parse_dictionary([source], "rime")
        self.assertEqual(1, len(result.entries))
        self.assertEqual(7, result.entries[0].use_count)
        self.assertEqual(1, result.duplicate_rows)

    def test_chatgpt_reads_only_user_terms_and_buckets_count(self):
        user_phrase = "星河数据中心"
        assistant_phrase = "云海应用中心"
        document = [
            {
                "mapping": {
                    "one": {
                        "message": {
                            "author": {"role": "user"},
                            "content": {"parts": [user_phrase + "\n" + user_phrase + "\n" + user_phrase]},
                        }
                    },
                    "two": {
                        "message": {
                            "author": {"role": "assistant"},
                            "content": {"parts": [assistant_phrase + "\n" + assistant_phrase]},
                        }
                    },
                }
            }
        ]
        resolver = PinyinResolver()
        resolver.add(user_phrase, "xing he shu ju zhong xin")
        with tempfile.TemporaryDirectory() as directory:
            export = Path(directory) / "conversations.json"
            export.write_text(json.dumps(document, ensure_ascii=False), encoding="utf-8")
            result = parse_history([export], "chatgpt", resolver)
        self.assertEqual([user_phrase], [entry.phrase for entry in result.entries])
        self.assertEqual(4, result.entries[0].use_count)
        self.assertEqual("xing he shu ju zhong xin", result.entries[0].pinyin)

    def test_phrase_pinyin_data_format_is_supported_offline(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "pinyin.txt"
            source.write_text("星河中心: xīng hé zhōng xīn\n", encoding="utf-8")
            resolver = load_pinyin_resolver([source])
        self.assertEqual("xing he zhong xin", resolver.resolve("星河中心"))

    def test_codex_markdown_filters_sensitive_line(self):
        kept = "北辰研究院"
        sensitive = "不应出现短语"
        with tempfile.TemporaryDirectory() as directory:
            summary = Path(directory) / "summary.md"
            summary.write_text(
                f"# 摘要\n{kept}\n{kept}\n{sensitive} "
                + "https://"
                + "example.test/private\n",
                encoding="utf-8",
            )
            result = parse_history([summary], "codex", PinyinResolver())
        self.assertEqual([kept], [entry.phrase for entry in result.entries])
        self.assertNotIn(sensitive, [entry.phrase for entry in result.entries])

    def test_history_rejects_repeated_short_sentences_but_keeps_exact_term(self):
        raw_one = "今天天晴"
        raw_two = "天气真好"
        term = "星河协议"
        resolver = PinyinResolver()
        resolver.add(raw_one, "jin tian tian qing")
        resolver.add(raw_two, "tian qi zhen hao")
        resolver.add(term, "xing he xie yi")
        with tempfile.TemporaryDirectory() as directory:
            summary = Path(directory) / "summary.md"
            summary.write_text(
                f"{raw_one}\n{raw_one}\n{raw_two}\n{raw_two}\n{term}\n{term}\n",
                encoding="utf-8",
            )
            result = parse_history([summary], "codex", resolver)
        self.assertEqual([term], [entry.phrase for entry in result.entries])
        self.assertEqual(4, result.rejected["sentence_like"])


if __name__ == "__main__":
    unittest.main()
