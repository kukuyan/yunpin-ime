# Third-party notices

YunPin records the following upstreams with immutable source identities. Most
are commit-pinned Git submodules fetched only when submodules are initialized;
librime-octagram is fetched as a commit-specific, SHA-256-pinned source archive
for both desktop build chains.

- [librime](https://github.com/rime/librime), BSD-3-Clause.
- [librime-octagram](https://github.com/lotem/librime-octagram), BSD-3-Clause.
- [Weasel](https://github.com/rime/weasel), GPL-3.0.
- [Squirrel](https://github.com/rime/squirrel), GPL-3.0.
- [Rime Ice](https://github.com/iDvel/rime-ice), GPL-3.0.
- [Rime Essay](https://github.com/rime/rime-essay), LGPL-3.0.
- [THUOCL](https://github.com/thunlp/THUOCL), MIT.
- [phrase-pinyin-data](https://github.com/mozillazg/phrase-pinyin-data), MIT.
- [ImeWlConverter](https://github.com/studyzy/imewlconverter), GPL-3.0.

The macOS development preview embeds Squirrel's pinned universal librime
1.16.0 runtime (BSD-3-Clause) and rebuilds librime with Boost 1.89.0 headers
(BSL-1.0). Their source/release archive URLs and verified SHA-256 values are
recorded in `platform/macos/dependencies.lock.json`; the matching Squirrel
nested gitlinks are checked by the macOS tests. The Squirrel patch series
removes its upstream automatic-update framework and update entry points from
YunPin builds.

The same preview rebuilds the external librime-lua (BSD-3-Clause),
librime-octagram (BSD-3-Clause), and librime-predict (BSD-3-Clause) plugins
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

The Windows `rime.dll` statically merges librime-octagram from the same
commit-pinned, SHA-256-verified source archive used by macOS. The Windows build
verifies the upstream BSD-3-Clause license text, the multi-byte encoder fix,
the x86 and x64 generated object projects, and headless C-API registration of
the `octagram` and `grammar` modules. The runtime carries the complete license
text, while the corresponding-source archive carries the verified source
archive. No language model or user data is part of this plugin source pin.

Squirrel's bundled Bopomofo, Cangjie, Essay, Luna Pinyin, Prelude, Stroke and
Terra Pinyin data packages are LGPL-3.0. The macOS lock records an exact source
commit and archive hash for each package, and the development source archive
retains their complete extracted sources and license texts.

Both desktop previews bundle the full `wanxiang-lts-zh-hans` grammar model
selected by Rime Ice's grammar recipe, from
[RIME-LMDG](https://github.com/amzxyz/RIME-LMDG), under CC-BY-4.0. Upstream's
`LTS` release is mutable, so the identical platform locks say `immutable:
false` and bind the observed tag ref, source snapshot, GitHub asset ID/update
time, exact filename, byte size and SHA-256. They also bind the snapshot-scoped
license URL, size and SHA-256. The source snapshot is an independently observed
snapshot near the asset update, not a GitHub API assertion that the asset was
built from that commit. The model is downloaded only by that exact
filename and is never committed to this repository; runtime and
corresponding-source artifacts retain the verified model and complete license.

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
