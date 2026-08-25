#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# This is the only automated gate that keeps private vocabulary, replay traces
# and credential material out of the repository and the release artifacts. It
# must fail closed: a missing scanner is an inconclusive scan, never a pass.
if ! command -v rg >/dev/null 2>&1; then
  echo "ripgrep (rg) is required by the privacy gate; refusing to report a pass" >&2
  exit 1
fi

# rg exits 0 on match and 1 on no-match; every other status (2 = usage/IO
# error, 127 = missing binary, 130 = interrupt) means the scan never ran.
# Collapsing those into "no match" is what previously let this gate report a
# pass without scanning anything, so each call checks its own status.
run_scan() {
  local description="$1"
  shift
  local output status
  set +e
  output="$("$@")"
  status=$?
  set -e
  if [[ $status -ne 0 && $status -ne 1 ]]; then
    echo "privacy scan failed to run (${description}: rg exit ${status})" >&2
    exit 1
  fi
  printf '%s' "$output"
}

if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  files="$(git ls-files --cached --others --exclude-standard)"
else
  files="$(find . -type f -not -path './.git/*')"
fi

bad_paths="$(printf '%s\n' "$files" | run_scan "forbidden file types" rg -i '(^|/)(conversations\.json|chat\.html|.*\.yunpinreplay|.*replay.*\.(jsonl|ndjson|sqlite|sqlite3|db)(-[^/]*)?|.*\.(scel|bin|sgpybin|sqlite|sqlite3|db|dpapi|p12|pfx|mobileprovision|pem|key))$')"
if [[ -n "$bad_paths" ]]; then
  echo "forbidden private/export/secret file types detected:" >&2
  echo "$bad_paths" >&2
  exit 1
fi

# Private vocabulary snapshots are TSV, which is also a legitimate fixture and
# template format, so the extension alone cannot decide. A private-looking TSV
# is accepted only as a synthetic test fixture or an empty ".example" template,
# and in both cases only below the fixture size limit. A real snapshot (the
# deployed one is over 4 MB) is therefore rejected wherever it is placed.
private_vocabulary_limit=65536
private_vocabulary="$(printf '%s\n' "$files" | run_scan "private vocabulary snapshots" rg -i '(^|/)[^/]*private[^/]*\.tsv([.][A-Za-z0-9_-]+)?$')"
if [[ -n "$private_vocabulary" ]]; then
  while IFS= read -r candidate; do
    [[ -n "$candidate" ]] || continue
    case "$candidate" in
      platform/*/tests/fixtures/*) ;;
      *.example) ;;
      *)
        echo "private vocabulary file is neither a platform test fixture nor an .example template:" >&2
        echo "$candidate" >&2
        exit 1
        ;;
    esac
    if [[ -f "$candidate" ]]; then
      size="$(wc -c < "$candidate" | tr -d ' ')"
      if (( size > private_vocabulary_limit )); then
        echo "private vocabulary file exceeds the ${private_vocabulary_limit}-byte synthetic-fixture limit:" >&2
        echo "$candidate ($size bytes)" >&2
        exit 1
      fi
    fi
  done <<< "$private_vocabulary"
fi

# Conflict copies produced by file-sync clients ("client 2.go", "0001-x 3.patch")
# are picked up by Go's directory scan and by every "*.patch" glob. They break
# builds and patch-series verification without ever appearing in a commit, so
# the gate rejects them in the working tree.
#
# They are deliberately NOT added to .gitignore: the listing above uses
# --exclude-standard, so ignoring them would hide them from this check and from
# `git status` at the same time. Staying visible is the point.
conflict_copies="$(printf '%s\n' "$files" | run_scan "sync-client conflict copies" rg '(^|/)[^/]+ [23](\.[^/.]+)*$')"
if [[ -n "$conflict_copies" ]]; then
  echo "file-sync conflict copies detected; remove them before building:" >&2
  echo "$conflict_copies" >&2
  exit 1
fi

secret_hits="$(run_scan "credential patterns" rg -n --hidden \
  --glob '!.git/**' \
  --glob '!.cache/**' \
  --glob '!build/**' \
  --glob '!engine/build/**' \
  --glob '!third_party/*/**' \
  --glob '!scripts/check_private_data.sh' \
  '(-----BEGIN ([A-Z ]+ )?PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,})' .)"
if [[ -n "$secret_hits" ]]; then
  echo "possible credential material detected:" >&2
  echo "$secret_hits" >&2
  exit 1
fi

echo "privacy scan passed: no forbidden replay trace, export, database, key, conflict copy, or known token pattern"
