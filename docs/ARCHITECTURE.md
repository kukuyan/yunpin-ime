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
short-input filtering, deterministic typo correction, corrected-final-key
latency and session correction, while native host UI, production-dictionary
performance, persistent learning and synchronization still require end-to-end
acceptance. The platforms share behavior and semantic theme tokens, not UI
source code.

The shared engine provides Pinyin segmentation, full-Pinyin prefix and initials
indexes, deterministic ranking, revision-aware correction feedback and a
word-scoped in-memory correction monitor. `librime-yunpin` supplies a read-only
private-snapshot filter, a public short-input guard, a bounded deterministic
Pinyin `yunpin_corrector`, and a bounded session learner connected to librime
commit/update/unhandled-key/option/delete notifiers. It recognizes only
same-pinyin replacement after an exact Unicode scalar count of unmodified
Backspaces within five seconds and stable-reranks the first eight upstream
candidates. Encrypted correction persistence, a desktop habit-report bridge,
and a background process that atomically swaps rebuilt snapshots into the input
process are not connected. The adapter acceptance gate requires proof that no
key event waits for network or disk.

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

## Local deterministic typo correction

The typo path is deliberately smaller than a language model and narrower than
global spelling algebra. ScriptTranslator asks the schema-selected
`yunpin_corrector` for corrections at a syllable-graph position. The component
generates at most one bounded edit per variant—physical QWERTY neighbour,
missing key, extra key or adjacent transposition—and admits only spellings
found in the already-loaded Prism. A complete phrase path may select corrected
variants at more than one syllable position. The only valid-syllable exception
currently reviewed is the one-way `you` → `yao` confusion. It performs no file,
disk, network or model I/O. After Prism validation and per-spelling-ID
deduplication, variants are deterministically ranked and capped at 16 correction
edges per input offset before Dictionary/Table traversal.

Exact short spellings remain on the normal Prism path. One-letter segments do
not produce corrections, unchanged spelling is never emitted as a variant, and
the merged-Rime regression fixture requires exact `xu` and `you` to stay first.
Long-context goldens cover neighbour `shouxu...`, missing `shosu...`, extra
`shouusu...`, transposed `shuosu...`, reviewed `youjubei...`, and the
six-letter-syllable missing-key case `zhuantai` → `zhuangtai`. The original
`shouxubijiakuaideshihou` fixture proves two corrected syllable positions can
coexist in one path. The exact-prefix collision `shangban` keeps “上班” before
corrected “山班” while retaining both candidates, even when the synthetic
dictionary gives “山班” 50 times the weight.

The component is selected by a minimal compatibility patch that adds
`translator/corrector_component` and changes exact-match identity from spelling
ID alone to `(spelling ID, consumed input length)`. Without the second field, a
corrected `shan` consuming all five bytes of `shang` could inherit the exact
four-byte-prefix classification and escape the correction penalty. Separate
patch files are locked to the exact macOS librime 1.16 and Windows librime 1.17
commits and SHA-256 values. YunPin registers `yunpin_corrector` without
replacing the global upstream `corrector`, so other schemas in the same process
are unaffected.

Stock librime assigns every correction edge a fixed `log(0.01)` credibility;
the generator's edit costs select and bound variants but are not a learned or
fine-grained ranking score. The 50× `shangban` collision proves the penalty is
applied to that exact-prefix case, not that arbitrary production weights can
never overcome it. The current real ScriptTranslator/Dictionary/Rime C
API E2E uses synthetic phrases, warms 10 times, measures 100 final-key samples
per corrected long input and enforces P95 ≤ 20 ms. The completed fixture run
measured 534–841 µs for the two-error `shouxubijiakuaideshihou` input and
907–1611 µs for the 37-byte reviewed `you` → `yao` input across two independent
runs. It does not prove ranking,
pollution or latency against full Rime Ice plus a 50,000-entry personal index.

A future local model, if justified by production-corpus measurements, is an
optional default-off sidecar rather than part of the IME process. It must be
offline, receive bounded data over local IPC, obey a strict deadline, and fail
closed to exact/deterministic candidates when unavailable or invalid. It may
never turn model or server availability into a typing dependency. Chinese-
English mixed-input segmentation/ranking is also a future design direction;
the current preview has no claimed mixed-input model.

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
