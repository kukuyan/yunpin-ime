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
short-input filtering and session correction. Automatic spelling correction is
now default-off on both platforms; its revised single-bridge graph policy still
needs a current opt-in merged-librime fixture and native-host acceptance.
Production-dictionary performance, persistent learning and synchronization also
remain end-to-end gates. The platforms share behavior and semantic theme
tokens, not UI source code.

The shared engine provides Pinyin segmentation, full-Pinyin prefix and initials
indexes, deterministic ranking, revision-aware correction feedback and a
word-scoped in-memory correction monitor. `librime-yunpin` supplies a read-only
private-snapshot filter, a public short-input guard, a default-off bounded
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

One immutable private snapshot accepts at most 100,000 reviewed entries. The
importer applies the same hard cap after duplicate merge and descending-frequency
sort. A 100,000-row high-collision synthetic snapshot measured query P95 at
1.672 ms and incremental peak RSS at 80.172 MiB on the verified development
machine; the enforced budgets are 20 ms and 256 MiB. These measurements do not
describe a native host or arbitrary real vocabulary. The R0W conversion has
94,382 merged rows, but its complete TSV remains incoming and undeployed; an
already installed legacy binary still has the former 50,000-entry ceiling.

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

## Experimental automatic spelling correction

The shipped Windows and macOS schemas set both
`translator/enable_correction` and `yunpin/typo_correction` to false. Selecting
the component name alone is inert: the component factory returns no corrector
when disabled and deliberately does not fall back to librime's broader
`NearSearchCorrector`.

When an experiment explicitly enables both switches, the locked librime
1.16/1.17 compatibility patch computes normal-exact reachability before graph
expansion. Any complete normal exact path from the start to the end disables
correction for the whole composition. Otherwise, a correction search is allowed
only at a forward exact-reachable offset, and an admitted correction must end at
a reverse exact-suffix-reachable offset. The resulting path therefore contains
an exact prefix, one correction bridge and an exact suffix. At most one input
offset may add correction edges in the entire composition, and at most 32
searches may be attempted.

The portable generator changes one physical action per variant—QWERTY neighbour,
missing key, extra key or adjacent transposition—and reads only the already
loaded Prism. It fails closed at 128 input bytes, produces at most 768 raw
variants, and exposes no more than 16 Prism-validated edges per searched offset.
Reviewed valid-Pinyin substitutions, including `you` to `yao`, are a second
explicit experiment and are off by default.

For normalized long input of at least 12 characters, the merged candidate
filter keeps at most one automatic correction. It may occupy total rank two
when no private candidate leads, or total rank three when one private candidate
leads. With two private leaders, no ordinary candidate, or a correction outside
the bounded first-page window, it is omitted; corrections never spill to later
pages. This is a safety cap, not evidence that automatic correction is ready to
ship.

Portable generator tests, platform patch/config tests and stub candidate-order
tests cover these bounds. Historical merged-Rime results that depended on two
corrected offsets or default `you` to `yao` no longer describe the current
policy. A revised opt-in real ScriptTranslator/Dictionary/Rime C API suite must
verify the whole-exact disable rule, the single forward/reverse bridge and the
32-search budget before performance or ranking claims are restored.

A future local model, if justified by production-corpus measurements, remains
an optional default-off sidecar. It must be offline, receive bounded data over
local IPC, obey a strict deadline and fail closed to the ordinary exact path.
There is no NearSearch or model fallback in the current disabled profile.

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

## Local Replay Lab

Replay Lab is a separate, opt-in local evidence path. The Go core implements a
strict bounded `EventV1`, episode/sequence validation, an fsync-before-metadata
append-only store, lifecycle CLI, export and deterministic report. The C++ side
implements a disabled-by-default fixed 64-slot single-producer/single-consumer
ring, bounded native frames, an 8 KiB JSON limit and drop counting. Native-frame
parsing and synthetic conversion into `EventV1` are tested.

The merged filter publishes the actual bounded first candidate page plus
selection, commit and composition-backspace frames. Each session keeps its own
candidate snapshot, so concurrent host sessions cannot borrow one another's
selection rank. Squirrel and Weasel start a dormant watcher at their fixed
per-user Replay Lab root. The watcher observes `active.json` on a background
thread and enables the producer only for a valid `running` session created by
an explicit `yunpin-replay-lab start` or `resume`; ordinary IME startup records
nothing. `pause`, an invalid session file, or host shutdown disables production
and discards queued content at the boundary. Contexts that Rime marks as
password/private/one-shot, plus host-opted-out contexts, fail closed through
the same predicate as learning; installed native secure-field propagation is
part of the remaining manual gate.

The input path performs only fixed-memory copies and a nonblocking ring push.
File polling, append, flush and the 64 MiB per-session native-file cap are on the
background watcher. A cross-language synthetic test drives native frames into
the spool and verifies that the Go report recognizes a correction candidate
displacing a viable ordinary candidate. Installed Squirrel/Weasel capture is
still a manual gate. Replay traces are private data and remain outside Git and
synchronization.

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
