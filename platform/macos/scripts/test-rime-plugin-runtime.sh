#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail

source "$(dirname "$0")/common.sh"
require_macos
require_full_xcode

app="${1:-${REPO_ROOT}/build/macos/DerivedData/Build/Products/Release/YunPin.app}"
source_dir="${2:-${REPO_ROOT}/build/macos/squirrel}"
metrics_output="${3:-${REPO_ROOT}/build/macos/package/grammar-quality-metrics.json}"
frameworks="$app/Contents/Frameworks"
shared_support="$app/Contents/SharedSupport"
librime="$frameworks/librime.1.dylib"
plugin_dir="$frameworks/rime-plugins"
probe_source="$MACOS_DIR/tests/rime_public_candidate_probe.cpp"

[[ -f "$librime" && -d "$plugin_dir" && -d "$shared_support" ]] ||
  die "YunPin app is incomplete for the real Rime plugin runtime test"
[[ -f "$source_dir/librime/src/rime_api.h" && -f "$probe_source" ]] ||
  die "Rime runtime probe source or headers are missing"

expected_plugins='librime-lua.dylib librime-octagram.dylib librime-predict.dylib '
actual_plugins="$(find "$plugin_dir" -maxdepth 1 -type f -name '*.dylib' -exec basename {} \; | LC_ALL=C sort | tr '\n' ' ')"
[[ "$actual_plugins" == "$expected_plugins" ]] ||
  die "YunPin app has an incomplete or unexpected Rime plugin set: $actual_plugins"

temporary="$(mktemp -d "${TMPDIR:-/tmp}/yunpin-rime-plugin-runtime.XXXXXX")"
trap 'rm -rf "$temporary"' EXIT
probe="$temporary/rime-public-candidate-probe"
model_user="$temporary/model-user"
baseline_user="$temporary/baseline-user"
for user_dir in "$model_user" "$baseline_user"; do
  mkdir -m 700 "$user_dir"
  mkdir -m 700 "$user_dir/lua"
  mkdir -m 700 "$user_dir/yunpin"
  install -m 600 "$shared_support/lua/lunar.db" "$user_dir/lua/lunar.db"
  install -m 600 "$MACOS_DIR/tests/fixtures/private.tsv" \
    "$user_dir/yunpin/private.tsv"
done
/usr/bin/python3 - "$shared_support/rime_ice.custom.yaml" \
  "$baseline_user/rime_ice.custom.yaml" <<'PY'
from pathlib import Path
import sys

source = Path(sys.argv[1]).read_text(encoding="utf-8")
lines = source.splitlines(keepends=True)
grammar = [line for line in lines if line.startswith('  "grammar/')]
if len(grammar) != 7:
    raise SystemExit("packaged overlay grammar key count changed")
enabled = '  "translator/contextual_suggestions": true'
if sum(line.rstrip("\r\n") == enabled for line in lines) != 1:
    raise SystemExit("packaged overlay contextual setting changed")
baseline = "".join(
    line.replace(enabled, '  "translator/contextual_suggestions": false')
    for line in lines
    if not line.startswith('  "grammar/')
)
if '"grammar/' in baseline or baseline.count(
    '"translator/contextual_suggestions": false'
) != 1:
    raise SystemExit("failed to create exact no-grammar baseline overlay")
Path(sys.argv[2]).write_text(baseline, encoding="utf-8")
PY

xcrun clang++ \
  -std=c++17 -Wall -Wextra -Wpedantic -Werror \
  -Wno-missing-field-initializers \
  -mmacosx-version-min=13.0 \
  -I "$source_dir/librime/src" \
  "$probe_source" "$librime" \
  -Wl,-rpath,"$frameworks" \
  -o "$probe"

for mode in baseline model; do
  user_dir="$baseline_user"
  [[ "$mode" == model ]] && user_dir="$model_user"
  phase="prepare-$mode"
  set +e
  DYLD_PRINT_LIBRARIES=1 "$probe" "$shared_support" "$user_dir" "$phase" \
    >"$temporary/$phase.stdout" 2>"$temporary/$phase.stderr"
  probe_status=$?
  set -e
  if [[ "$probe_status" -ne 0 ]]; then
    cat "$temporary/$phase.stdout" >&2
    tail -n 200 "$temporary/$phase.stderr" >&2
    die "real Rime $phase deployment probe failed with status $probe_status"
  fi
  grep -Fx "mode=$phase" "$temporary/$phase.stdout" >/dev/null ||
    die "real Rime deployment probe reported the wrong phase"
  grep -Fx 'deployment_pass=true' "$temporary/$phase.stdout" >/dev/null ||
    die "real Rime deployment phase did not complete"
  grep -Fx 'cache_condition=isolated-deployment-process-os-warm' \
    "$temporary/$phase.stdout" >/dev/null ||
    die "real Rime deployment phase reported the wrong cache condition"
  for plugin in librime-lua.dylib librime-octagram.dylib librime-predict.dylib; do
    grep -F "$plugin_dir/$plugin" "$temporary/$phase.stderr" >/dev/null ||
      die "real Rime $phase deployment did not load packaged plugin: $plugin"
  done
done

# Deployment and measurement are deliberately different processes. This keeps
# dictionary compilation and maintenance allocations out of resident runtime
# RSS and cold-stage latency evidence.
for mode in baseline model; do
  user_dir="$baseline_user"
  [[ "$mode" == model ]] && user_dir="$model_user"
  set +e
  DYLD_PRINT_LIBRARIES=1 "$probe" "$shared_support" "$user_dir" "$mode" \
    >"$temporary/$mode.stdout" 2>"$temporary/$mode.stderr"
  probe_status=$?
  set -e
  if [[ "$probe_status" -ne 0 ]]; then
    cat "$temporary/$mode.stdout" >&2
    tail -n 200 "$temporary/$mode.stderr" >&2
    die "real Rime $mode measurement probe failed with status $probe_status"
  fi
  for plugin in librime-lua.dylib librime-octagram.dylib librime-predict.dylib; do
    grep -F "$plugin_dir/$plugin" "$temporary/$mode.stderr" >/dev/null ||
      die "real Rime $mode measurement did not load packaged plugin: $plugin"
  done
done

# The private-on run above proves first-place injection. Remove only the exact
# synthetic fixture and start a fresh process against the same deployed user
# data; the entire visible candidate page must no longer contain it.
private_fixture="$model_user/yunpin/private.tsv"
[[ -f "$private_fixture" && ! -L "$private_fixture" ]] ||
  die "synthetic private fixture is missing or unsafe before counterfactual"
rm -f -- "$private_fixture"
set +e
DYLD_PRINT_LIBRARIES=1 "$probe" "$shared_support" "$model_user" private-off \
  >"$temporary/private-off.stdout" 2>"$temporary/private-off.stderr"
private_off_status=$?
set -e
if [[ "$private_off_status" -ne 0 ]]; then
  cat "$temporary/private-off.stdout" >&2
  tail -n 200 "$temporary/private-off.stderr" >&2
  die "real Rime private-off counterfactual failed with status $private_off_status"
fi
grep -Fx 'synthetic_private_counterfactual=pass' \
  "$temporary/private-off.stdout" >/dev/null ||
  die "real Rime private-off counterfactual omitted its fixed pass result"
for plugin in librime-lua.dylib librime-octagram.dylib librime-predict.dylib; do
  grep -F "$plugin_dir/$plugin" "$temporary/private-off.stderr" >/dev/null ||
    die "real Rime private-off process did not load packaged plugin: $plugin"
done

grammar_model_filename="$(read_lock_value grammarModel.filename)"
model_load_log="loading gram db: $shared_support/$grammar_model_filename"
model_load_count="$(grep -Fc "$model_load_log" "$temporary/model.stderr" || true)"
baseline_load_count="$(grep -Fc 'loading gram db:' "$temporary/baseline.stderr" || true)"
[[ "$model_load_count" == 1 && "$baseline_load_count" == 0 ]] || {
  tail -n 200 "$temporary/model.stderr" >&2
  tail -n 200 "$temporary/baseline.stderr" >&2
  die "grammar model file-open log must occur once during model schema select and never in baseline"
}
schema_begin_line="$(grep -nFx 'schema_select_begin' "$temporary/model.stderr" | cut -d: -f1)"
model_load_line="$(grep -nF "$model_load_log" "$temporary/model.stderr" | cut -d: -f1)"
schema_end_line="$(grep -nFx 'schema_select_end' "$temporary/model.stderr" | cut -d: -f1)"
[[ "$schema_begin_line" =~ ^[0-9]+$ && "$model_load_line" =~ ^[0-9]+$ &&
   "$schema_end_line" =~ ^[0-9]+$ &&
   "$schema_begin_line" -lt "$model_load_line" &&
   "$model_load_line" -lt "$schema_end_line" ]] ||
  die "grammar model file-open log did not occur inside schema selection"

for expected in \
  'mode=model' \
  'octagram_modules=registered' \
  'grammar_quality=youyuantuma:有原图吗' \
  'grammar_quality=youceshizhanghaoma:右侧是账号吗' \
  'grammar_quality=shujukushiyongdeshinagebanben:数据库使用的是哪个版本' \
  'grammar_quality=qingzaishiyici:请再试一次' \
  'grammar_quality=woyijingshoudaole:我已经收到了' \
  'synthetic_private_fixture=pass' \
  's:' 'sh:' 'shu:' 'shuru:' 'ceshi:' 'wendingxing:' \
  'lifecycle_sessions=128' \
  'accepted_quality_cases=18' \
  'holdout_case_count=20' \
  'cache_condition=process-cold-deployed-user-data-os-warm' \
  'grammar_quality_pass=true'; do
  grep -F "$expected" "$temporary/model.stdout" >/dev/null ||
    die "real Rime runtime probe omitted expected result: $expected"
done
for expected in \
  'mode=baseline' \
  'octagram_modules=registered' \
  'accepted_quality_cases=17' \
  'holdout_case_count=20' \
  'cache_condition=process-cold-deployed-user-data-os-warm' \
  'grammar_quality_pass=true'; do
  grep -F "$expected" "$temporary/baseline.stdout" >/dev/null ||
    die "no-grammar baseline omitted expected result: $expected"
done
for mode in baseline model; do
  [[ "$(grep -Ec '^holdout_case=[a-z0-9_]+:pass$' "$temporary/$mode.stdout")" -eq 20 ]] ||
    die "$mode runtime did not pass the frozen 20-case candidate stream"
  p95="$(sed -n 's/^final_key_candidate_p95_us=\([0-9][0-9]*\)$/\1/p' \
    "$temporary/$mode.stdout")"
  [[ "$p95" =~ ^[0-9]+$ && "$p95" -le 20000 ]] || {
    cat "$temporary/$mode.stdout" >&2
    die "$mode final-key plus candidate P95 exceeds 20 ms or is missing"
  }
done
if grep -E '^(s|sh|shu|shuru|ceshi|wendingxing):.* han=no$' "$temporary/model.stdout" >/dev/null; then
  cat "$temporary/model.stdout" >&2
  die "real Rime runtime returned an English-only public candidate page"
fi

model_schema="$model_user/build/rime_ice.schema.yaml"
baseline_schema="$baseline_user/build/rime_ice.schema.yaml"
built_schema="$model_schema"
[[ -f "$built_schema" && ! -L "$built_schema" ]] ||
  die "real Rime maintenance did not build rime_ice.schema.yaml"
[[ -f "$baseline_schema" && ! -L "$baseline_schema" ]] ||
  die "no-grammar baseline did not build rime_ice.schema.yaml"
grammar_model_name="$(read_lock_value grammarModel.name)"
for expected_schema in \
  'grammar:' \
  "  language: \"$grammar_model_name\"" \
  '  collocation_max_length: 6' \
  '  collocation_min_length: 3' \
  '  contextual_suggestions: true' \
  '  enable_correction: false' \
  '  max_homophones: 8' \
  '  long_correction_guard: true' \
  '  short_input_guard: true' \
  '  typo_correction: false'; do
  grep -F "$expected_schema" "$built_schema" >/dev/null ||
    die "built rime_ice schema omitted grammar/safety setting: $expected_schema"
done
for expected_schema_pattern in \
  '^  collocation_penalty:[[:space:]]+(-14|"-14")[[:space:]]*$' \
  '^  non_collocation_penalty:[[:space:]]+(-6|"-6")[[:space:]]*$' \
  '^  weak_collocation_penalty:[[:space:]]+(-100|"-100")[[:space:]]*$' \
  '^  rear_penalty:[[:space:]]+(-20|"-20")[[:space:]]*$'; do
  grep -Eq "$expected_schema_pattern" "$built_schema" >/dev/null ||
    die "built rime_ice schema omitted grammar penalty: $expected_schema_pattern"
done
if grep -F 'max_homographs:' "$built_schema" >/dev/null; then
  die "built Rime Ice schema contains ineffective max_homographs"
fi
grep -F '  contextual_suggestions: false' "$baseline_schema" >/dev/null ||
  die "baseline schema did not disable contextual suggestions"
if grep -Eq '^grammar:|^[[:space:]]+language:[[:space:]]+wanxiang-' "$baseline_schema"; then
  die "baseline schema still contains grammar model configuration"
fi
for expected_schema in \
  '  enable_correction: false' \
  '  long_correction_guard: true' \
  '  short_input_guard: true' \
  '  typo_correction: false'; do
  grep -F "$expected_schema" "$baseline_schema" >/dev/null ||
    die "baseline schema changed a non-grammar safety setting: $expected_schema"
done

/usr/bin/python3 - "$metrics_output" "$app" "$REPO_ROOT" \
  "$MACOS_DIR/dependencies.lock.json" \
  "$temporary/prepare-baseline.stdout" "$temporary/prepare-model.stdout" \
  "$temporary/baseline.stdout" "$temporary/model.stdout" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys

output_path = Path(sys.argv[1]).resolve()
app = Path(sys.argv[2]).resolve()
repo = Path(sys.argv[3]).resolve()
lock = json.loads(Path(sys.argv[4]).read_text(encoding="utf-8"))
if output_path == app or app in output_path.parents:
    raise SystemExit("grammar metrics must remain outside the signed app payload")
metric_names = {
    "initializeMicroseconds": "initialize_us",
    "schemaSelectMicroseconds": "schema_select_us",
    "firstCompleteInputMicroseconds": "first_complete_input_us",
    "rssAfterInitializeBytes": "rss_after_initialize_bytes",
    "rssAfterSchemaSelectBytes": "rss_after_schema_select_bytes",
    "rssAfterFirstInputBytes": "rss_after_first_input_bytes",
    "rssAfterHoldoutBytes": "rss_after_holdout_bytes",
    "measurementMaxRssBytes": "measurement_max_rss_bytes",
    "finalKeyCandidateP95Microseconds": "final_key_candidate_p95_us",
    "measurementProcessElapsedMicroseconds": "measurement_process_elapsed_us",
}
deployment_metric_names = {
    "elapsedMicroseconds": "deployment_phase_elapsed_us",
    "peakRssBytes": "deployment_phase_peak_rss_bytes",
}

def read_metrics(path: str, names: dict[str, str]) -> dict[str, int]:
    rows: dict[str, list[str]] = {}
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            rows.setdefault(key, []).append(value)
    result: dict[str, int] = {}
    for output_name, source_name in names.items():
        values = rows.get(source_name, [])
        if len(values) != 1 or not values[0].isdigit():
            raise SystemExit(f"{path}: missing numeric {source_name}")
        result[output_name] = int(values[0])
        if result[output_name] <= 0:
            raise SystemExit(f"{path}: non-positive numeric {source_name}")
    return result

baseline_deployment = read_metrics(sys.argv[5], deployment_metric_names)
model_deployment = read_metrics(sys.argv[6], deployment_metric_names)
baseline = read_metrics(sys.argv[7], metric_names)
model = read_metrics(sys.argv[8], metric_names)
if baseline["finalKeyCandidateP95Microseconds"] > 20000:
    raise SystemExit("baseline P95 exceeded 20 ms")
if model["finalKeyCandidateP95Microseconds"] > 20000:
    raise SystemExit("model P95 exceeded 20 ms")
deltas = {key: model[key] - baseline[key] for key in metric_names}
stage_deltas = {
    "initialize": deltas["rssAfterInitializeBytes"],
    "schema-select": (
        deltas["rssAfterSchemaSelectBytes"]
        - deltas["rssAfterInitializeBytes"]
    ),
    "first-input": (
        deltas["rssAfterFirstInputBytes"]
        - deltas["rssAfterSchemaSelectBytes"]
    ),
    "holdout": (
        deltas["rssAfterHoldoutBytes"]
        - deltas["rssAfterFirstInputBytes"]
    ),
}
largest_resident_growth_stage = max(stage_deltas, key=stage_deltas.get)
if stage_deltas[largest_resident_growth_stage] <= 0:
    raise SystemExit("A/B RSS evidence has no positive model resident growth")
load_stage_evidence = {
    "modelFileOpenObservedStage": "schema-select-before-first-input",
    "largestResidentGrowthStage": largest_resident_growth_stage,
    "modelMinusBaselineRssAfterInitializeBytes": stage_deltas["initialize"],
    "modelMinusBaselineRssIncreaseAtSchemaSelectBytes": stage_deltas[
        "schema-select"
    ],
    "modelMinusBaselineRssIncreaseAtFirstInputBytes": stage_deltas[
        "first-input"
    ],
    "modelMinusBaselineRssIncreaseAtHoldoutBytes": stage_deltas["holdout"],
    "modelMinusBaselineSchemaSelectMicroseconds": deltas[
        "schemaSelectMicroseconds"
    ],
    "firstInputLatencyDeltaMicroseconds": deltas[
        "firstCompleteInputMicroseconds"
    ],
    "modelFirstInputExceeds20ms": model["firstCompleteInputMicroseconds"] > 20000,
}

def identity(path: Path) -> dict[str, object]:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return {
        "path": path.relative_to(app).as_posix(),
        "size": path.stat().st_size,
        "sha256": digest.hexdigest(),
    }

repository_commit = subprocess.run(
    ["git", "-C", str(repo), "rev-parse", "HEAD"],
    check=True,
    text=True,
    stdout=subprocess.PIPE,
).stdout.strip()
if len(repository_commit) != 40:
    raise SystemExit("could not bind grammar evidence to a repository commit")
document = {
    "schemaVersion": 1,
    "repositoryCommit": repository_commit,
    "platform": "macos",
    "packagedArchitectures": ["arm64", "x86_64"],
    "probeArchitecture": platform.machine(),
    "bundleIdentifier": "io.github.kukuyan.inputmethod.YunPin",
    "grammarModel": lock["grammarModel"],
    "runtimeIdentity": {
        "librime": identity(app / "Contents/Frameworks/librime.1.dylib"),
        "octagram": identity(
            app / "Contents/Frameworks/rime-plugins/librime-octagram.dylib"
        ),
        "executable": identity(app / "Contents/MacOS/YunPin"),
    },
    "qualityComparison": {
    "headlessRimeIce": True,
    "cacheCondition": "process-cold-deployed-user-data-os-warm",
    "comparisonOrder": ["baseline", "model"],
    "deploymentPhase": {
        "cacheCondition": "isolated-deployment-process-os-warm",
        "processIsolation": "separate-prepare-process",
        "baseline": baseline_deployment,
        "model": model_deployment,
    },
    "measurementPhase": {
        "processIsolation": "fresh-process-after-deployment",
        "maintenanceInvoked": False,
    },
    "holdoutCaseCount": 20,
    "acceptedQualityCases": {"baseline": 17, "model": 18},
    "gateMicroseconds": 20000,
    "syntheticPrivateCounterfactual": True,
    "baseline": baseline,
    "model": model,
    "modelMinusBaseline": deltas,
    "loadStageEvidence": load_stage_evidence,
    },
}
output_path.parent.mkdir(parents=True, exist_ok=True)
temporary = output_path.with_name(output_path.name + ".tmp")
temporary.write_text(
    json.dumps(document, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)
os.replace(temporary, output_path)
PY

printf '%s\n' '--- no-grammar baseline deployment (isolated process) ---'
cat "$temporary/prepare-baseline.stdout"
printf '%s\n' '--- Wanxiang model deployment (isolated process) ---'
cat "$temporary/prepare-model.stdout"
printf '%s\n' '--- no-grammar baseline (process-cold, deployed data, OS-warm) ---'
cat "$temporary/baseline.stdout"
printf '%s\n' '--- Wanxiang model (process-cold, deployed data, OS-warm) ---'
cat "$temporary/model.stdout"
printf '%s\n' '--- synthetic private fixture removed counterfactual ---'
cat "$temporary/private-off.stdout"
printf 'verified isolated deployment, packaged A/B load stage/RSS, 20-case ranking, <=20 ms P95, private on/off counterfactual, plugins and 128 session lifecycles\n'
