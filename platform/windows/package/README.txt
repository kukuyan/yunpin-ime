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

* The package is unsigned. Run the transaction in elevated 64-bit PowerShell
  as the intended signed-in user. Locked TSF DLLs require a controlled
  sign-out/reboot window; the installer refuses before replacing them.
* Named-pipe access is restricted to SYSTEM, the current user, and the minimum
  app-container read/write compatibility boundary. This is not cryptographic
  client authentication.
* The merged yunpin_filter is present, but yunpin/enabled is false. No private
  phrase snapshot is packaged or read until secure-input suppression and IPC
  isolation are verified in real x86/x64 hosts. The independent public-data
  short_input_guard remains enabled; it only filters Rime's in-memory upstream
  candidates and does not load a personal snapshot. session_learning remains
  false until the Windows secure-input and IPC gates pass.
* A fresh install does not configure cloud synchronization, migrate Sogou, or
  import private phrases. An upgrade restores previously opted-in startup;
  that existing resident may resume its normal configured sync afterward.
* The package carries the public default-tag `yunpin-sync-agent.exe`. Its
  protected pairing commands support approved device additions. The diagnostic
  `e2e-init-empty-baseline` alias stays private-tag only. Installation stages
  `YunPinSyncAgent` disabled/stopped, then restores only a previous enabled
  choice. It does not read or copy credentials or private word databases.
* The tray's Settings item uses the separate GUI-subsystem
  `support\sync-agent\yunpin-settings.exe`, so it opens the temporary local-only
  guard/sync/vocabulary page without leaving a console or PowerShell window.
  The page has no endpoint, account/device, recovery, reset or re-pair control.
* Private-tag pairing binaries are separate short-lived CI E2E artifacts. They
  are not copied into this archive or any GitHub Release asset.

The installer verifies MANIFEST.sha256 and journals each replacement stage in
the current user's private installation transactions directory. It freezes
known writers, retains runtime/support, targeted configuration/task/registry
state and both system DLLs, and restores them on failure. User databases and
credentials are not transaction backup targets. Existing custom overlays and
startup choices are retained; a fresh installation keeps private candidates
and session learning disabled. A rollback that cannot finish leaves writers
stopped with RECOVERY_REQUIRED and blocks any fresh attempt. After resolving
the recorded cause, use the retained transaction's Install-Preview.ps1 with
-RecoverTransaction (and the same -InstallRoot if customized). Do not delete
the journal, force a new installation, or overwrite learning/credential data.

After endpoint, approved device pairing and Rime bridge setup, run an explicit
sync and verify real candidate visibility before using
`support\sync-agent\Enable-SyncAgent.ps1`; its redacted `resident-ready` gate
keeps the task disabled if setup is incomplete. `Verify-SyncAgent.ps1` checks
the initial disabled state. Clean devices without any baseline/snapshot can
use initialize-learning --confirm-empty-baseline; existing devices retain
their current baseline. Adding devices requires the signed-roster-capable
relay and clients; an old client binary must not be restored over a v3 trust
checkpoint after enrollment.
The source archive next to this package contains the exact pinned upstreams,
patches, YunPin sources, build scripts, licenses, and verified Boost source.
