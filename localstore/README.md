# Encrypted local store

This Go reference implements the background persistence boundary shared by desktop designs. Phrase text, Pinyin, counts, pin state and tombstones are individually protected with XChaCha20-Poly1305 in SQLite WAL; only a stable opaque object ID, nonce, ciphertext and update time are visible in the database.

The containing platform adapter owns the opaque paired-device credential;
`localstore` never persists it. Desktop adapters use current-user Windows DPAPI
or the macOS atomic private-file store, while mobile containing apps use
Android Keystore or Apple Keychain. This slice does not claim SQLCipher
whole-file encryption. The input key-event path never calls this package. The
containing app reads/decrypts the database, builds a validated immutable
snapshot, and atomically swaps a new generation into the engine.

`RecordSelection` suppresses password, private-mode and one-time contexts. The first committed selection creates a coalesced encrypted outbox event in the same SQLite transaction; later selections update that record. Explicit saves and tombstones also enter the outbox immediately. Versioned acknowledgement cannot delete a newer coalesced count.

Synchronized clients open the database with `OpenForDevice` and a random 128-bit lowercase-hex device ID. Each encrypted phrase persists the actual `protocol.PhraseState`: usage is a per-device G-counter, pin state is HLC-LWW, and presence is a remove-wins generation. `Delete` writes a same-generation tombstone; ordinary selections refuse to mutate that tombstone; `SaveExplicit` is the only re-add path and increments the generation. The HLC wall/counter state is persisted in encrypted-store metadata and observes remote clocks before later local changes.

`PendingEvent.ProtocolPayload` creates canonical phrase content plus a detached CRDT value. `PendingEvent.SealEnvelope` requires an `OpenForDevice` event, verifies that the envelope device ID matches the local counter component, then encrypts and signs a `protocol.Envelope`; callers serialize it with `Envelope.ToWire`. `MergeRemotePayload` validates the opaque object identity, component IDs, generations and HLC nodes, merges without echoing a remote event to the outbox, and materializes aggregate count/pin/deletion fields for snapshot generation. Tests cover offline two-device merge, remove-wins behavior, explicit generation re-add, envelope signing/opening, and local-only stores being unable to emit sync envelopes.

`LoadSyncState`, `SavePreparedUpload`, `CommitPreparedUpload` and
`AdvanceSyncCursor` form the crash-safe checkpoint used by the shared
background worker. The exact ciphertext wire bytes are persisted before a
request so an accepted response lost in transit can be replayed byte-for-byte.
Committing advances the device sequence/hash chain and acknowledges only the
outbox version actually sent in one SQLite transaction. Bearer tokens and
private keys are never stored in these tables, and a database is bound to its
original random device ID.
