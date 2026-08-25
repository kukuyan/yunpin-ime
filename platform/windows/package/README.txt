YunPin IME / 云拼输入法 — Windows development preview
=========================================================

This archive is an unsigned, inspectable developer build. It is not a
production installer. Read MANIFEST.sha256 and BUILD-METADATA.json before use.

安装（Windows 10 22H2 / Windows 11 x64）：

  PowerShell -ExecutionPolicy Bypass -File .\Install-Preview.ps1 `
    -AcceptUnsignedDevelopmentBuild

卸载（保留个人词典和可恢复的旧运行目录）：

  PowerShell -ExecutionPolicy Bypass -File .\Uninstall-Preview.ps1 `
    -ConfirmUninstall

The package contains x86 and x64 TSF components plus an x64 input service. It
uses a YunPin-specific TSF identity, registry path, named pipe, window class,
runtime path and Rime application name, so it does not register as stock
Weasel. Automatic and manual WinSparkle update calls are disabled.

Security gates:

* The package is unsigned. Windows will request administrative permission when
  the TSF registration helper copies the components into the system directory.
* Named-pipe access is restricted to SYSTEM, the current user, and the minimum
  app-container read/write compatibility boundary. This is not cryptographic
  client authentication.
* The merged yunpin_filter is present, but yunpin/enabled is false. No private
  phrase snapshot is packaged or read until secure-input suppression and IPC
  isolation are verified in real x86/x64 hosts. The independent public-data
  short_input_guard remains enabled; it only filters Rime's in-memory upstream
  candidates and does not load a personal snapshot. session_learning remains
  false until the Windows secure-input and IPC gates pass.
* No cloud synchronization, Sogou migration, or private phrase import is run by
  these scripts. R0W is not contacted.
* The package carries the public default-tag `yunpin-sync-agent.exe`. Its
  private pairing subcommands are not registered and return exactly `unknown
  command`. Installation copies it into the current user's protected sync
  state and registers `YunPinSyncAgent` as a disabled, stopped scheduled task.
  It does not read credentials, databases, dictionaries, or the network.
* The tray's Settings item uses the separate GUI-subsystem
  `support\sync-agent\yunpin-settings.exe`, so it opens the temporary local-only
  guard/sync/vocabulary page without leaving a console or PowerShell window.
  The page has no endpoint, account/device, recovery, reset or re-pair control.
* Private-tag pairing binaries are separate short-lived CI E2E artifacts. They
  are not copied into this archive or any GitHub Release asset.

The installer verifies every bundle file against MANIFEST.sha256, backs up
overwritten Rime configuration files, and keeps existing user database files.
When upgrading, it also preserves an existing explicit `yunpin/enabled: true`
or `yunpin/session_learning: true` choice while taking all other settings from
the newly verified overlay; a clean installation keeps both package defaults
disabled.
After an authorized private E2E procedure has completed endpoint, account,
two-device pairing and Rime bridge setup, run
`support\sync-agent\Enable-SyncAgent.ps1`; its redacted `resident-ready` gate
keeps the task disabled if setup is incomplete. `Verify-SyncAgent.ps1` checks
the initial disabled state without contacting R0W.
The source archive next to this package contains the exact pinned upstreams,
patches, YunPin sources, build scripts, licenses, and verified Boost source.
