# SPDX-License-Identifier: Apache-2.0
"""CLI for the offline public dictionary build."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Optional, Sequence

from .builder import BuildError, PublicPackBuilder, load_source_specs, validate_output_directory, write_outputs


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="yunpin-public-pack",
        description="Build the YunPin public Rime dictionary from verified local Git checkouts; never uses network",
    )
    parser.add_argument("--lock", required=True, help="third_party/upstreams.lock.json")
    parser.add_argument("--rime-ice-root", required=True)
    parser.add_argument("--rime-essay-root", required=True)
    parser.add_argument("--thuocl-root", required=True)
    parser.add_argument("--phrase-pinyin-root", required=True)
    output = parser.add_mutually_exclusive_group(required=True)
    output.add_argument("--output-dir", help="destination outside every Git repository")
    output.add_argument("--build-dir", help="explicit destination below the current repository's top-level build/")
    parser.add_argument(
        "--replace-existing",
        action="store_true",
        help="replace only the two deterministic public generated files if already present",
    )
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    roots = {
        "rime-ice": Path(args.rime_ice_root),
        "rime-essay": Path(args.rime_essay_root),
        "THUOCL": Path(args.thuocl_root),
        "phrase-pinyin-data": Path(args.phrase_pinyin_root),
    }
    try:
        specs = load_source_specs(Path(args.lock), roots)
        destination = Path(args.build_dir or args.output_dir)
        mode = "build" if args.build_dir else "output"
        destination = validate_output_directory(destination, mode, (spec.root for spec in specs.values()))
        dictionary_bytes, manifest_bytes, manifest = PublicPackBuilder(specs).build()
        write_outputs(destination, dictionary_bytes, manifest_bytes, replace_existing=args.replace_existing)
    except (BuildError, OSError, UnicodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(
        json.dumps(
            {
                "dictionary": manifest["dictionary"],
                "network_access": False,
                "output_mode": mode,
            },
            ensure_ascii=False,
            sort_keys=True,
        )
    )
    return 0
