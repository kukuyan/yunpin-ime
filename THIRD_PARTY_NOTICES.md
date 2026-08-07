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
