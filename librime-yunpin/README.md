# librime-yunpin

`librime-yunpin` is the Apache-2.0 adapter between the immutable YunPin phrase
index and librime. It registers `yunpin_filter`, which wraps the already merged
Rime translation, injects at most two personal candidates, and removes an
upstream duplicate before `uniquifier` runs. It also registers the isolated
`yunpin_corrector` used for bounded, deterministic local Pinyin typo recovery,
plus `yunpin_comment_filter` for the display-only candidate-Pinyin preference.

This is a development-preview bridge. It performs one bounded TSV load while
the schema components are initialized. Candidate queries and the conservative
session-learning state operate only in memory; encrypted SQLite refresh and
background snapshot notifications are not connected yet.

## Private snapshot

The filter looks for `yunpin/private.tsv` below the frontend's isolated user
data directory. The file is never bundled. It uses the importer columns:

```text
phrase<TAB>pinyin<TAB>source<TAB>use_count[<TAB>pinned]
```

`pinned` is optional and accepts `1`, `true`, `yes`, or `pinned`. A snapshot is
backward compatible with older desktop builds: newly synchronized rows may use
`synced_learning@<UTC-day>` as their source so current builds can rank a recent
first selection ahead of stale frequency while older builds still treat it as
an ordinary personal source. An optional sixth `last_used_day` column is also
accepted by the shared/mobile loader.

A snapshot is
limited to 100,000 entries so the reviewed R0W Sogou vocabulary fits in one
fully searchable index instead of a lossy hot/cold split. Invalid private rows
are counted without logging the phrase or pinyin. If the file is absent or
malformed, private injection stays disabled; the filter remains available for
the short-input upstream guard.

Some long-lived Sogou personal rows use explicitly separated Latin letters or
a small reviewed legacy spelling set instead of standard syllables. Those rows
are marked private exact-code-only: they live in a separate index and can be
recalled only when the complete literal code is entered. They never participate
in full-Pinyin prefixes, initials prefixes, fuzzy aliases, public dictionaries
or typo correction. Unknown pseudo-syllables and one-letter private codes are
rejected.

For a normalized one- or two-letter pinyin input, the merged translation skips
only upstream candidates that are valid UTF-8, consist entirely of CJK
ideographs, and contain at least three Unicode scalars. Thus `he` cannot expose
`合并为`, while one/two-character words, English or mixed text, malformed data,
and every input of three or more letters retain the upstream order. This guard
does not perform a network or disk lookup.

These session options suppress filtering and learning immediately:

- `yunpin_private_mode`
- `password_mode`
- `incognito_mode`
- `yunpin_one_shot`, `one_shot_mode`, or `one_time_input`

Filtering and learning additionally require the explicit
`yunpin_learning_allowed` session option. The current Squirrel and Weasel
patches set it when they establish an ordinary IME session, before applying
per-application overrides; a configured client can therefore turn it off.
The clean Windows public package still ships its private snapshot and session
learning switches disabled, while an existing user-selected overlay is a
separate deployment choice. The protected-mode options above always take
precedence once supplied by the host.

## Schema

Place the filter before `uniquifier`:

```yaml
engine:
  filters:
    - yunpin_filter@yunpin
    - uniquifier

yunpin:
  tag: abc
  snapshot: yunpin/private.tsv
  max_candidates: 2
  enabled: true
  short_input_guard: true
  long_correction_guard: true
  session_learning: true
  # Experimental only; shipped desktop overlays keep both switches false.
  typo_correction: false
  typo_reviewed_confusions: false
  # Reserved for a future typed/armed native action channel. The development
  # preview keeps this false and ignores true values; it injects no action
  # candidates and performs no browser or favorite-file side effect.
  expression_search: false

translator:
  enable_correction: false
  corrector_component: yunpin_corrector
```

`enabled` gates private snapshot loading and injection. `short_input_guard` and
`long_correction_guard` and `session_learning` are independent: Windows can
keep private data and learning disabled while still filtering an implausible
three-character upstream prediction for a one/two-letter input. Set all four
filter switches false to make the filter completely inert. `max_candidates`
bounds the private phrases that may take head slots.
`expression_search` is deliberately inert. The existing Rime commit boundary
provides plain text but no unforgeable proof that a commit came from an action
candidate. Until both native frontends provide a typed, explicitly armed
channel, ordinary, imported and synchronized text must always remain text.
`typo_correction: false` makes `yunpin_corrector` a strict no-op; it never
falls back to librime's broader `NearSearchCorrector`. Both desktop overlays
also ship with `translator/enable_correction: false`. Enabling typo recovery
therefore requires an explicit experimental change to both switches.

## Candidate-Pinyin display option

`yunpin_comment_filter` lets the desktop schema expose a `拼音关 / 拼音开`
switch without altering candidate identity, order, quality, preedit or commit
text. It must be the last display filter immediately before `uniquifier`.
When `yunpin_show_candidate_pinyin` is absent or false, the filter masks only
full-width-bracket comments whose body is Latin Pinyin; pronunciation and
spelling notes containing Chinese remain visible. When the option is true, the
upstream translation is returned unchanged.

The mask is a flattened `ShadowCandidate` that points directly to the genuine
candidate. Existing emoji or conversion shadows are unwrapped first, while the
currently visible text, bounds, quality, preedit and correction flag are
preserved. Selection and learning therefore still resolve to the original
candidate. The schema keeps `translator/keep_comments: true` and retains the
frontend `[comment]` placeholder; this filter alone decides whether Pinyin is
visible.

```yaml
switches:
  - name: yunpin_show_candidate_pinyin
    states: [拼音关, 拼音开]

engine:
  filters:
    - yunpin_comment_filter@yunpin_comment_visibility
    - uniquifier
```

The switch has no `reset`, so a previously unseen option defaults to false and
can be listed in `switcher/save_options`. Frontends still define whether that
preference is scoped to an app, text field or broader session.

## Deterministic Pinyin typo correction

`yunpin_corrector` is schema-local and stateless. For the leading lowercase
ASCII syllable at each ScriptTranslator graph position, it generates bounded
one-edit variants for:

- physical QWERTY neighbours, including diagonal `x` ↔ `s` for
  `shouxu...` → `shousu...`;
- one missing key, one extra key, or one adjacent transposition;
- an optional reviewed, one-way valid-syllable confusion such as `you` →
  `yao`, disabled by default behind `typo_reviewed_confusions`.

Every generated spelling must exist in the deployed Prism before it is exposed
to ScriptTranslator. The portable generator never guesses Chinese text, opens
a dictionary, reads a file, calls a network service or invokes a model. It
rejects non-lowercase and adversarially long input, limits a syllable to six
bytes, emits at most 768 variants, and changes no more than one physical typing
action in a returned variant.

The version-locked librime patch first builds forward and reverse reachability
using only normal, non-correction Prism spellings. Completion, fuzzy and
abbreviation spellings do not count as exact. If those transitions already
segment the complete composition, the corrector is not called anywhere in the
graph. Otherwise, a correction may survive only when its start is reachable by
an exact prefix and its end reaches the input boundary through an exact suffix.
Only one graph offset may add such bridge edges, so every surviving path has at
most one physical edit. Inputs containing two invalid regions therefore fail
closed instead of combining speculative edits. Leading/trailing delimiters use
the semantics of the locked librime version.

Experimental analysis is limited to inputs shorter than 128 bytes and to 32
read-only `ToleranceSearch` attempts per complete graph build. Exhausting either
budget yields no correction. The generator does not run for a one-letter
segment and never returns the unchanged leading spelling. The reviewed-
confusion list is intentionally small and default-off; enabling or adding a
valid-syllable pair is a separately reviewed experiment, not automatic
learning. After Prism validation, the adapter keeps the best variant per
spelling ID, deterministically orders survivors and exposes at most 16 bridge
edges at the selected offset. This is a syllable-graph budget, not merely a
candidate-window display limit.

The experimental bridge search can still inspect several exact-prefix offsets
before finding a late error. A measured tail-extra-key case required 13 bounded
variant attempts; selecting the bridge offset more directly remains a P1
optimization. The shipped defaults avoid that work entirely: with
`translator/enable_correction: false` no corrector is constructed, so the
forward/reverse analysis and all `ToleranceSearch` calls are skipped.

For a long composition, `long_correction_guard` adds a second fail-closed
boundary at candidate display time. Personal candidates remain at the head and
an ordinary upstream candidate must precede recovery. At most one correction
may appear, only at total rank #2 or #3. If no ordinary candidate is available,
or two personal candidates already occupy the safe slots, correction is hidden.
All additional corrections—including any correction beyond the first eight
total candidates—are discarded rather than moved to a later page.

Upstream ScriptTranslator only knows whether a path is a correction. The
locked librime 1.16/1.17 implementation assigns every correction edge the same
credibility `log(0.01)`; YunPin's edit costs bound and choose variants but do
not become a fine-grained candidate-ranking penalty. Dictionary weights,
segmentation and context can therefore still affect final ordering after the
feature is explicitly enabled. Earlier native fixtures that expected two
corrections in one sentence or default `you` → `yao` recovery describe the
retired aggressive policy and are not acceptance evidence for this conservative
revision; a future experimental native build must be re-baselined separately.

Selecting the component needs a minimal version-locked librime compatibility
patch. It adds `translator/corrector_component`, tracks exact identity by both
spelling ID and consumed input length, and applies the exact-prefix/suffix
one-edge bridge rule above. macOS pins the patch to librime 1.16 and Windows to
librime 1.17; each dependency manifest locks the patch path and SHA-256, and the
build verifies it applies to the recorded upstream commit. YunPin registers
only `yunpin_corrector` and never replaces the process-global `corrector`, so
unrelated schemas retain upstream behavior.

The patch adds a correction flag to librime's `Candidate` object and therefore
defines a YunPin-specific native ABI. All native librime, frontend and plugin
components must be rebuilt from the same patched source. Do not mix this build
with stock or third-party precompiled librime modules; avoiding the base-class
layout change is a longer-term compatibility improvement.

`youceshizhanghaoma` illustrates a different problem: “右侧是账号吗” and
“右侧市长好吗” can both arise from valid exact Pinyin segmentation. This is a
language-model, phrase-frequency and personal-learning ranking problem, not a
spelling-edit problem. The conservative corrector must not claim to solve it;
personal phrase learning or a separately bounded exact-path reranker is the
appropriate future layer.

A learned local model is not part of this preview. A future experiment may use
an optional, default-off process sidecar, but it must have no network access,
receive only the minimum bounded composition, use a strict IPC deadline and
fail closed to the deterministic/exact path on timeout, crash or invalid
output. Chinese-English mixed-input segmentation and ranking are likewise a
future direction, not current behavior.

## Session correction learning

The filter connects to librime's commit, context-update, unhandled-key, option,
and delete notifiers. It learns only the narrow sequence it can prove locally:
a single word candidate is committed, exactly one unmodified Backspace arrives
for each valid UTF-8 Unicode scalar, and a different word from the same
normalized pinyin is committed within five seconds. A normal Rime trailing
zero-length composition placeholder is accepted; multiple non-empty segments,
private `yunpin` candidates, `sentence` and unknown candidate types,
modified/extra keys, changed pinyin, aborted composition, timeout, or a
sensitive option break the chain.

A completed correction gives the old word `-1` and the replacement `+1`. Only
the first eight ordinary upstream candidates are prefetched and stable-sorted;
private candidates remain at the head, duplicates and the short-input guard
retain their existing behavior, and the remainder stays in upstream order.
Both habit aggregates and correction scores reject new keys after 50,000 while
allowing existing keys to update.

`YunPinFilter::QueryHabits()` exposes local-date bucket, word, normalized
pinyin, selection count, corrected-from count, and replacement count. It does
not retain a surrounding sentence, application, window, or raw composition,
and does no file or network I/O. This API is currently in-memory only: a native
monitor UI/CLI bridge and encrypted localstore persistence are still required
before installed desktop clients can display history across restarts.

## Build modes

At repository root the directory builds seven test targets and no plugin: the
snapshot/parser tests, a synthetic 100,000-row snapshot benchmark, the
session-learning state-machine tests, replay-ring tests, portable typo-variant
tests, candidate-ordering tests and candidate-comment visibility tests. The two
filter tests compile the production sources against `tests/rime_stubs/`. Those
stubs declare only the slice of the librime API the filters touch, so ordering,
identity and display regressions are caught on a machine without librime,
Boost or glog. They are pinned to the librime commit in
`platform/upstream-lock.json`; re-check them against the real headers whenever
that moves.

The benchmark uses generated public fixture values only. It requires all
100,000 rows to survive parsing and index replacement, parse plus build to
finish within 15 seconds, hot-query P95 to remain at or below 20 ms, and the
incremental peak working set to remain at or below 256 MiB. It prints the
measured parse, build, P95, maximum latency and resident-memory values on every
run. Ten thousand rows deliberately share one two-syllable prefix and that
high-collision lookup occupies more than five per cent of samples, so P95
cannot hide it. The broad load/memory ceilings are regression guards, not
typical targets.

```bash
cmake -S librime-yunpin -B build/librime-yunpin
cmake --build build/librime-yunpin --parallel
ctest --test-dir build/librime-yunpin --output-on-failure
```

Desktop packaging stages this directory below the corresponding frontend's
`librime/plugins/`, copies `engine/include` and `engine/src` into an `engine/`
subdirectory, and sets `RIME_PLUGINS=librime-yunpin`. The resulting module is
merged separately into Weasel's and Squirrel's librime build; a plugin binary
is not shared between platforms.
