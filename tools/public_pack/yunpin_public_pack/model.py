# SPDX-License-Identifier: Apache-2.0
"""Data structures for deterministic public dictionary builds."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List, Set


@dataclass(frozen=True)
class SourceSpec:
    name: str
    url: str
    commit: str
    license: str
    root: Path


@dataclass
class Candidate:
    phrase: str
    pinyin: str
    weight: int
    primary_source: str
    sources: Set[str] = field(default_factory=set)


@dataclass
class SourceStats:
    parsed: int = 0
    accepted: int = 0
    unresolved: int = 0
    invalid: int = 0
    selected: int = 0
    files: List[Dict[str, str]] = field(default_factory=list)
