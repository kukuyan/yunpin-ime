# License boundaries

YunPin is a mixed-license repository. This document is operational guidance,
not legal advice.

| Area | License | Boundary |
| --- | --- | --- |
| Windows frontend and its Weasel patch set | GPL-3.0 | Built from pinned Weasel source; complete corresponding source and patches must accompany a binary release. |
| macOS frontend and its Squirrel patch set | GPL-3.0 | Built from pinned Squirrel source; complete corresponding source and patches must accompany a binary release. |
| `librime-yunpin`, protocol, sync service and importer | Apache-2.0 | Original YunPin components. Their notices remain intact when included in a desktop distribution. |
| librime | BSD-3-Clause | Preserve the upstream copyright and license text. |
| ImeWlConverter | GPL-3.0 | Optional external executable. YunPin does not link, copy, or redistribute it; the importer invokes a user-supplied, hash-pinned binary. |
| Personal vocabulary and migrated output | User data, never repository content | Must stay outside Git, CI artifacts, fixtures, logs, images and releases. |

Desktop patches, packaging files and frontend-specific Rime overlays in this
directory are `GPL-3.0-only` to keep the distribution boundary unambiguous.
The standalone importer under `tools/importer` is `Apache-2.0` and communicates
with ImeWlConverter only through its documented command-line interface.

Before each release, generate a source offer/archive that contains the exact
upstream commits in `upstream-lock.json`, the applied patches, build scripts,
license texts, and the matching YunPin frontend source. Never place signing
keys, notarization credentials, personal dictionaries, or converted data in
that archive.
