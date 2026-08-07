# SPDX-License-Identifier: Apache-2.0
"""Unicode and pinyin normalization with no third-party dependency."""

from __future__ import annotations

import re
import unicodedata


def is_cjk_character(character: str) -> bool:
    value = ord(character)
    return (
        value == 0x3007
        or 0x3400 <= value <= 0x4DBF
        or 0x4E00 <= value <= 0x9FFF
        or 0xF900 <= value <= 0xFAFF
        or 0x20000 <= value <= 0x2FA1F
        or 0x30000 <= value <= 0x323AF
    )


def normalize_phrase(value: str) -> str:
    value = unicodedata.normalize("NFKC", value).strip()
    value = "".join(value.split())
    if not value or len(value) > 96:
        return ""
    if not any(is_cjk_character(character) for character in value):
        return ""
    if any(unicodedata.category(character).startswith("C") for character in value):
        return ""
    if any(character in "\t\r\n" for character in value):
        return ""
    return value


def normalize_pinyin(value: str) -> str:
    value = unicodedata.normalize("NFKC", value).lower().strip().replace("u:", "v")
    value = value.translate(str.maketrans({"ü": "v", "ǖ": "v", "ǘ": "v", "ǚ": "v", "ǜ": "v"}))
    value = "".join(
        character
        for character in unicodedata.normalize("NFD", value)
        if not unicodedata.combining(character)
    )
    value = re.sub(r"[1-5]", "", value)
    value = value.replace("'", " ")
    value = re.sub(r"[^a-zv\s]", " ", value)
    return re.sub(r"\s+", " ", value).strip()


def parse_frequency(value: str) -> int:
    match = re.search(r"\d+", value)
    if not match:
        return 1
    return min(99_999_999, max(0, int(match.group(0))))
