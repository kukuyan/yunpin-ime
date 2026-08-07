# SPDX-License-Identifier: Apache-2.0
"""Offline parsers for Rime/text dictionaries and exported chat history."""

from __future__ import annotations

import json
import re
from collections import Counter
from pathlib import Path
from typing import Dict, Iterable, Iterator, List, Optional, Sequence, Tuple

from .filters import extract_history_terms, normalize_phrase, normalize_pinyin, validate_phrase
from .model import Entry, ImportResult, coarse_count, merge_entries


SUPPORTED_CODEX_SUFFIXES = {".md", ".markdown", ".txt", ".json", ".jsonl"}


class PinyinResolver:
    def __init__(self) -> None:
        self.phrases: Dict[str, str] = {}
        self.characters: Dict[str, str] = {}

    def add(self, phrase: str, pinyin: str) -> None:
        phrase = normalize_phrase(phrase)
        pinyin = normalize_pinyin(pinyin)
        if not phrase or not pinyin:
            return
        self.phrases.setdefault(phrase, pinyin)
        if len(phrase) == 1 and " " not in pinyin:
            self.characters.setdefault(phrase, pinyin)

    def resolve(self, phrase: str) -> str:
        exact = self.phrases.get(phrase)
        if exact:
            return exact
        syllables = [self.characters.get(character) for character in phrase]
        if all(syllables):
            return " ".join(syllables)  # type: ignore[arg-type]
        return ""

    def is_known_phrase(self, phrase: str) -> bool:
        """Check exact terminology membership without composing character readings."""
        return phrase in self.phrases


def _parse_count(value: str, default: int = 1) -> int:
    match = re.search(r"\d+", value or "")
    if not match:
        return default
    return max(1, min((1 << 31) - 1, int(match.group(0))))


def parse_dictionary_line(line: str, source: str) -> Optional[Entry]:
    line = line.lstrip("\ufeff").strip()
    if not line or line.startswith(("#", "---", "...")):
        return None

    if "\t" in line:
        fields = [field.strip() for field in line.split("\t")]
    elif ":" in line:
        phrase, pinyin = [field.strip() for field in line.split(":", 1)]
        if not re.search(r"[\u3400-\u9fff]", phrase):
            return None
        fields = [phrase, pinyin, "1"]
    elif "," in line:
        fields = [field.strip() for field in line.split(",")]
    else:
        fields = re.split(r"\s{2,}", line)
        if len(fields) == 1:
            parts = line.split()
            cjk_positions = [index for index, part in enumerate(parts) if re.search(r"[\u3400-\u9fff]", part)]
            if not cjk_positions:
                return None
            cjk_index = cjk_positions[0]
            if cjk_index == 0:
                phrase = parts[0]
                count = parts[-1] if parts[-1].isdigit() else "1"
                pinyin_parts = parts[1:-1] if parts[-1].isdigit() else parts[1:]
                fields = [phrase, " ".join(pinyin_parts), count]
            else:
                phrase = parts[cjk_index]
                pinyin_parts = parts[:cjk_index]
                count = parts[cjk_index + 1] if len(parts) > cjk_index + 1 else "1"
                fields = [phrase, " ".join(pinyin_parts), count]

    if len(fields) < 2:
        return None
    if re.search(r"[\u3400-\u9fff]", fields[0]):
        phrase, pinyin = fields[0], fields[1]
        count = fields[2] if len(fields) > 2 else "1"
    elif re.search(r"[\u3400-\u9fff]", fields[1]):
        pinyin, phrase = fields[0], fields[1]
        count = fields[2] if len(fields) > 2 else "1"
    else:
        return None

    phrase = normalize_phrase(phrase)
    return Entry(phrase=phrase, pinyin=normalize_pinyin(pinyin), source=source, use_count=_parse_count(count))


def parse_dictionary(paths: Sequence[Path], source: str) -> ImportResult:
    result = ImportResult()
    for path in paths:
        with path.open("r", encoding="utf-8-sig", errors="replace") as handle:
            in_rime_body = False
            saw_rime_header = False
            for line in handle:
                stripped = line.strip()
                if stripped == "---":
                    saw_rime_header = True
                    continue
                if stripped == "..." and saw_rime_header:
                    in_rime_body = True
                    continue
                if saw_rime_header and not in_rime_body:
                    continue
                entry = parse_dictionary_line(line, source)
                if entry is None:
                    if stripped and not stripped.startswith(("#", "---", "...")):
                        result.rejected["unparsed_or_filtered"] += 1
                    continue
                reason = validate_phrase(entry.phrase)[1]
                if reason:
                    result.rejected[reason] += 1
                    continue
                result.entries.append(entry)
    result.entries, result.duplicate_rows = merge_entries(result.entries)
    return result


def load_pinyin_resolver(paths: Sequence[Path]) -> PinyinResolver:
    resolver = PinyinResolver()
    if not paths:
        return resolver
    result = parse_dictionary(paths, "pinyin_reference")
    for entry in result.entries:
        resolver.add(entry.phrase, entry.pinyin)
    return resolver


def _chatgpt_texts(document: object, include_assistant: bool = False) -> Iterator[str]:
    conversations = document if isinstance(document, list) else [document]
    for conversation in conversations:
        if not isinstance(conversation, dict):
            continue
        mapping = conversation.get("mapping", {})
        if not isinstance(mapping, dict):
            continue
        for node in mapping.values():
            if not isinstance(node, dict):
                continue
            message = node.get("message")
            if not isinstance(message, dict):
                continue
            author = message.get("author") or {}
            role = author.get("role") if isinstance(author, dict) else None
            if role != "user" and not (include_assistant and role == "assistant"):
                continue
            content = message.get("content") or {}
            parts = content.get("parts") if isinstance(content, dict) else None
            if isinstance(parts, list):
                for part in parts:
                    if isinstance(part, str):
                        yield part


def _codex_json_texts(value: object) -> Iterator[str]:
    if isinstance(value, dict):
        role = value.get("role")
        if role == "user":
            content = value.get("content")
            if isinstance(content, str):
                yield content
            elif isinstance(content, list):
                for item in content:
                    if isinstance(item, dict) and isinstance(item.get("text"), str):
                        yield item["text"]
        for key in ("summary", "memory_summary", "title", "keywords"):
            field = value.get(key)
            if isinstance(field, str):
                yield field
        for child in value.values():
            if isinstance(child, (dict, list)):
                yield from _codex_json_texts(child)
    elif isinstance(value, list):
        for child in value:
            yield from _codex_json_texts(child)


def _iter_codex_files(paths: Sequence[Path]) -> Iterator[Path]:
    for path in paths:
        if path.is_dir():
            for child in sorted(path.rglob("*")):
                if child.is_file() and child.suffix.lower() in SUPPORTED_CODEX_SUFFIXES and not any(
                    part.startswith(".") for part in child.relative_to(path).parts
                ):
                    yield child
        elif path.suffix.lower() in SUPPORTED_CODEX_SUFFIXES:
            yield path


def _codex_texts(paths: Sequence[Path]) -> Iterator[str]:
    for path in _iter_codex_files(paths):
        suffix = path.suffix.lower()
        if suffix in {".md", ".markdown", ".txt"}:
            yield path.read_text(encoding="utf-8", errors="replace")
            continue
        if suffix == ".json":
            try:
                yield from _codex_json_texts(json.loads(path.read_text(encoding="utf-8", errors="replace")))
            except json.JSONDecodeError:
                continue
            continue
        with path.open("r", encoding="utf-8", errors="replace") as handle:
            for line in handle:
                try:
                    yield from _codex_json_texts(json.loads(line))
                except json.JSONDecodeError:
                    continue


def parse_history(
    paths: Sequence[Path],
    kind: str,
    resolver: PinyinResolver,
    min_count: int = 2,
    max_phrases: int = 50000,
    include_assistant: bool = False,
) -> ImportResult:
    texts: Iterable[str]
    if kind == "chatgpt":
        documents: List[object] = []
        for path in paths:
            documents.append(json.loads(path.read_text(encoding="utf-8", errors="strict")))
        texts = (
            text
            for document in documents
            for text in _chatgpt_texts(document, include_assistant=include_assistant)
        )
    elif kind == "codex":
        texts = _codex_texts(paths)
    else:
        raise ValueError(f"unsupported history kind: {kind}")

    result = ImportResult()
    counts = Counter(extract_history_terms(texts, result.rejected, resolver.is_known_phrase))
    selected = sorted(counts.items(), key=lambda item: (-item[1], -len(item[0]), item[0]))[:max_phrases]
    for phrase, count in selected:
        if count < max(1, min_count):
            result.rejected["below_history_threshold"] += 1
            continue
        valid, reason = validate_phrase(phrase)
        if not valid:
            result.rejected[reason or "filtered"] += 1
            continue
        result.entries.append(
            Entry(
                phrase=phrase,
                pinyin=resolver.resolve(phrase),
                source=f"{kind}_history",
                use_count=coarse_count(count),
            )
        )
    result.entries, result.duplicate_rows = merge_entries(result.entries)
    return result


def guess_kind(path: Path) -> str:
    if path.is_dir():
        return "codex"
    suffix = path.suffix.lower()
    if suffix == ".json" and path.name == "conversations.json":
        return "chatgpt"
    if suffix in {".md", ".markdown", ".jsonl"}:
        return "codex"
    if suffix in {".yaml", ".yml"} or ".dict." in path.name:
        return "rime"
    return "text"
