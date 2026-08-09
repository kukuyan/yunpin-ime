# Architecture

```text
Windows TSF DLL (x86/x64) ─┐
                           ├─ local IPC/session ─ librime + yunpin engine
macOS InputMethodKit       ┘                         │
                                                    ├─ immutable memory index
                                                    └─ async learning queue
                                                               │
                                                encrypted local SQLite/outbox
                                                               │ background only
                                                               ▼
                                               self-hosted opaque sync server
```

## Desktop frontends

The Windows development preview follows Weasel's TSF architecture: both x86
and x64 TSF components communicate with a single x64 input service. The macOS
development preview follows Squirrel's InputMethodKit architecture and targets
a Universal arm64/x86_64 package for macOS 13+. Pinned upstream sources, patch
sets, native build/package scripts and unsigned preview installers now exist.
They are not signed releases. A merged librime C API test covers ranking,
short-input filtering and session correction, while native host UI, persistent
learning and synchronization still require end-to-end acceptance. The platforms share behavior and
semantic theme tokens, not UI source code.

The shared engine provides Pinyin segmentation, full-Pinyin prefix and initials
indexes, deterministic ranking, revision-aware correction feedback and a
word-scoped in-memory correction monitor. `librime-yunpin` supplies a read-only
private-snapshot filter, a public short-input guard, and a bounded session
learner connected to librime commit/update/unhandled-key/option/delete
notifiers. It recognizes only same-pinyin replacement after an exact Unicode
scalar count of unmodified Backspaces within five seconds and stable-reranks
the first eight upstream candidates. Encrypted correction persistence, a
desktop habit-report bridge, and a background process that atomically swaps
rebuilt snapshots into the input process are not connected. The adapter
acceptance gate requires proof that no key event waits for network or disk.

## Candidate policy

The first page contains eight entries. At most two are personal. Within the merged stream:

1. Explicitly pinned personal phrases.
2. Eligible high-frequency personal, historical, or imported phrases.
3. Locked public high-frequency packs.
4. Rime base candidates.

Pinned long phrases activate after two complete syllables or four initials. Automatically learned phrases become sync-eligible after two explicit selections. Tombstones are remove-wins and normal counters cannot resurrect them.

For one- or two-letter normalized Pinyin, the librime adapter independently
filters only upstream predictions made entirely of at least three CJK
ideographs. It retains one- and two-character words plus English, mixed and
malformed text. This guard is enabled in the Windows preview even while private
snapshot injection is disabled. Non-pinned engine entries with three or more
syllables additionally require two complete full-Pinyin syllables and cannot be
recalled through short initials.

Expression search and favorite actions are deliberately disconnected. Rime
commit text is untrusted dictionary data, so neither frontend interprets magic
text prefixes as browser or filesystem commands. The reserved configuration is
inert until an unforgeable, explicitly armed native action channel exists.

## Local state

The reference local store uses record-level encrypted SQLite and an encrypted
outbox. The `desktopagent` skeleton serializes a bounded credential bundle into
non-synchronizing macOS Keychain storage or current-user Windows DPAPI storage.
Its `status`, `sync-once` and single-instance `run` paths remain outside the IME
key-event process. Production `init-account` fails closed because the relay
does not yet support rollback-safe account provisioning. Installer registration
and signed background services remain incomplete.

Password fields, private mode and one-time inputs bypass correction monitoring
in the core model and librime option bridge, but native hosts do not yet provide
a fully trusted secure-context signal or proof of the host editor buffer after
Backspace. Production adapters must persist aggregates in the encrypted store,
expose reports only on explicit local request, and update candidate snapshots
through an atomic generation swap.

## Sync service

The Go service stores opaque signed envelopes in SQLite WAL. It has no phrase decryption key. `/v1/sync` performs idempotent exchange by `(device_id, device_seq)` and cursor. Network failure only grows a local outbox; input remains available.

The shared `syncclient` worker runs outside the input key-event path. It stages
the exact ciphertext wire record before upload, maintains the signed device
sequence/previous-hash chain, retries a lost response idempotently, verifies
and decrypts downloaded records locally, merges the CRDT into `localstore`, and
only then advances its cursor. The local checkpoint contains ciphertext and
relay metadata but never the bearer token or private keys.

This headless path does not yet make either native frontend synchronized. The
desktop agent has current-user DPAPI and macOS Keychain adapters plus a
single-instance runner skeleton, but authenticated cross-device trust roster,
pairing/recovery UI, rotation, persistent native learning, encrypted candidate
snapshot rebuilding and Rime reload remain desktop acceptance gates.

## Clipboard sync (Preview 0.2)

Clipboard sync uses the existing account/device trust graph but a separate
`clipboard.v1` envelope type and a domain-separated key derived from the account
root. The encrypted payload contains the UTF-8 text, origin device and sequence,
HLC timestamp, expiry, content fingerprint and a random event identifier. The
server can route and expire the opaque envelope but cannot read the text or its
fingerprint.

Windows and macOS clipboard agents observe local changes, apply local sensitive
content policy, encrypt accepted events and update the local clipboard when a
new remote event wins. A bounded event cache suppresses echo loops and duplicate
delivery. Clipboard history is not mixed with the phrase CRDT, does not enter
the candidate index and never runs in the input key-event path.

The iOS host app performs network exchange and stores received items in the App
Group. The keyboard extension reads only that local snapshot. iOS pasteboard
upload is initiated by an explicit paste/share/Shortcut action because reading
another app's general pasteboard without user intent can produce a system
privacy notification or approval prompt.

## iOS phase two

The Swift host app performs sync and updates an App Group snapshot. The keyboard extension reads that snapshot and never directly opens the network. Without Full Access it remains read-only; learning writeback is enabled only after the user opts into Full Access. GPL desktop code is not reused.
