# License Matrix

| Area | License | Notes |
|---|---|---|
| `engine/`, `localstore/`, `sync/`, `syncclient/`, `desktopagent/`, `integration/`, `tools/`, `protocol/`, root scripts and docs | Apache-2.0 | Original YunPin work unless a file states otherwise. |
| `platform/patches/weasel/`, `platform/windows/`, and Windows derivative distribution | GPL-3.0-only | The preview preserves the exact Weasel base, ordered patches, runtime notices, and corresponding-source archive. Production still requires Authenticode and native-host acceptance. |
| `platform/patches/squirrel/`, `platform/macos/`, macOS frontend overlays and derivative distribution | GPL-3.0-only | The development preview preserves the exact Squirrel base, ordered patches, source archive and notices. Production distribution still requires native-plugin and host acceptance. |
| `platform/ios/` | Apache-2.0-compatible only | Phase two; must not copy or link GPL desktop frontend code. |
| `rime/librime` | BSD-3-Clause | Pinned upstream dependency. |
| Rime Ice | GPL-3.0 | Distributed data/build outputs must comply with its license. |
| Rime Essay | LGPL-3.0 | Preserve license and source/modification notices. |
| THUOCL | MIT | Preserve copyright and license notice. |
| phrase-pinyin-data | MIT | Preserve copyright and license notice. |
| ImeWlConverter | GPL-3.0 | Separate offline converter process; not linked into Apache components. |
| Sparkle 2.6.2 in the macOS preview | MIT | Squirrel dependency; exact archive hash is locked, automatic checks and the upstream feed are disabled. |
| Boost 1.84.0 in the Windows native build | BSL-1.0 | The Windows lock records its official source archive URL/SHA-256; its source and license are bundled. |
| Boost 1.89.0 in the macOS native build | BSL-1.0 | The macOS source archive URL and SHA-256 are locked and its license is bundled. |
| Squirrel preset Rime data packages | LGPL-3.0 | Exact Bopomofo, Cangjie, Essay, Luna Pinyin, Prelude, Stroke and Terra Pinyin sources are locked and included in the macOS corresponding-source archive. |

The root `LICENSE` applies only where no narrower license is declared. A desktop binary combining Apache code with GPL frontend code is distributed under GPL-3.0-compatible terms. The iOS implementation must remain independent from GPL desktop code.

Every distinct external Go `module@version`, including go.mod-only checksum
rows retained in any of the six `go.sum` files, is listed in
`third_party/go-modules.lock.json` with a reviewed SPDX identifier and a
versioned license source. `scripts/check_licenses.py` requires exact coverage
and rejects stale rows, unknown licenses, uncovered requirements, and local
replacement drift. CI never infers dependency licenses from the network.
