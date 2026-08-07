# YunPin public dictionary builder

This Apache-2.0 Python 3.9 tool deterministically builds the public YunPin Rime
dictionary from four already checked-out sources. It never fetches, updates or
queries the network.

Required local Git checkouts:

- Rime Ice: candidate text, pinyin and tuned weights;
- THUOCL: public domain-frequency candidates;
- Rime Essay: public general-frequency candidates;
- phrase-pinyin-data: exact phrase readings used as a resolver, not emitted as
  an independent candidate corpus.

Every checkout's full Git `HEAD` must exactly match
`third_party/upstreams.lock.json`, and the worktree must be clean, including no
untracked files. A matching HEAD with modified dictionary files is rejected.
The builder does not use tags, branches or remote state.

## Ranking and reading policy

Weights use non-overlapping deterministic bands:

| Source | Base weight | Reading |
| --- | ---: | --- |
| Rime Ice | 300,000,000 | supplied by the Rime row |
| THUOCL | 200,000,000 | exact phrase data, exact Rime phrase, then per-character map |
| Rime Essay | 100,000,000 | exact phrase data, exact Rime phrase, then per-character map |

The clamped original frequency is added to the base. Duplicate
`(phrase, pinyin)` rows retain the highest-priority weight; distinct readings
remain distinct. A THUOCL/Essay row without a complete offline reading is
reported in the manifest and omitted rather than guessed online.

The combined dictionary includes GPL-3.0 Rime Ice data, so the generated data
manifest declares the combined output as GPL-3.0 and retains the URL, commit,
license, input file path and SHA-256 for every source. Preserve all upstream
notices when redistributing it. Input hashes are calculated after UTF-8 BOM and
line-ending normalization, so clean Windows and Unix checkouts produce the same
manifest.

## Build

To write outside any Git repository:

```console
python3 -m yunpin_public_pack \
  --lock ../../third_party/upstreams.lock.json \
  --rime-ice-root ../../third_party/rime-ice \
  --rime-essay-root ../../third_party/rime-essay \
  --thuocl-root ../../third_party/THUOCL \
  --phrase-pinyin-root ../../third_party/phrase-pinyin-data \
  --output-dir /tmp/yunpin-public
```

Repository output requires the explicit `--build-dir` form and is accepted
only below that repository's top-level `build/` directory:

```console
python3 -m yunpin_public_pack ... --build-dir ../../build/public-pack
```

The two outputs are:

- `yunpin_public.dict.yaml`
- `yunpin_public.sources.json`

Existing generated files are not overwritten unless
`--replace-existing` is explicitly supplied. This switch can replace only
those two fixed public build filenames.

Run synthetic tests without bytecode artifacts:

```console
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -v
```
