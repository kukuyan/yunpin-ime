# Pinned upstreams

This directory combines Git submodule pointers with a human/auditable JSON lock. The gitlink SHA and `upstreams.lock.json` must agree. Initialize only when building or refreshing language data:

```bash
git submodule update --init --depth 1
```

Submodules are never contacted from the input hot path. The scheduled update workflow proposes new SHAs in a review pull request; it does not publish or activate a dictionary automatically.

Every source bundle or binary distribution that includes a submodule must preserve that upstream's LICENSE/NOTICE and corresponding-source obligations. See `THIRD_PARTY_NOTICES.md` and `docs/LICENSE_MATRIX.md`.

`go-modules.lock.json` is the separate, human-reviewed license map for every
distinct `module@version` retained by `protocol/go.sum`, `localstore/go.sum`,
or `sync/go.sum`. It also records the local protocol replacement. CI compares
the map with all three modules exactly: a new, removed, or version-changed Go
module fails until its license is reviewed and the map is updated. The checker
does not query a proxy, package index, repository, or license classifier.
