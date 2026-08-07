# SPDX-License-Identifier: Apache-2.0
"""Small, dependency-free data model for offline vocabulary imports."""

from __future__ import annotations

from collections import Counter, defaultdict
from dataclasses import dataclass, field
from typing import DefaultDict, Dict, Iterable, List, Tuple


@dataclass(frozen=True)
class Entry:
    phrase: str
    pinyin: str
    source: str
    use_count: int = 1


@dataclass
class ImportResult:
    entries: List[Entry] = field(default_factory=list)
    rejected: Counter = field(default_factory=Counter)
    duplicate_rows: int = 0


def coarse_count(value: int) -> int:
    """Bucket a private occurrence count to avoid preserving exact history."""
    value = max(1, int(value))
    bucket = 1
    while bucket < value and bucket < 1 << 20:
        bucket <<= 1
    return bucket


def merge_entries(entries: Iterable[Entry]) -> Tuple[List[Entry], int]:
    """Merge exact duplicates and fold an unpronounced row into one known reading."""
    grouped: DefaultDict[str, List[Entry]] = defaultdict(list)
    count = 0
    for entry in entries:
        grouped[entry.phrase].append(entry)
        count += 1

    merged: List[Entry] = []
    for phrase, rows in grouped.items():
        readings = {row.pinyin for row in rows if row.pinyin}
        canonical_reading = next(iter(readings)) if len(readings) == 1 else None
        by_key: Dict[Tuple[str, str], Dict[str, object]] = {}
        for row in rows:
            reading = row.pinyin or canonical_reading or ""
            key = (phrase, reading)
            slot = by_key.setdefault(key, {"sources": set(), "count": 0})
            slot["sources"].add(row.source)
            slot["count"] = min((1 << 31) - 1, int(slot["count"]) + max(1, row.use_count))
        for (merged_phrase, pinyin), values in by_key.items():
            merged.append(
                Entry(
                    phrase=merged_phrase,
                    pinyin=pinyin,
                    source="+".join(sorted(values["sources"])),
                    use_count=int(values["count"]),
                )
            )

    merged.sort(key=lambda row: (-row.use_count, -len(row.phrase), row.phrase, row.pinyin))
    return merged, count - len(merged)
