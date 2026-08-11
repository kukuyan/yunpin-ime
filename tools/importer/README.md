# YunPin offline importer

This Apache-2.0 tool builds a filtered personal phrase TSV without making any
network request. It accepts Rime/text dictionaries, a ChatGPT
`conversations.json` export, Codex summary Markdown/JSONL, and copied Sogou
SCEL/BIN files through a separately downloaded ImeWlConverter.

It does not write chat records, complete messages, or arbitrary CJK fragments.
Chat history is processed in memory; code blocks and lines containing URLs, IP
addresses, email addresses, file paths, credentials, tokens, or long numbers
are removed. A remaining fragment is eligible only when it is an exact entry
in a supplied local terminology dictionary or matches a narrow institution or
technical-entity rule. Sentence-like fragments are rejected before dictionary
lookup, so a reading assembled from individual characters cannot legitimize a
sentence. Only phrase, normalized pinyin, source class, and coarse use count are
written. Eligible entity phrases without a known reading remain visible as
`missing_pinyin` in preview; provide one or more local Rime dictionaries or
`{phrase}: {pinyin}` files from the pinned `phrase-pinyin-data` source with
`--pinyin-dict` to resolve them offline.

For an immediately usable history import, add `--require-pinyin`. Entries that
cannot be resolved locally are removed and counted as
`missing_pinyin_required` in the masked preview; no online lookup is attempted.

## Safety model

- Preview is the default. Phrase values are masked unless
  `--reveal-phrases` is deliberately added.
- Writing requires the exact flag `--confirm IMPORT`.
- The output path is rejected if it is inside any Git repository.
- Output uses an atomic no-overwrite publish and has only
`phrase`, `pinyin`, `source`, `use_count` columns.
- Success/error messages do not echo private source or destination paths.
- Assistant messages in ChatGPT exports are excluded unless
  `--include-assistant` is set.
- History terms must occur twice by default; counts are rounded up to powers of
  two. Exact local-dictionary terms and explicit institution/technical entities
  can be retained; unclassified prose is dropped regardless of length.

## Examples

Run from `tools/importer` or install the package into an isolated environment.
The examples deliberately place personal output outside the repository.

```console
python3 -m yunpin_importer import ~/Downloads/conversations.json \
  --kind chatgpt \
  --pinyin-dict /opt/yunpin-public/rime_ice.dict.yaml

python3 -m yunpin_importer import ~/Documents/codex-summaries \
  --kind codex \
  --pinyin-dict /opt/yunpin-public/rime_ice.dict.yaml \
  --require-pinyin \
  --reveal-phrases

python3 -m yunpin_importer import ~/Downloads/my-rime.dict.yaml \
  --kind rime \
  --confirm IMPORT \
  --output ~/.local/share/yunpin/imported.tsv
```

## Sogou SCEL/BIN bridge

YunPin does not contain or redistribute proprietary Sogou parsers or data.
The bridge invokes the GPL-3.0 ImeWlConverter v3.4.3 CLI as a separate process.
Download an official release asset listed in
[`platform/upstream-lock.json`](../../platform/upstream-lock.json), verify the
archive hash against that lock, extract it locally, and calculate the
executable/DLL hash. The wrapper pins that exact local executable for
repeatability; the separately verified release-archive hash is the provenance
check and cannot be inferred from the extracted executable hash alone:

```console
shasum -a 256 /opt/imewlconverter/ImeWlConverterCmd

python3 -m yunpin_importer sogou ~/Snapshots/sogou/user.bin \
  --source-format sgpybin \
  --source-sha256 SOURCE_HASH_FROM_SNAPSHOT \
  --converter /opt/imewlconverter/ImeWlConverterCmd \
  --converter-sha256 HASH_OF_EXACT_EXECUTABLE
```

Sogou exports can contain more rows than the bounded native index accepts. The
bridge therefore merges duplicates, sorts by descending mapped frequency and
retains at most 100,000 entries by default. `--max-sogou-phrases` may lower
that limit but can never raise it above the runtime's 100,000-entry hard cap.
The reviewed R0W result (about 94,000 merged entries) therefore remains intact
and fully searchable. The original snapshot remains the lossless recovery
source outside Git.

The first run previews only. Re-run with `--confirm IMPORT --output <outside
repo>` after reviewing it. The wrapper hashes the original before and after,
copies it to an isolated temporary directory, converts only the copy, and
deletes temporary conversion artifacts. Converter stdout/stderr is captured
and never echoed because it may contain personal words.

The converter remains a separate GPL program; no ImeWlConverter code is linked
or copied into this Apache-2.0 package. See
[`platform/LICENSE-BOUNDARIES.md`](../../platform/LICENSE-BOUNDARIES.md).
