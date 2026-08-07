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
  isolation are verified in real x86/x64 hosts.
* No cloud synchronization, Sogou migration, or private phrase import is run by
  these scripts. R0W is not contacted.

The installer verifies every bundle file against MANIFEST.sha256, backs up
overwritten Rime configuration files, and keeps existing user database files.
The source archive next to this package contains the exact pinned upstreams,
patches, YunPin sources, build scripts, licenses, and verified Boost source.
