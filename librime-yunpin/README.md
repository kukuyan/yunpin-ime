# librime-yunpin

`librime-yunpin` is the Apache-2.0 adapter between the immutable YunPin phrase
index and librime. It registers `yunpin_filter`, which wraps the already merged
Rime translation, injects at most two personal candidates, and removes an
upstream duplicate before `uniquifier` runs.

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
  # Reserved for a future typed/armed native action channel. The development
  # preview keeps this false and ignores true values; it injects no action
  # candidates and performs no browser or favorite-file side effect.
  expression_search: false
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

At repository root the directory builds three test targets and no plugin: the
snapshot/parser tests, the session-learning state-machine tests, and a
candidate-ordering test that compiles the real
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
