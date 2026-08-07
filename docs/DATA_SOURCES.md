# Data Sources

YunPin never performs an online query while processing a key event. Public language data is built offline from reviewed, version-locked sources in `third_party/upstreams.lock.json`.

| Source | Purpose | License |
|---|---|---|
| [Rime Ice](https://github.com/iDvel/rime-ice) | Modern Chinese dictionary/schema and frequency data | GPL-3.0 |
| [Rime Essay](https://github.com/rime/rime-essay) | General phrase and language-model weights | LGPL-3.0 |
| [THUOCL](https://github.com/thunlp/THUOCL) | Domain vocabulary | MIT |
| [phrase-pinyin-data](https://github.com/mozillazg/phrase-pinyin-data) | Phrase-to-Pinyin readings | MIT |

The small `data/bootstrap/public_seed.tsv` is a project-authored smoke-test seed, not a replacement for the upstream packs. Generated full packs are reproducible build artifacts and must include upstream licenses.

Conversation-derived vocabulary is a separate private layer. It is extracted and reviewed locally, never committed, and never used to train or publish a public word pack. Sogou data is user-owned migration input only; no proprietary Sogou corpus ships with YunPin.

The scheduled update workflow changes only lock commits and opens a pull request. A maintainer must review source/license changes and regenerated ranking tests before merge.
