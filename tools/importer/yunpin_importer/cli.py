# SPDX-License-Identifier: Apache-2.0
"""Command line interface for privacy-preserving, offline imports."""

from __future__ import annotations

import argparse
import csv
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Iterable, List, Optional, Sequence

from .filters import masked_phrase
from .model import Entry, ImportResult, merge_entries
from .parsers import guess_kind, load_pinyin_resolver, parse_dictionary, parse_history
from .sogou import SogouConversionError, convert_with_pinned_tool, dispose_artifact


CONFIRMATION = "IMPORT"
MAX_PRIVATE_SNAPSHOT_ENTRIES = 100_000


def _assert_output_outside_repository(path: Path) -> None:
    resolved = path.expanduser().resolve(strict=False)
    for parent in (resolved.parent, *resolved.parents):
        if (parent / ".git").exists():
            raise ValueError("personal dictionary output must be outside every Git repository")


def _write_atomic_tsv(path: Path, entries: Iterable[Entry]) -> None:
    _assert_output_outside_repository(path)
    path = path.expanduser().resolve(strict=False)
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists():
        raise ValueError("private destination already exists; choose a new path so no data is overwritten")
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent), text=True)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as handle:
            writer = csv.writer(handle, dialect="excel-tab", lineterminator="\n")
            writer.writerow(("phrase", "pinyin", "source", "use_count"))
            for entry in entries:
                writer.writerow((entry.phrase, entry.pinyin, entry.source, entry.use_count))
            handle.flush()
            os.fsync(handle.fileno())
        os.link(temporary_name, path)
        os.unlink(temporary_name)
    except Exception:
        try:
            os.unlink(temporary_name)
        except FileNotFoundError:
            pass
        raise


def _print_preview(result: ImportResult, preview_limit: int, reveal: bool) -> None:
    missing_pinyin = sum(1 for entry in result.entries if not entry.pinyin)
    summary = {
        "candidate_count": len(result.entries),
        "duplicate_rows_merged": result.duplicate_rows,
        "missing_pinyin": missing_pinyin,
        "rejected_by_reason": dict(sorted(result.rejected.items())),
        "preview_is_masked": not reveal,
        "raw_sentences_persisted": False,
    }
    print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    for entry in result.entries[: max(0, preview_limit)]:
        phrase = entry.phrase if reveal else masked_phrase(entry.phrase)
        pinyin = entry.pinyin if reveal else ("present" if entry.pinyin else "missing")
        print(f"{phrase}\t{pinyin}\t{entry.source}\t{entry.use_count}")


def _finish(result: ImportResult, args: argparse.Namespace) -> int:
    _print_preview(result, args.preview_limit, args.reveal_phrases)
    if args.confirm != CONFIRMATION:
        print(f"preview only; rerun with --confirm {CONFIRMATION} and --output outside the repository", file=sys.stderr)
        return 0
    if not args.output:
        raise ValueError("--output is required when --confirm IMPORT is used")
    _write_atomic_tsv(Path(args.output), result.entries)
    print(f"wrote {len(result.entries)} filtered entries to the confirmed private destination")
    return 0


def _import_command(args: argparse.Namespace) -> int:
    paths = [Path(item).expanduser().resolve(strict=True) for item in args.inputs]
    kinds = {guess_kind(path) if args.kind == "auto" else args.kind for path in paths}
    if len(kinds) != 1:
        raise ValueError("mixed automatic input kinds must be imported in separate runs")
    kind = next(iter(kinds))
    resolver = load_pinyin_resolver([Path(item).expanduser().resolve(strict=True) for item in args.pinyin_dict])
    if kind in {"rime", "text"}:
        result = parse_dictionary(paths, kind)
    else:
        result = parse_history(
            paths,
            kind,
            resolver,
            min_count=args.min_history_count,
            max_phrases=args.max_history_phrases,
            include_assistant=args.include_assistant,
        )
    if args.require_pinyin:
        with_pinyin = [entry for entry in result.entries if entry.pinyin]
        result.rejected["missing_pinyin_required"] += len(result.entries) - len(with_pinyin)
        result.entries = with_pinyin
    return _finish(result, args)


def _sogou_command(args: argparse.Namespace) -> int:
    artifact = None
    try:
        artifact = convert_with_pinned_tool(
            source=Path(args.input),
            converter=Path(args.converter),
            converter_sha256=args.converter_sha256,
            source_format=args.source_format,
            expected_source_sha256=args.source_sha256,
            dotnet=args.dotnet,
            timeout_seconds=args.timeout,
        )
        result = parse_dictionary([artifact.converted_path], f"sogou_{artifact.source_format}")
        # The native private snapshot has a hard 100,000-entry budget.  The
        # dictionary parser has already merged duplicates and sorted entries by
        # descending use_count, so truncating here retains the most valuable
        # personal vocabulary instead of letting the runtime silently keep an
        # arbitrary file prefix.
        maximum = max(
            1,
            min(MAX_PRIVATE_SNAPSHOT_ENTRIES, int(args.max_sogou_phrases)),
        )
        if len(result.entries) > maximum:
            result.rejected["over_private_snapshot_capacity"] += (
                len(result.entries) - maximum
            )
            result.entries = result.entries[:maximum]
        print(
            json.dumps(
                {
                    "source_sha256": artifact.source_sha256,
                    "staged_copy_sha256": artifact.copied_sha256,
                    "converter_sha256": artifact.converter_sha256,
                    "source_unchanged": artifact.source_sha256 == artifact.copied_sha256,
                },
                sort_keys=True,
            )
        )
        return _finish(result, args)
    finally:
        if artifact is not None:
            dispose_artifact(artifact)


def _add_common_preview_options(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--output", help="filtered TSV destination; must be outside the Git repository")
    parser.add_argument("--confirm", help=f"write only when this is exactly {CONFIRMATION}")
    parser.add_argument("--preview-limit", type=int, default=12)
    parser.add_argument("--reveal-phrases", action="store_true", help="show unmasked local preview values")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="yunpin-import", description="Offline YunPin personal vocabulary importer")
    subcommands = parser.add_subparsers(dest="command", required=True)

    import_parser = subcommands.add_parser("import", help="preview/import text, Rime, ChatGPT or Codex data")
    import_parser.add_argument("inputs", nargs="+")
    import_parser.add_argument("--kind", choices=("auto", "rime", "text", "chatgpt", "codex"), default="auto")
    import_parser.add_argument("--pinyin-dict", action="append", default=[], help="offline Rime/text lookup (repeatable)")
    import_parser.add_argument("--min-history-count", type=int, default=2)
    import_parser.add_argument("--max-history-phrases", type=int, default=50000)
    import_parser.add_argument(
        "--require-pinyin",
        action="store_true",
        help="discard entries whose pinyin cannot be resolved offline",
    )
    import_parser.add_argument(
        "--include-assistant",
        action="store_true",
        help="also inspect assistant text in ChatGPT exports (off by default)",
    )
    _add_common_preview_options(import_parser)
    import_parser.set_defaults(handler=_import_command)

    sogou_parser = subcommands.add_parser("sogou", help="convert a copied SCEL/BIN using pinned ImeWlConverter")
    sogou_parser.add_argument("input")
    sogou_parser.add_argument("--source-format", choices=("auto", "scel", "sgpybin"), default="auto")
    sogou_parser.add_argument("--source-sha256", help="optional expected hash from the read-only snapshot")
    sogou_parser.add_argument("--converter", required=True, help="ImeWlConverter v3.4.3 executable or DLL")
    sogou_parser.add_argument("--converter-sha256", required=True, help="SHA-256 of that exact converter file")
    sogou_parser.add_argument("--dotnet", default="dotnet")
    sogou_parser.add_argument("--timeout", type=int, default=180)
    sogou_parser.add_argument(
        "--max-sogou-phrases",
        type=int,
        default=MAX_PRIVATE_SNAPSHOT_ENTRIES,
        help=(
            "retain at most this many highest-frequency merged entries "
            f"(hard cap: {MAX_PRIVATE_SNAPSHOT_ENTRIES})"
        ),
    )
    _add_common_preview_options(sogou_parser)
    sogou_parser.set_defaults(handler=_sogou_command)
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return int(args.handler(args))
    except OSError as exc:
        print(f"error: local filesystem operation failed ({type(exc).__name__})", file=sys.stderr)
        return 2
    except (ValueError, json.JSONDecodeError, SogouConversionError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
