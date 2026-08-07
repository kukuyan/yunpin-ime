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

The first page contains five entries. At most two are personal. Within the merged stream:

1. Explicitly pinned personal phrases.
2. Eligible high-frequency personal, historical, or imported phrases.
3. Locked public high-frequency packs.
4. Rime base candidates.

Pinned long phrases activate after two complete syllables or four initials. Automatically learned phrases become sync-eligible after two explicit selections. Tombstones are remove-wins and normal counters cannot resurrect them.

## Local state

The reference local store uses record-level encrypted SQLite and an encrypted outbox. Production desktop adapters must keep private keys in the OS credential store and update candidate snapshots through an atomic generation swap. Password fields, private mode, and one-time inputs bypass learning in the reference implementation; native host detection remains a desktop-alpha gate.

## Sync service

The Go service stores opaque signed envelopes in SQLite WAL. It has no phrase decryption key. `/v1/sync` performs idempotent exchange by `(device_id, device_seq)` and cursor. Network failure only grows a local outbox; input remains available.

## iOS phase two

The Swift host app performs sync and updates an App Group snapshot. The keyboard extension reads that snapshot and never directly opens the network. Without Full Access it remains read-only; learning writeback is enabled only after the user opts into Full Access. GPL desktop code is not reused.
