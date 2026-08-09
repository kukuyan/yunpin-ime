# YunPin desktop credential and background agent

This Apache-2.0 Go module is an intentionally narrow, single-device encrypted
backup preview around `syncclient`. It does not make the Windows or macOS input
method a completed multi-device product.

`CredentialBundleV1` uses a canonical bounded binary format. It contains the
device token, Ed25519 seed, X25519 private key, per-device local database key,
account object-ID key, epoch keys, and the locally trusted Ed25519 keys. The
human `yprec1...` recovery root and its derived recovery-authentication value
are never stored in this bundle. A disaster-recovery record must preserve both
the displayed recovery root and the separately displayed random account ID;
the current `yprec1...` text alone does not encode that account ID.

The platform stores are:

- macOS 13+: a non-synchronizable generic-password item in the data-protection
  Keychain, marked `AfterFirstUnlockThisDeviceOnly`. Tests never read or change
  the user's real Keychain.
- Windows: a current-user `CryptProtectData` record with UI forbidden and
  domain-separated optional entropy. It deliberately does not use the
  machine-wide DPAPI flag. The protected record is atomically replaced below
  the current user's `%LOCALAPPDATA%`; the constructor rejects shared or
  arbitrary directories outside that tree. No explicit DACL is installed in
  this slice, so packaging must use the fixed per-user default directory and
  preserve its inherited user ACL.

YunPin explicitly wipes its own Keychain/DPAPI plaintext copies before freeing
the OS buffers. Go and operating-system APIs may still create immutable string,
stack, runtime, or framework-owned copies that cannot be reliably overwritten;
in particular, the HTTP authorization header briefly requires a Go string.
Those lifetime limits are documented rather than being presented as guaranteed
process-wide zeroization.

The command skeleton exposes `init-account`, `sync-once`, `run`, and `status`.
`init-account` is fail-closed even after its explicit recovery-display
acknowledgement: the current relay has no account-delete rollback, so a local
Keychain/DPAPI or SQLite failure after the remote write could leave a permanent
orphan account. The only non-atomic provisioning helper is package-private and
used solely with in-memory fakes; external library callers cannot enable it.
`status` is local-only. `run`
takes a per-user file lock, runs networking outside the IME process, and emits
only stable redacted event codes with numeric summaries.

This slice does **not** implement:

- account recovery or a multi-device pairing UI;
- an authenticated cross-device public-key trust roster;
- encrypted persistence and desktop reporting for the in-process librime
  correction events, plus trusted TSF/InputMethodKit secure-context/deletion
  evidence;
- an encrypted in-memory candidate snapshot bridge or Rime reload;
- Keychain/DPAPI installer registration or signed background launch services;
- TLS termination for the private NAS endpoint.

Consequently a successful `sync-once` only proves that the encrypted local
database can exchange opaque vocabulary envelopes. It does not prove that a
phrase selected in one native input method appears on another device.
