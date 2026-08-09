# librime-yunpin

`librime-yunpin` is the Apache-2.0 adapter between the immutable YunPin phrase
index and librime. It registers `yunpin_filter`, which wraps the already merged
Rime translation, injects at most two personal candidates, and removes an
upstream duplicate before `uniquifier` runs. It also registers the isolated
`yunpin_corrector` used for bounded, deterministic local Pinyin typo recovery.

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
limited to 50,000 entries. Invalid private rows are counted without logging the
phrase or pinyin. If the file is absent or malformed, private injection stays
disabled; the filter remains available for the short-input upstream guard.

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

The Windows preview does not enable a real private snapshot until its TSF host
can set those options from a verified secure-input signal and the service IPC
has passed the local-user isolation gate.

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
  session_learning: true
  typo_correction: true
  # Reserved for a future typed/armed native action channel. The development
  # preview keeps this false and ignores true values; it injects no action
  # candidates and performs no browser or favorite-file side effect.
  expression_search: false

translator:
  enable_correction: true
  corrector_component: yunpin_corrector
```

`enabled` gates private snapshot loading and injection. `short_input_guard` and
`session_learning` are independent: Windows can keep private data and learning
disabled while still filtering an implausible three-character upstream
prediction for a one/two-letter input. Set all three false to make the filter
completely inert. `max_candidates` bounds the private phrases that may take
head slots.
`expression_search` is deliberately inert. The existing Rime commit boundary
provides plain text but no unforgeable proof that a commit came from an action
candidate. Until both native frontends provide a typed, explicitly armed
channel, ordinary, imported and synchronized text must always remain text.
`typo_correction: false` makes the selected component fall back to librime's
upstream `NearSearchCorrector`; set `translator/enable_correction: false` to
disable ScriptTranslator correction entirely.

## Deterministic Pinyin typo correction

`yunpin_corrector` is schema-local and stateless. For the leading lowercase
ASCII syllable at each ScriptTranslator graph position, it generates bounded
one-edit variants for:

- physical QWERTY neighbours, including diagonal `x` ↔ `s` for
  `shouxu...` → `shousu...`;
- one missing key, one extra key, or one adjacent transposition;
- the deliberately reviewed, one-way valid-syllable confusion `you` → `yao`.

Every generated spelling must exist in the deployed Prism before it is exposed
to ScriptTranslator. The portable generator never guesses Chinese text, opens
a dictionary, reads a file, calls a network service or invokes a model. It
rejects non-lowercase and adversarially long input, limits a syllable to six
bytes, emits at most 768 variants, and changes no more than one physical typing
action in a returned variant. A complete ScriptTranslator path may still use
one such correction at more than one syllable position, as the two-error
`shouxubijiakuaideshihou` golden demonstrates. The reviewed-confusion list is
intentionally small; adding a valid-syllable pair is a code-review decision,
not automatic learning. After Prism validation, the adapter keeps the best
variant per spelling ID, deterministically orders the survivors and exposes at
most 16 correction edges at any input offset. This is a syllable-graph budget,
not merely a candidate-window display limit.

Short exact input has an explicit regression boundary. The generator does not
run for a one-letter segment and never returns the unchanged leading spelling;
normal exact Prism matches remain non-correction paths. The real merged-Rime
fixture requires `xu` → `需` and `you` → `有` to remain first even while longer
context can recover `youjubei...` → `要具备...`. A prefix-collision fixture
types `shangban`: exact “上班” must remain before corrected “山班” while both
are present, even though “山班” has 50 times the dictionary weight. This
protects the tested exact cases, but is not a proof that every exact entry in a
production dictionary will outrank every corrected high-frequency phrase.

Upstream ScriptTranslator only knows whether a path is a correction. The
locked librime 1.16/1.17 implementation assigns every correction edge the same
credibility `log(0.01)`; YunPin's edit costs bound and choose variants but do
not become a fine-grained candidate-ranking penalty. Dictionary weights,
segmentation and context can therefore still affect final ordering. The real
Rime C API E2E uses a small synthetic dictionary and checks neighbour, missing,
extra, transposed, reviewed-confusion and two-error long inputs plus exact
controls. After 10 warmups it measures 100 final-key samples per corrected long
input and requires P95 no more than 20 ms. Two independent completed runs
measured 534–841 µs for the two-error `shouxubijiakuaideshihou` input and
907–1611 µs for the 37-byte reviewed `you` → `yao` input. This is a useful pipeline regression, not a
production Rime Ice or 50,000-personal-entry benchmark.

Selecting the component needs a minimal version-locked librime compatibility
patch. It adds `translator/corrector_component` and keys exact-match identity by
both spelling ID and consumed input length. The latter ensures a corrected
`shan` that consumes the extra `g` in `shang` is still marked as a correction
instead of inheriting an exact shorter-prefix match. macOS pins the patch to
librime 1.16 and Windows to librime 1.17; each dependency manifest locks the
patch path and SHA-256, and the build verifies it applies to the recorded
upstream commit. YunPin registers only `yunpin_corrector` and never replaces
the process-global `corrector`, so unrelated schemas retain upstream behavior.

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

At repository root the directory builds four test targets and no plugin: the
snapshot/parser tests, the session-learning state-machine tests, the portable
typo-variant tests, and a candidate-ordering test that compiles the real
`src/rime_yunpin_filter.cpp` against `tests/rime_stubs/`. Those stubs declare
only the slice of the librime API the filter touches, so ordering regressions
are caught on a machine without librime, Boost or glog. They are pinned to the
librime commit in `platform/upstream-lock.json`; re-check them against the real
headers whenever that moves.

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
