# Windows frontend boundary

Target: Windows 10 22H2 and Windows 11 x64.

The Windows package is built from the pinned GPL-3.0 Weasel source. It must
ship both x86 and x64 TSF components because 32-bit and 64-bit host processes
load matching text services. Both connect over local authenticated IPC to a
single x64 YunPin input service containing librime and the read-only YunPin
candidate index.

The host adapter must mark password fields, private mode and one-shot input as
non-learning. An input event may query only the in-memory index and librime;
SQLite access, index rebuild, sync and HTTP run in background workers. Package
registration and uninstall tests cover Notepad, Office, Chrome, Terminal and
both host architectures.

Copy `../rime/common/default.custom.yaml` and
`../rime/weasel/weasel.custom.yaml` into the test user's Rime data directory,
then redeploy. These files set five numbered horizontal candidates and an
original light/dark YunPin palette.

Production packages require Authenticode signing. Certificates and signing
credentials belong only in a protected GitHub Release environment; source and
development builds must never pretend to be production-signed installers.
