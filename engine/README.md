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
7. an automatically learned phrase stays ineligible until its second use;
8. tombstones suppress deleted phrases before ranking.

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

Tombstone visibility uses one lock-free atomic bit per entry, so deletion can
race safely with queries without adding a mutex to the hot path. Platform
adapters remain responsible for swapping rebuilt indexes after settings or sync
updates and for eventually compacting tombstoned entries.

## Build and test

No CMake installation is required for this development slice:

```sh
make -C engine test
make -C engine benchmark
```

Windows CI uses the equivalent portable CMake build and MSVC tests.

The benchmark constructs exactly 50,000 synthetic entries, warms the index, and
fails if measured lookup P95 exceeds 20 ms. Timing depends on the host, so CI
records the measured value alongside pass/fail status.
