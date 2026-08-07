# SPDX-License-Identifier: Apache-2.0

import unittest

from yunpin_importer.filters import extract_history_terms, normalize_pinyin, unsafe_reason


class FilterTests(unittest.TestCase):
    def test_normalizes_tone_marks_and_umlaut(self):
        self.assertEqual("lv se", normalize_pinyin("lǜ sè"))
        self.assertEqual("nv er", normalize_pinyin("nu:3 er2"))

    def test_sensitive_classes_are_rejected(self):
        values = {
            "url": "请看 " + "https://" + "example.test/a",
            "email": "联系 " + "demo" + "@" + "example.test",
            "domain": "访问 " + "private" + ".example.test",
            "ipv4": "地址 " + "203.0." + "113.9",
            "windows_path": "文件 " + "C:" + "\\private\\sample.txt",
            "unix_path": "文件 " + "/Users/example/private.txt",
            "file_path": "文件 " + "/private/example.txt",
            "long_number": "编号 " + "123" + "456789",
            "opaque_token": "标识 " + "abcd" * 7,
            "secret": "api" + "_key=" + "synthetic-value",
        }
        for expected, value in values.items():
            with self.subTest(expected=expected):
                self.assertEqual(expected, unsafe_reason(value))

    def test_history_drops_code_and_long_prose(self):
        text = """
星河数据中心
星河数据中心
我们需要认真讨论所有相关事项并尽快完成后续工作
```text
代码块专用短语
```
"""
        terms = list(extract_history_terms([text]))
        self.assertEqual(["星河数据中心", "星河数据中心"], terms)

    def test_history_does_not_retain_short_instructions_as_terms(self):
        text = "按照你的建议部署\n按照你的建议部署\n请马上处理\n请马上处理\n云拼输入法\n云拼输入法"
        terms = list(extract_history_terms([text]))
        self.assertEqual(["云拼输入法", "云拼输入法"], terms)

    def test_short_raw_sentences_are_rejected_even_if_dictionary_marks_them_known(self):
        raw_sentences = {"今天天晴", "天气真好", "我喜欢这个系统"}
        text = "今天天晴\n今天天晴\n天气真好\n天气真好\n我喜欢这个系统\n认真检查系统"
        terms = list(extract_history_terms([text], known_term=raw_sentences.__contains__))
        self.assertEqual([], terms)

    def test_exact_dictionary_term_and_explicit_entity_are_retained(self):
        known = {"星河协议"}
        text = "星河协议\n北辰研究院\n普通短语"
        terms = list(extract_history_terms([text], known_term=known.__contains__))
        self.assertEqual(["星河协议", "北辰研究院"], terms)


if __name__ == "__main__":
    unittest.main()
