#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"

unregister=false
if [[ "${1:-}" == "--unregister" ]]; then
  unregister=true
  shift
fi
app="${1:-}"
[[ -n "$app" && -d "$app" ]] || die "YunPin build bundle is missing: $app"
[[ "$app" == /* ]] || die "YunPin build bundle path must be absolute"
[[ "$#" -eq 1 ]] || die "unexpected LaunchServices verification arguments"

lsregister="${YUNPIN_LSREGISTER:-/System/Library/Frameworks/CoreServices.framework/Versions/Current/Frameworks/LaunchServices.framework/Versions/Current/Support/lsregister}"
[[ -x "$lsregister" ]] || die "LaunchServices registration tool is unavailable"

unregister_status=0
if [[ "$unregister" == true ]]; then
  set +e
  "$lsregister" -u "$app" >/dev/null 2>&1
  unregister_status=$?
  set -e
fi

dump="$(mktemp "${TMPDIR:-/tmp}/yunpin-launchservices.XXXXXX")"
trap 'rm -f "$dump"' EXIT
if ! "$lsregister" -dump >"$dump"; then
  die "unable to inspect LaunchServices after building the YunPin bundle"
fi

# Keep diagnostics useful without echoing the database's application paths.
# A format change should report only structural counts so CI evidence remains
# safe to publish and can be reduced to a fixture on the next run.
report_launchservices_shape() {
  /usr/bin/awk -v target="$app" '
    function exact_target_literal(text, position, boundary, after_slash) {
      position = index(text, target)
      if (position == 0) return 0
      boundary = substr(text, position + length(target), 1)
      if (boundary == "/") {
        after_slash = substr(text, position + length(target) + 1, 1)
        return after_slash == "" || after_slash == " " || after_slash == "\t" ||
          after_slash == "(" || after_slash == "\""
      }
      return boundary == "" || boundary == " " || boundary == "\t" ||
        boundary == "(" || boundary == "\""
    }
    /^Bundle:[[:space:]]/ {
      tables++
      line = $0
      if (match(line, /[0-9]+[[:space:]]+units/) != 0) {
        count = substr(line, RSTART, RLENGTH)
        sub(/[[:space:]]+units$/, "", count)
        expected = count + 0
        expected_known++
      }
    }
    /^bundle id:/ { records++ }
    exact_target_literal($0) { target_literals++ }
    /^path:/ {
      line = $0
      sub(/^path:[[:space:]]*/, "", line)
      sub(/[[:space:]]+\(0x[[:xdigit:]]+\)$/, "", line)
      if (line == target) target_paths++
    }
    END {
      expected_text = expected_known == 1 ? expected : "unknown"
      printf "LaunchServices structure: bundle_tables=%d expected_bundle_records=%s observed_bundle_records=%d target_literals=%d parsed_target_paths=%d\n", tables + 0, expected_text, records + 0, target_literals + 0, target_paths + 0
    }
  ' "$dump" >&2
}

grep -F 'Database is seeded.' "$dump" >/dev/null ||
  {
    report_launchservices_shape
    die "LaunchServices returned an unrecognized database dump"
  }

# `lsregister -dump` retains disabled tombstones for apps built on removable
# volumes, even after an exact unregister operation succeeds. Reject only an
# exact-path record that is still active. Cross-check the Bundle table count and
# every record boundary so truncated or format-shifted output fails closed.
set +e
/usr/bin/awk -v target="$app" '
  function exact_target_literal(text, position, boundary, after_slash) {
    position = index(text, target)
    if (position == 0) return 0
    boundary = substr(text, position + length(target), 1)
    if (boundary == "/") {
      after_slash = substr(text, position + length(target) + 1, 1)
      return after_slash == "" || after_slash == " " || after_slash == "\t" ||
        after_slash == "(" || after_slash == "\""
    }
    return boundary == "" || boundary == " " || boundary == "\t" ||
      boundary == "(" || boundary == "\""
  }
  function malformed_record() {
    malformed = 1
  }
  function finish_record() {
    if (!in_record) return
    if (path_count != 1) malformed_record()
    if (target_record) {
      if (bundle_flags_count != 1) malformed_record()
      if (!disabled) active = 1
    }
    complete_records++
    in_record = 0
    path_count = 0
    bundle_flags_count = 0
    target_record = 0
    disabled = 0
  }
  /^Bundle:[[:space:]]/ {
    if (bundle_table_seen) malformed_record()
    bundle_table_seen = 1
    line = $0
    if (match(line, /[0-9]+[[:space:]]+units/) == 0) {
      malformed_record()
      next
    }
    count = substr(line, RSTART, RLENGTH)
    sub(/[[:space:]]+units$/, "", count)
    expected_records = count + 0
    if (expected_records < 0) malformed_record()
    next
  }
  /^bundle id:/ {
    if (in_record) malformed_record()
    in_record = 1
    seen_records++
    path_count = 0
    bundle_flags_count = 0
    target_record = 0
    disabled = 0
    next
  }
  in_record && /^path:/ {
    line = $0
    if (exact_target_literal(line)) target_literal_seen = 1
    sub(/^path:[[:space:]]*/, "", line)
    if (line !~ /[[:space:]]+\(0x[[:xdigit:]]+\)$/) {
      malformed_record()
      next
    }
    sub(/[[:space:]]+\(0x[[:xdigit:]]+\)$/, "", line)
    path_count++
    if (line == target) {
      target_record = 1
      target_path_parsed = 1
    }
    next
  }
  in_record && /^bundle flags:/ {
    bundle_flags_count++
    if (index($0, "launch-disabled") != 0) disabled = 1
    next
  }
  exact_target_literal($0) {
    target_literal_seen = 1
  }
  /^-+$/ {
    finish_record()
  }
  END {
    if (in_record) malformed_record()
    if (!bundle_table_seen || seen_records != expected_records ||
        complete_records != seen_records) malformed_record()
    if (target_literal_seen && !target_path_parsed) malformed_record()
    if (malformed) exit 2
    if (active) exit 1
    exit 0
  }
' "$dump"
parse_status=$?
set -e

case "$parse_status" in
  0) ;;
  1)
    if [[ "$unregister" == true ]]; then
      die "the exact YunPin build bundle remains actively registered with LaunchServices after unregister status $unregister_status"
    fi
    die "the exact YunPin build bundle remains actively registered with LaunchServices"
    ;;
  2)
    report_launchservices_shape
    die "LaunchServices returned an incomplete or unrecognized database dump"
    ;;
  *) die "unable to parse the LaunchServices database dump" ;;
esac

if [[ "$unregister" == true && "$unregister_status" -ne 0 ]]; then
  printf 'LaunchServices unregister returned status %s; verified final state is inactive: %s\n' \
    "$unregister_status" "$app" >&2
fi
printf 'verified YunPin build path is not active in LaunchServices: %s\n' "$app"
