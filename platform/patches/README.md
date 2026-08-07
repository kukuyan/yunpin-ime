# Upstream patch policy

Do not vendor Weasel, Squirrel, or their Git histories here. Build automation
must clone the repositories and verify the exact commit recorded in
`../upstream-lock.json` before applying patches.

Patch filenames are ordered and platform-scoped:

```text
weasel/0001-yunpin-local-candidate-provider.patch
squirrel/0001-yunpin-local-candidate-provider.patch
```

Each patch must:

1. carry `GPL-3.0-only` provenance and an upstream base commit;
2. change only the native integration needed for the local YunPin provider,
   candidate metadata, privacy mode, or packaging;
3. keep network and disk I/O out of keyboard event handlers;
4. include a focused test or a reproducible host test entry;
5. avoid upstream logos, skins, dictionaries and other brand assets.

No patch files exist in the bootstrap release because native host integration
has not yet passed Windows/macOS real-device tests. Development source builds
must say so explicitly; a signed production installer cannot be cut until the
appropriate patches and host evidence are present.
