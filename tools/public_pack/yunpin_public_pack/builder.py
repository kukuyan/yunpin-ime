# SPDX-License-Identifier: Apache-2.0
"""Build a deterministic Rime dictionary from verified public sources."""

from __future__ import annotations

import hashlib
import json
import os
import tempfile
from pathlib import Path
from typing import Dict, Iterable, Iterator, List, Mapping, Optional, Sequence, Tuple

from .gitcheck import CheckoutError, verify_checkout, verify_tracked_file
from .model import Candidate, SourceSpec, SourceStats
from .normalize import is_cjk_character, normalize_phrase, normalize_pinyin, parse_frequency


BUILDER_VERSION = "0.1.0"
REQUIRED_SOURCES = ("rime-ice", "rime-essay", "THUOCL", "phrase-pinyin-data")
SOURCE_BANDS = {"rime-ice": 300_000_000, "THUOCL": 200_000_000, "rime-essay": 100_000_000}
SOURCE_PRIORITY = {name: len(SOURCE_BANDS) - index for index, name in enumerate(SOURCE_BANDS)}
DICTIONARY_NAME = "yunpin_public"
DICTIONARY_FILE = f"{DICTIONARY_NAME}.dict.yaml"
MANIFEST_FILE = f"{DICTIONARY_NAME}.sources.json"


class BuildError(RuntimeError):
    pass


def _source_file_record(root: Path, path: Path) -> Dict[str, str]:
    normalized = path.read_text(encoding="utf-8-sig", errors="strict").replace("\r\n", "\n").replace("\r", "\n")
    return {
        "path": path.relative_to(root).as_posix(),
        "normalized_sha256": hashlib.sha256(normalized.encode("utf-8")).hexdigest(),
    }


def load_source_specs(lock_path: Path, roots: Mapping[str, Path]) -> Dict[str, SourceSpec]:
    try:
        document = json.loads(lock_path.expanduser().resolve(strict=True).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise BuildError("upstream lock is not readable JSON") from exc
    if document.get("format") != 1 or not isinstance(document.get("upstreams"), list):
        raise BuildError("unsupported upstream lock format")
    locked: Dict[str, dict] = {}
    for item in document["upstreams"]:
        if not isinstance(item, dict) or not isinstance(item.get("name"), str):
            raise BuildError("malformed source entry in upstream lock")
        if item["name"] in locked:
            raise BuildError(f"duplicate source in upstream lock: {item['name']}")
        locked[item["name"]] = item

    if set(roots) != set(REQUIRED_SOURCES):
        raise BuildError("exactly four required source roots must be supplied")
    resolved_roots = [path.expanduser().resolve(strict=True) for path in roots.values()]
    if len(set(resolved_roots)) != len(resolved_roots):
        raise BuildError("each public source must use a distinct Git checkout")

    specs: Dict[str, SourceSpec] = {}
    for name in REQUIRED_SOURCES:
        item = locked.get(name)
        if item is None:
            raise BuildError(f"required source is absent from lock: {name}")
        values = (item.get("url"), item.get("commit"), item.get("license"))
        if not all(isinstance(value, str) and value for value in values):
            raise BuildError(f"required lock metadata is incomplete for {name}")
        spec = SourceSpec(
            name=name,
            url=item["url"],
            commit=item["commit"].lower(),
            license=item["license"],
            root=roots[name].expanduser().resolve(strict=True),
        )
        try:
            verify_checkout(spec.root, spec.commit)
        except CheckoutError as exc:
            raise BuildError(f"{name}: {exc}") from exc
        specs[name] = spec
    return specs


def _iter_rime_rows(path: Path) -> Iterator[Tuple[str, str, int]]:
    saw_header = False
    in_body = False
    with path.open("r", encoding="utf-8-sig", errors="strict") as handle:
        for raw in handle:
            stripped = raw.strip()
            if not stripped or stripped.startswith("#"):
                continue
            if stripped == "---":
                saw_header = True
                continue
            if stripped == "..." and saw_header:
                in_body = True
                continue
            if saw_header and not in_body:
                continue
            fields = [field.strip() for field in raw.rstrip("\r\n").split("\t")]
            if len(fields) < 2:
                continue
            phrase = normalize_phrase(fields[0])
            pinyin = normalize_pinyin(fields[1])
            if not phrase or not pinyin:
                continue
            weight = parse_frequency(fields[2]) if len(fields) > 2 else 1
            yield phrase, pinyin, weight


def _iter_frequency_rows(path: Path) -> Iterator[Tuple[str, int]]:
    with path.open("r", encoding="utf-8-sig", errors="strict") as handle:
        for raw in handle:
            stripped = raw.strip()
            if not stripped or stripped.startswith("#"):
                continue
            fields = stripped.rsplit(None, 1)
            if len(fields) != 2 or not fields[1].isdigit():
                continue
            phrase = normalize_phrase(fields[0])
            if phrase:
                yield phrase, parse_frequency(fields[1])


def _iter_phrase_pinyin_rows(path: Path) -> Iterator[Tuple[str, str]]:
    with path.open("r", encoding="utf-8-sig", errors="strict") as handle:
        for raw in handle:
            stripped = raw.split("#", 1)[0].strip()
            if not stripped or ":" not in stripped:
                continue
            phrase_value, pinyin_value = stripped.split(":", 1)
            phrase = normalize_phrase(phrase_value)
            pinyin = normalize_pinyin(pinyin_value)
            if phrase and pinyin:
                yield phrase, pinyin


class ReadingResolver:
    def __init__(self) -> None:
        self.phrase_data: Dict[str, str] = {}
        self.rime_phrases: Dict[str, Tuple[int, str]] = {}
        self.characters: Dict[str, Tuple[int, str]] = {}

    def add_phrase_data(self, phrase: str, pinyin: str) -> None:
        self.phrase_data[phrase] = pinyin
        if len(phrase) == 1:
            self.characters[phrase] = (400_000_000, pinyin)

    def add_rime(self, phrase: str, pinyin: str, weight: int) -> None:
        existing = self.rime_phrases.get(phrase)
        if existing is None or weight > existing[0] or (weight == existing[0] and pinyin < existing[1]):
            self.rime_phrases[phrase] = (weight, pinyin)
        if len(phrase) == 1:
            character = self.characters.get(phrase)
            if character is None or weight > character[0] or (weight == character[0] and pinyin < character[1]):
                self.characters[phrase] = (weight, pinyin)

    def resolve(self, phrase: str) -> str:
        pinyin = self.phrase_data.get(phrase)
        if pinyin:
            return pinyin
        rime = self.rime_phrases.get(phrase)
        if rime:
            return rime[1]
        readings: List[str] = []
        for character in phrase:
            if not is_cjk_character(character):
                return ""
            reading = self.characters.get(character)
            if reading is None:
                return ""
            readings.append(reading[1])
        return " ".join(readings)


class PublicPackBuilder:
    def __init__(self, specs: Mapping[str, SourceSpec]) -> None:
        self.specs = dict(specs)
        self.stats = {name: SourceStats() for name in REQUIRED_SOURCES}
        self.resolver = ReadingResolver()
        self.candidates: Dict[Tuple[str, str], Candidate] = {}
        self.duplicates_merged = 0

    def _register_file(self, source: str, path: Path) -> None:
        try:
            verify_tracked_file(self.specs[source].root, path)
        except CheckoutError as exc:
            raise BuildError(f"{source}: {exc}") from exc
        self.stats[source].files.append(_source_file_record(self.specs[source].root, path))

    def _add_candidate(self, source: str, phrase: str, pinyin: str, frequency: int) -> None:
        weight = SOURCE_BANDS[source] + min(99_999_999, max(0, frequency))
        key = (phrase, pinyin)
        existing = self.candidates.get(key)
        if existing is None:
            self.candidates[key] = Candidate(
                phrase=phrase,
                pinyin=pinyin,
                weight=weight,
                primary_source=source,
                sources={source},
            )
            return
        self.duplicates_merged += 1
        existing.sources.add(source)
        if weight > existing.weight or (
            weight == existing.weight
            and SOURCE_PRIORITY[source] > SOURCE_PRIORITY[existing.primary_source]
        ):
            existing.weight = weight
            existing.primary_source = source

    def _load_phrase_pinyin(self) -> None:
        source = "phrase-pinyin-data"
        root = self.specs[source].root
        paths = [root / name for name in ("large_pinyin.txt", "pinyin.txt", "overwrite.txt") if (root / name).is_file()]
        if not paths:
            raise BuildError("phrase-pinyin-data has no supported reading files")
        for path in paths:
            self._register_file(source, path)
            for phrase, pinyin in _iter_phrase_pinyin_rows(path):
                self.stats[source].parsed += 1
                self.resolver.add_phrase_data(phrase, pinyin)
                self.stats[source].accepted += 1

    def _load_rime_ice(self) -> None:
        source = "rime-ice"
        root = self.specs[source].root
        paths = sorted((root / "cn_dicts").glob("*.dict.yaml")) if (root / "cn_dicts").is_dir() else []
        if not paths:
            raise BuildError("rime-ice has no cn_dicts/*.dict.yaml files")
        for path in paths:
            self._register_file(source, path)
            for phrase, pinyin, frequency in _iter_rime_rows(path):
                self.stats[source].parsed += 1
                self.stats[source].accepted += 1
                self.resolver.add_rime(phrase, pinyin, frequency)
                self._add_candidate(source, phrase, pinyin, frequency)

    def _load_frequency_source(self, source: str, paths: Sequence[Path]) -> None:
        if not paths:
            raise BuildError(f"{source} has no supported frequency files")
        for path in paths:
            self._register_file(source, path)
            for phrase, frequency in _iter_frequency_rows(path):
                self.stats[source].parsed += 1
                pinyin = self.resolver.resolve(phrase)
                if not pinyin:
                    self.stats[source].unresolved += 1
                    continue
                self.stats[source].accepted += 1
                self._add_candidate(source, phrase, pinyin, frequency)

    def build(self) -> Tuple[bytes, bytes, Dict[str, object]]:
        self._load_phrase_pinyin()
        self._load_rime_ice()

        thuocl_root = self.specs["THUOCL"].root
        self._load_frequency_source("THUOCL", sorted((thuocl_root / "data").glob("THUOCL_*.txt")))
        essay_root = self.specs["rime-essay"].root
        essay_path = essay_root / "essay.txt"
        self._load_frequency_source("rime-essay", [essay_path] if essay_path.is_file() else [])

        candidates = sorted(
            self.candidates.values(),
            key=lambda candidate: (candidate.pinyin, -candidate.weight, candidate.phrase, candidate.primary_source),
        )
        for candidate in candidates:
            self.stats[candidate.primary_source].selected += 1

        fingerprint_rows = [
            {"name": name, "commit": self.specs[name].commit}
            for name in REQUIRED_SOURCES
        ]
        fingerprint = hashlib.sha256(
            json.dumps(fingerprint_rows, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
        ).hexdigest()
        lines = [
            "# Rime dictionary",
            "# encoding: utf-8",
            "# Generated offline by yunpin-public-pack; see yunpin_public.sources.json.",
            "---",
            f"name: {DICTIONARY_NAME}",
            f'version: "{fingerprint[:12]}"',
            "sort: by_weight",
            "use_preset_vocabulary: false",
            "columns:",
            "  - text",
            "  - code",
            "  - weight",
            "...",
        ]
        lines.extend(f"{candidate.phrase}\t{candidate.pinyin}\t{candidate.weight}" for candidate in candidates)
        dictionary_bytes = ("\n".join(lines) + "\n").encode("utf-8")

        sources_manifest = []
        for name in REQUIRED_SOURCES:
            spec = self.specs[name]
            stats = self.stats[name]
            sources_manifest.append(
                {
                    "name": name,
                    "url": spec.url,
                    "commit": spec.commit,
                    "license": spec.license,
                    "role": "reading resolver" if name == "phrase-pinyin-data" else "candidate source",
                    "files": sorted(stats.files, key=lambda item: item["path"]),
                    "stats": {
                        "parsed": stats.parsed,
                        "accepted": stats.accepted,
                        "unresolved": stats.unresolved,
                        "invalid": stats.invalid,
                        "selected": stats.selected,
                    },
                }
            )
        manifest: Dict[str, object] = {
            "format": 1,
            "builder": {"name": "yunpin-public-pack", "version": BUILDER_VERSION, "network_access": False},
            "dictionary": {
                "name": DICTIONARY_NAME,
                "file": DICTIONARY_FILE,
                "entry_count": len(candidates),
                "sha256": hashlib.sha256(dictionary_bytes).hexdigest(),
                "source_fingerprint": fingerprint,
            },
            "license": {
                "combined_output": "GPL-3.0",
                "notice": "Preserve every source license and attribution when redistributing this combined data.",
            },
            "policy": {
                "priority": list(SOURCE_BANDS),
                "weight_bands": SOURCE_BANDS,
                "dedupe_key": ["phrase", "pinyin"],
                "duplicates_merged": self.duplicates_merged,
                "unresolved_frequency_rows_are_excluded": True,
            },
            "sources": sources_manifest,
        }
        manifest_bytes = (json.dumps(manifest, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8")
        return dictionary_bytes, manifest_bytes, manifest


def _containing_git_root(path: Path) -> Optional[Path]:
    resolved = path.expanduser().resolve(strict=False)
    for parent in (resolved, *resolved.parents):
        if (parent / ".git").exists():
            return parent
    return None


def validate_output_directory(
    output: Path,
    mode: str,
    source_roots: Iterable[Path],
) -> Path:
    output = output.expanduser().resolve(strict=False)
    for source_root in source_roots:
        resolved_source = source_root.expanduser().resolve(strict=True)
        try:
            output.relative_to(resolved_source)
        except ValueError:
            continue
        raise BuildError("output must not be inside a verified upstream checkout")

    repository = _containing_git_root(output)
    if mode == "output":
        if repository is not None:
            raise BuildError("--output-dir must be outside every Git repository")
        return output
    if mode != "build":
        raise BuildError("unknown output mode")
    if repository is None:
        raise BuildError("--build-dir is only for an explicit repository build/ directory")
    relative = output.relative_to(repository)
    if not relative.parts or relative.parts[0] != "build":
        raise BuildError("repository output is allowed only below its top-level build/ directory")
    return output


def _write_atomic(path: Path, content: bytes, replace_existing: bool) -> None:
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent))
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        if replace_existing:
            os.replace(temporary, path)
        else:
            os.link(temporary, path)
            os.unlink(temporary)
    except Exception:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def write_outputs(
    output: Path,
    dictionary_bytes: bytes,
    manifest_bytes: bytes,
    replace_existing: bool = False,
) -> Tuple[Path, Path]:
    output.mkdir(parents=True, exist_ok=True)
    if not output.is_dir():
        raise BuildError("output path is not a directory")
    dictionary_path = output / DICTIONARY_FILE
    manifest_path = output / MANIFEST_FILE
    if not replace_existing and (dictionary_path.exists() or manifest_path.exists()):
        raise BuildError("generated output already exists; use --replace-existing for these public build files")
    _write_atomic(dictionary_path, dictionary_bytes, replace_existing)
    _write_atomic(manifest_path, manifest_bytes, replace_existing)
    return dictionary_path, manifest_path
