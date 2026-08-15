# Third-party notices

YunPin records the following upstreams as commit-pinned Git submodules. A normal clone fetches their contents only when submodules are initialized.

- [librime](https://github.com/rime/librime), BSD-3-Clause.
- [Weasel](https://github.com/rime/weasel), GPL-3.0.
- [Squirrel](https://github.com/rime/squirrel), GPL-3.0.
- [Rime Ice](https://github.com/iDvel/rime-ice), GPL-3.0.
- [Rime Essay](https://github.com/rime/rime-essay), LGPL-3.0.
- [THUOCL](https://github.com/thunlp/THUOCL), MIT.
- [phrase-pinyin-data](https://github.com/mozillazg/phrase-pinyin-data), MIT.
- [ImeWlConverter](https://github.com/studyzy/imewlconverter), GPL-3.0.

The macOS development preview also embeds Squirrel's pinned universal librime
1.16.0 runtime (BSD-3-Clause) and Sparkle 2.6.2 framework (MIT), and rebuilds
librime with Boost 1.89.0 headers (BSL-1.0). Their source/release archive URLs
and verified SHA-256 values are recorded in
`platform/macos/dependencies.lock.json`; the matching Squirrel nested gitlinks
are checked by the macOS tests. Sparkle's upstream update feed is disabled in
YunPin builds.

The same preview rebuilds the external librime-lua (BSD-3-Clause),
librime-octagram (GPL-3.0-only), and librime-predict (BSD-3-Clause) plugins
from their locked upstream commits with the same compiler and SDK as the
bundled librime. librime-lua embeds Lua 5.4.8 (MIT). The exact source archive
URLs, commit IDs, and SHA-256 values are recorded in the macOS dependency
lock; corresponding source and the complete upstream license texts are
retained in the source and application artifacts.

The Windows development preview is built from Weasel 0.17.4 and librime 1.17.0
at the commits recorded in `platform/windows/dependencies.lock.json`. Its
ordered GPL patch series, nested librime dependency commits, official Boost
1.84.0 source archive SHA-256, runtime license bundle, and exact corresponding-
source archive are part of the reproducible package process. WinSparkle is not
linked or distributed in the YunPin runtime, and all upstream update calls are
disabled.

Squirrel's bundled Bopomofo, Cangjie, Essay, Luna Pinyin, Prelude, Stroke and
Terra Pinyin data packages are LGPL-3.0. The macOS lock records an exact source
commit and archive hash for each package, and the development source archive
retains their complete extracted sources and license texts.

Fetching or packaging an upstream requires retaining its exact LICENSE/NOTICE files and recording modifications. No Sogou proprietary dictionary is included or redistributed.

The Go components also compile direct dependencies including
[fxamacker/cbor](https://github.com/fxamacker/cbor) (MIT),
[golang.org/x/crypto](https://go.googlesource.com/crypto) (BSD-3-Clause),
[golang.org/x/text](https://go.googlesource.com/text) (BSD-3-Clause), and
[modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (BSD-3-Clause), plus their
transitive dependencies. Exact versions and cryptographic checksums are locked
in each component's `go.mod` and `go.sum`; release artifacts must include an
SBOM and the license texts resolved from that locked module graph.

Container builds use the official Go Alpine image and Google's distroless
static Debian image at exact multi-platform manifest digests recorded in the
Dockerfiles. Their operating-system package notices must be retained in a
release SBOM.
