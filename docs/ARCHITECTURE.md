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

The planned Windows client follows Weasel's TSF architecture: both x86 and x64 TSF components communicate with a single x64 input service. The planned macOS client follows Squirrel's InputMethodKit architecture and targets a Universal arm64/x86_64 package for macOS 13+. The bootstrap repository contains the shared engine and configuration overlays, but not yet the native adapters or installers. The platforms will share behavior and semantic theme tokens, not UI source code.

The current shared engine provides Pinyin segmentation, full-Pinyin prefix and initials indexes, and deterministic ranking. The desktop-alpha integration will expose it as `librime-yunpin`, connect learning events, and make a background process atomically swap immutable snapshots into the input process. The adapter acceptance gate requires proof that no key event waits for network or disk.

## Candidate policy

The first page contains eight entries. At most two are personal. Within the merged stream:

1. Explicitly pinned personal phrases.
2. Eligible high-frequency personal, historical, or imported phrases.
3. Locked public high-frequency packs.
4. Rime base candidates.

Pinned long phrases activate after two complete syllables or four initials. Automatically learned phrases become sync-eligible after two explicit selections. Tombstones are remove-wins and normal counters cannot resurrect them.

## Local state

The reference local store uses record-level encrypted SQLite and an encrypted outbox. Production desktop adapters must keep private keys in the OS credential store and update candidate snapshots through an atomic generation swap. Password fields, private mode, and one-time inputs bypass learning in the reference implementation; native host detection remains a desktop-alpha gate.

## Sync service

The Go service stores opaque signed envelopes in SQLite WAL. It has no phrase decryption key. `/v1/sync` performs idempotent exchange by `(device_id, device_seq)` and cursor. Network failure only grows a local outbox; input remains available.

The shared `syncclient` worker runs outside the input key-event path. It stages
the exact ciphertext wire record before upload, maintains the signed device
sequence/previous-hash chain, retries a lost response idempotently, verifies
and decrypts downloaded records locally, merges the CRDT into `localstore`, and
only then advances its cursor. The local checkpoint contains ciphertext and
relay metadata but never the bearer token or private keys.

This headless path does not yet make either native frontend synchronized.
Windows DPAPI/Credential Manager and macOS Keychain adapters, authenticated
pairing/keyring persistence, a single-instance background scheduler, native
learning hooks, atomic snapshot rebuilding and Rime reload remain desktop
acceptance gates.

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
