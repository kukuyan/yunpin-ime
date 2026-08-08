# librime-yunpin

`librime-yunpin` is the Apache-2.0 adapter between the immutable YunPin phrase
index and librime. It registers `yunpin_filter`, which wraps the already merged
Rime translation, injects at most two personal candidates, and removes an
upstream duplicate before `uniquifier` runs.

This is a read-only development-preview bridge. It performs one bounded TSV
load while the schema components are initialized. `Query`, `Peek`, and `Next`
operate only on memory; learning, encrypted SQLite refresh, and background
snapshot notifications are not connected yet.

## Private snapshot

The filter looks for `yunpin/private.tsv` below the frontend's isolated user
data directory. The file is never bundled. It uses the importer columns:

```text
phrase<TAB>pinyin<TAB>source<TAB>use_count[<TAB>pinned]
```

`pinned` is optional and accepts `1`, `true`, `yes`, or `pinned`. A snapshot is
limited to 50,000 entries. Invalid private rows are counted without logging the
phrase or pinyin. If the file is absent or malformed, the filter stays disabled
and ordinary Rime input continues.

Three session options suppress the filter immediately:

- `yunpin_private_mode`
- `password_mode`
- `incognito_mode`

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
  # Optional expression search/favorite actions. Default false. When enabled
  # the two action candidates are placed in the trailing slots of the first
  # page, never at the head, so the space bar and keys 1-2 keep selecting real
  # candidates. `enabled: false` also forces this off.
  expression_search: false
```

`max_candidates` bounds the private phrases that may take head slots.
`expression_search` is independent of `snapshot`: a missing snapshot disables
only the private phrases, and a present one never switches the actions on.

## Build modes

At repository root, the directory builds only the snapshot/parser tests:

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
