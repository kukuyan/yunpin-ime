# License Matrix

| Area | License | Notes |
|---|---|---|
| `engine/`, `localstore/`, `sync/`, `integration/`, `tools/`, `protocol/`, root scripts and docs | Apache-2.0 | Original YunPin work unless a file states otherwise. |
| Future `platform/patches/weasel/` patch set and Windows derivative distribution | GPL-3.0-only | Must preserve Weasel source, notices and corresponding source obligations. No native patch ships in the bootstrap. |
| Future `platform/patches/squirrel/` patch set and macOS derivative distribution | GPL-3.0-only | Must preserve Squirrel source, notices and corresponding source obligations. No native patch ships in the bootstrap. |
| `platform/ios/` | Apache-2.0-compatible only | Phase two; must not copy or link GPL desktop frontend code. |
| `rime/librime` | BSD-3-Clause | Pinned upstream dependency. |
| Rime Ice | GPL-3.0 | Distributed data/build outputs must comply with its license. |
| Rime Essay | LGPL-3.0 | Preserve license and source/modification notices. |
| THUOCL | MIT | Preserve copyright and license notice. |
| phrase-pinyin-data | MIT | Preserve copyright and license notice. |
| ImeWlConverter | GPL-3.0 | Separate offline converter process; not linked into Apache components. |

The root `LICENSE` applies only where no narrower license is declared. A desktop binary combining Apache code with GPL frontend code is distributed under GPL-3.0-compatible terms. The iOS implementation must remain independent from GPL desktop code.

Every distinct external Go `module@version`, including go.mod-only checksum
rows retained in any of the four `go.sum` files, is listed in
`third_party/go-modules.lock.json` with a reviewed SPDX identifier and a
versioned license source. `scripts/check_licenses.py` requires exact coverage
and rejects stale rows, unknown licenses, uncovered requirements, and local
replacement drift. CI never infers dependency licenses from the network.
