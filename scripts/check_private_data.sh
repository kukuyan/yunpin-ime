#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# This is the only automated gate that keeps private vocabulary, replay traces
# and credential material out of the repository and the release artifacts, so it
# must fail closed: a scan that could not run is inconclusive, never a pass.
#
# The scan itself is Python rather than ripgrep. ripgrep is not present on the
# ubuntu-24.04 runner, and the previous `rg ... || true` spelling turned that
# missing binary into an empty result, so this gate reported "privacy scan
# passed" in CI without having read a single file. Installing ripgrep would mean
# an unpinned apt fetch in a repository that pins its Actions, images, submodules
# and archives by digest; python3 is already required by every other gate here.
if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required by the privacy gate; refusing to report a pass" >&2
  exit 1
fi

# The listing lives outside the repository: written inside it, the file would
# appear in its own untracked listing.
listing="$(mktemp "${TMPDIR:-/tmp}/yunpin-privacy-scan.XXXXXX")"
trap 'rm -f "$listing"' EXIT
if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git ls-files --cached --others --exclude-standard > "$listing"
else
  find . -type f -not -path './.git/*' > "$listing"
fi

python3 - "$listing" <<'SCAN'
"""Scan the working tree for private vocabulary, replay traces and secrets.

Any exception propagates and fails the gate. There is deliberately no path
through this program that reports success without having completed every scan.
"""
import re
import sys
from pathlib import Path

listing = Path(sys.argv[1])
# The find fallback emits "./platform/..." while git emits "platform/...".
# Normalising here keeps the fixture-boundary and exclusion rules identical
# whichever listing produced the paths.
paths = [
    line[2:] if line.startswith("./") else line
    for line in listing.read_text(encoding="utf-8").splitlines()
    if line
]

FORBIDDEN_TYPES = re.compile(
    r"(^|/)("
    r"conversations\.json|chat\.html|.*\.yunpinreplay"
    r"|.*replay.*\.(jsonl|ndjson|sqlite|sqlite3|db)(-[^/]*)?"
    r"|.*\.(scel|bin|sgpybin|sqlite|sqlite3|db|dpapi|p12|pfx|mobileprovision|pem|key)"
    r")$",
    re.IGNORECASE,
)
# Private vocabulary snapshots are TSV, which is also a legitimate fixture and
# template format, so the extension alone cannot decide. Such a file is accepted
# only as a synthetic test fixture or an ".example" template, and only below the
# size limit, so a real snapshot (the deployed one is over 4 MB) is rejected
# wherever it is placed.
PRIVATE_VOCABULARY = re.compile(r"(^|/)[^/]*private[^/]*\.tsv([.][A-Za-z0-9_-]+)?$", re.IGNORECASE)
PRIVATE_VOCABULARY_LIMIT = 65536
FIXTURE_DIRECTORY = re.compile(r"^platform/[^/]+/tests/fixtures/")
# Conflict copies produced by file-sync clients ("client 2.go", "0001-x 3.patch").
# Go's directory scan and every "*.patch" glob collect these, so they change what
# gets compiled and which patches get applied without ever entering a commit.
#
# They are deliberately NOT added to .gitignore: the listing above uses
# --exclude-standard, so ignoring them would hide them from this check and from
# `git status` at the same time. Staying visible is the point.
CONFLICT_COPY = re.compile(r"(^|/)[^/]+ [23](\.[^/.]+)*$")

SECRETS = re.compile(
    r"-----BEGIN ([A-Z ]+ )?PRIVATE KEY-----"
    r"|gh[pousr]_[A-Za-z0-9]{20,}"
    r"|sk-[A-Za-z0-9_-]{20,}"
    r"|AKIA[0-9A-Z]{16}"
    r"|xox[baprs]-[A-Za-z0-9-]{10,}"
)
# Mirrors the exclusions the ripgrep invocation used: generated output, caches,
# vendored upstream checkouts, and this gate's own pattern list.
SKIP_DIRECTORIES = (".git/", ".cache/", "build/", "engine/build/")
SKIP_FILES = ("scripts/check_private_data.sh",)


def fail(headline, offenders):
    print(headline, file=sys.stderr)
    for offender in offenders:
        print(offender, file=sys.stderr)
    raise SystemExit(1)


def skipped(path):
    if path in SKIP_FILES:
        return True
    if any(path == d.rstrip("/") or path.startswith(d) for d in SKIP_DIRECTORIES):
        return True
    if "/build/" in path:
        return True
    # third_party/*/** : vendored upstreams are excluded, but this repository's
    # own files directly under third_party/ (the lock files) are still scanned.
    parts = path.split("/")
    return len(parts) > 2 and parts[0] == "third_party"


bad_types = [p for p in paths if FORBIDDEN_TYPES.search(p)]
if bad_types:
    fail("forbidden private/export/secret file types detected:", bad_types)

offenders = []
for path in (p for p in paths if PRIVATE_VOCABULARY.search(p)):
    if not FIXTURE_DIRECTORY.match(path) and not path.endswith(".example"):
        offenders.append(f"{path} (neither a platform test fixture nor an .example template)")
        continue
    candidate = Path(path)
    if candidate.is_file():
        size = candidate.stat().st_size
        if size > PRIVATE_VOCABULARY_LIMIT:
            offenders.append(
                f"{path} ({size} bytes exceeds the {PRIVATE_VOCABULARY_LIMIT}-byte fixture limit)"
            )
if offenders:
    fail("private vocabulary file outside the allowed fixture boundary:", offenders)

conflicts = [p for p in paths if CONFLICT_COPY.search(p)]
if conflicts:
    fail("file-sync conflict copies detected; remove them before building:", conflicts)

secret_hits = []
for path in paths:
    if skipped(path):
        continue
    candidate = Path(path)
    if not candidate.is_file() or candidate.is_symlink():
        continue
    try:
        text = candidate.read_text(encoding="utf-8", errors="ignore")
    except OSError as error:
        # An unreadable tracked file means the scan is incomplete, and an
        # incomplete scan must not pass.
        fail("privacy scan could not read a file:", [f"{path}: {error}"])
    for number, line in enumerate(text.splitlines(), start=1):
        if SECRETS.search(line):
            secret_hits.append(f"{path}:{number}")
if secret_hits:
    fail("possible credential material detected:", secret_hits)
SCAN

echo "privacy scan passed: no forbidden replay trace, export, database, key, conflict copy, or known token pattern"
