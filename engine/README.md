<!-- SPDX-License-Identifier: Apache-2.0 -->

# YunPin phrase engine

This directory contains the portable C++17 hot-path phrase recall and ranking
core. It is intentionally independent of networking, databases, platform UI,
and librime. Weasel and Squirrel adapters can build a new `PhraseIndex` off the
key path, then atomically replace the shared immutable index.

Implemented policy:

1. an exact full-pinyin match expresses the strongest input intent;
2. manually pinned personal phrases precede other sources;
3. personal, migrated, and locally extracted history phrases are ranked by
   device-merged usage count and then static weight;
4. reviewed public phrases precede the Rime base layer;
5. the first eight candidates contain no more than two personal items;
6. a pinned phrase with four or more syllables is treated as long and recalled
   after two complete full-pinyin syllables or four initials; two- and
   three-syllable pins retain ordinary two-initial recall;
7. non-pinned phrases with three or more syllables require two complete
   full-pinyin syllables and are not injected by short initials (`he` therefore
   cannot inject `合并为`); a single incomplete letter also cannot inject a
   multi-syllable personal/history/import phrase;
8. an automatically learned phrase stays ineligible until its second use;
9. tombstones suppress deleted phrases before ranking.

Input lookup uses sorted full-pinyin and initials indexes. A query allocates only
for its matching range and never waits for a filesystem or network operation.
Pinyin separators are optional; ASCII `u:` is normalized to `v`.

`PinyinSegmenter` returns bounded, deterministic complete-syllable paths. Thus
`xian` can be interpreted as both `xian` and `xi + an`, while `xi'an` retains the
explicit boundary. `FuzzyConfig` independently enables `zh/z`, `ch/c`, `sh/s`,
`n/l`, `en/eng`, and `in/ing`. Fuzzy aliases are dictionary-validated, capped at
64 even if a caller requests more, and never generated for one- or two-letter
input. Fuzzy matching is disabled by default and can be enabled with
`FuzzyConfig::Common()`; literal exact spelling always outranks an alias.

Tombstone visibility uses one lock-free atomic bit per entry. Explicit
correction scores and the candidate revision use bounded, statically verified
lock-free 32-bit atomics. Deletion or feedback advances the revision, so a host
can reject a candidate menu cached before the update. Platform adapters remain
responsible for swapping rebuilt indexes after settings or sync updates and for
eventually compacting tombstoned entries.

## Correction learning and local report boundary

`CorrectionLearning` recognizes only the immediate sequence “commit one entry,
undo/delete that just-committed entry, choose a different entry.” It emits `-1`
for the old entry, `+1` for the replacement, and `requires_requery=true`.
Applying those deltas through `PhraseIndex::ApplyCorrectionFeedback` changes the
revision and prevents the rest of a composition from reusing its old candidate
cache. Any unrelated edit must call `BreakAdjacency()`.

The monitor model stores only date bucket, entry ID, phrase, normalized pinyin,
selection count, corrected-from count, and replacement count. Its API has no
surrounding sentence/application/window field. Password, private, one-shot,
host-opted-out, URL/email/path-like, credential-like, control-character, and
oversized events are not recorded. The in-memory map is keyed for average O(1)
event aggregation and rejects new keys after 50,000 word/date aggregates while
continuing to update existing keys; reporting is intentionally off the key
path.

`yunpin_habit_report` is a minimal local report renderer. It accepts an
explicitly requested aggregate stream on standard input and prints word-level
rows without entry IDs. It never opens a file, database, or network connection.
The TSV helpers are an on-demand plaintext report interchange, **not** a
persistence format. `librime-yunpin` now maps a narrow, fail-closed notifier
sequence into this core for in-process session reranking. Production
persistence must still be supplied by the encrypted `localstore`; an installed
monitor UI/CLI and encrypted report producer are not connected, so no client
should claim cross-restart habit monitoring yet.

## Build and test

No CMake installation is required for this development slice:

```sh
make -C engine test
make -C engine benchmark
./engine/build/yunpin_habit_report --help
```

Windows CI uses the equivalent portable CMake build and MSVC tests.

The benchmark constructs exactly 100,000 synthetic entries, warms the index, and
fails if measured lookup P95 exceeds 20 ms. Timing depends on the host, so CI
records the measured value alongside pass/fail status.
