#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

mapfile_cmd=cat
if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  files="$(git ls-files --cached --others --exclude-standard)"
else
  files="$(find . -type f -not -path './.git/*')"
fi

bad_paths="$(printf '%s\n' "$files" | rg -i '(^|/)(conversations\.json|chat\.html|.*\.(scel|bin|sgpybin|sqlite|sqlite3|db|p12|pfx|mobileprovision|pem|key))$' || true)"
if [[ -n "$bad_paths" ]]; then
  echo "forbidden private/export/secret file types detected:" >&2
  echo "$bad_paths" >&2
  exit 1
fi

secret_hits="$(rg -n --hidden \
  --glob '!.git/**' \
  --glob '!.cache/**' \
  --glob '!build/**' \
  --glob '!engine/build/**' \
  --glob '!third_party/*/**' \
  --glob '!scripts/check_private_data.sh' \
  '(-----BEGIN ([A-Z ]+ )?PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,})' . || true)"
if [[ -n "$secret_hits" ]]; then
  echo "possible credential material detected:" >&2
  echo "$secret_hits" >&2
  exit 1
fi

echo "privacy scan passed: no forbidden export, database, key, or known token pattern"
