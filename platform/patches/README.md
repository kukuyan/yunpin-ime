# Upstream patch policy

Do not vendor Weasel, Squirrel, or their Git histories here. Build automation
must clone the repositories and verify the exact commit recorded in
`../upstream-lock.json` before applying patches.

Patch filenames are ordered and platform-scoped. The current desktop previews
use:

```text
weasel/0001-yunpin-isolate-preview-identity-and-IPC.patch
weasel/0002-yunpin-remove-winsparkle-link.patch
squirrel/0001-yunpin-development-preview-identity.patch
squirrel/0002-yunpin-original-input-source-artwork.patch
squirrel/0003-yunpin-original-app-icon-name.patch
```

Each patch must:

1. carry `GPL-3.0-only` provenance and an upstream base commit;
2. change only the native integration needed for the local YunPin provider,
   candidate metadata, privacy mode, or packaging;
3. keep network and disk I/O out of keyboard event handlers;
4. include a focused test or a reproducible host test entry;
5. avoid upstream logos, skins, dictionaries and other brand assets.

The Weasel patches establish a separate TSF/profile identity, runtime/registry/
IPC names, a current-user pipe ACL boundary, and a preview with no WinSparkle
link or update calls. The Squirrel patches establish a collision-free development identity,
isolated storage, disabled upstream update/Rime-sync paths, and original YunPin
artwork. The separate Apache-2.0 `librime-yunpin` source is staged into nested
librime and merged by both platform builds; it is intentionally not carried in
a GPL frontend patch. macOS verifies the merged filter with a synthetic
headless ranking/private-mode test. Windows keeps private snapshot loading
disabled until secure-input/IPC host evidence passes. Learning, encrypted
background refresh, and full native-host evidence remain explicit release
gates on both platforms.
