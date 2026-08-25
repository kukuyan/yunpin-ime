<!-- SPDX-License-Identifier: Apache-2.0 -->

# YunPin mobile protocol, privacy, and capability freeze

Status: **frozen for the first Android and iOS native slices**.

Machine-readable companion: `mobile/contracts/mobile-protocol-freeze-v1.json`.

This freeze is a compatibility boundary, not a claim that either mobile client
is ready for distribution. Android and iOS may implement the boundary in
different platform code, but neither may weaken it. Any incompatible change
requires a new contract version, cross-platform migration tests, and explicit
review of the privacy and trust model.

## 1. Authoritative compatibility sources

The mobile implementation reuses these existing repository contracts:

- `protocol/`: Protocol v1 canonical CBOR, XChaCha20-Poly1305 envelopes,
  Ed25519 signatures, opaque object identifiers, CRDT merge rules, YPBX sealed
  boxes, and pairing v2 cryptography;
- `sync/openapi.yaml` and `syncclient/`: the optional content-blind relay,
  selectable endpoint profile, strict wire decoding, idempotent upload, and
  cursor-based download;
- `localstore/`: encrypted local state, coalescing outbox, prepared-wire retry,
  CRDT materialization, and compare-and-swap cursor advancement;
- `desktopagent/credentials.go`: the YPCB credential-bundle format and signed
  roster trust boundary;
- `desktopagent/snapshot.go` and `librime-yunpin/src/snapshot_store.cpp`: the
  private snapshot TSV producer/consumer contract;
- `mobile/shared/`: the network-free C ABI over the existing candidate engine.

Mobile code must not create a second protocol with similar-looking fields. It
must either use a conforming implementation of the contracts above or stop
fail-closed before mutation.

## 2. Frozen binary and wire formats

### Protocol v1 envelope

The clear authenticated header remains the canonical-CBOR map with integer keys
`1..8`: protocol version, 16-byte account ID, 16-byte opaque object ID, key
epoch, 16-byte source device ID, per-device sequence, optional 32-byte previous
record hash, and 24-byte nonce. Phrase text, Pinyin, counts, pin state,
deletion state, and settings remain inside ciphertext.

The payload is canonical CBOR, length-prefixed, padded to 512-byte buckets, and
encrypted with XChaCha20-Poly1305. The Ed25519 signature covers
`canonical_header_cbor || ciphertext`. Upload identity comes from the device
bearer; downloads must supply a source device ID that is present in the locally
verified signed roster. Relay device metadata is never a trust root.

### YPBX v1 sealed box

`YPBX` is the network/storage representation used by encrypted keyring and
pairing-package fields. Its decoded big-endian representation is frozen as:

```text
"YPBX" || version:u8 || ciphertext_length:u32be || nonce:24 || ciphertext
```

Version is `1`; the complete decoded blob is at most 262144 bytes. Unpadded
base64url is required at the JSON boundary. Unknown versions, padding,
truncation, length mismatch, trailing bytes, and invalid AEAD material are
rejected.

### YPCB v2 credential bundle

`YPCB` is not a relay format. It is the canonical device-local credential blob
stored only through the platform secret store. Mobile writers emit version `2`.
The bundle binds the account/device identity, bearer credential, device signing
and pairing private material, local database key, object-ID key, epoch keys, and
the creator-signed trusted roster. Verification-key maps are derived from that
signed roster and are not separately trusted.

Recovery roots and recovery-authentication material are absent from YPCB. They
must not be added by a mobile adapter, copied into endpoint preferences, logged,
exported, displayed, or placed in an App Group/shared-preferences container.
Android uses Keystore-backed protection; iOS uses Keychain access scoped to the
containing app unless a narrowly reviewed sharing requirement is introduced.

### Snapshot TSV profile

The first mobile publisher emits UTF-8, LF-terminated, five-column TSV with this
exact header:

```text
phrase\tpinyin\tsource\tuse_count\tpinned
```

Rows use the existing canonical phrase/Pinyin rules, positive decimal counts,
and the existing pinned boolean spellings. The reviewed producer ceiling is
100000 unique rows. A reader may retain the existing four- and six-column
compatibility behavior, but a mobile writer must emit the five-column profile
above. A malformed header, unsafe row, duplicate identity, oversized input, or
failed integrity check cannot replace the last-known-good snapshot.

Snapshot contents are sensitive even though the file is not a relay envelope.
They live only in an application-private location (or the reviewed iOS App Group
handoff), use platform data protection, and are staged, flushed, validated, and
atomically renamed before a generation switch. The keyboard sees an immutable
generation. Real snapshots and imports never enter source, tests, logs, or
release artifacts.

## 3. Sync state machine

The relay transport is optional after configuration. Once an already-paired
opaque device credential is bound to a user-selected profile, local candidate
lookup and encrypted local queuing continue while the selected relay is
unreachable or the device is offline. An unconfigured client fails closed: it
does not invent an anonymous local account, encryption key, recovery secret, or
queue outside that paired identity. A relay origin is supplied by the user at
runtime; no environment-specific host alias, IP address, or device name is
compiled into either client. HTTPS is the default.
Private-network HTTP requires the existing explicit opt-in policy, and public
plaintext HTTP, URL credentials, redirects, paths, queries, and fragments remain
rejected.

The background worker preserves this order:

1. Commit a local selection/save/delete and its encrypted outbox version in one
   local transaction.
2. Seal one event using the next per-device sequence and previous record hash.
3. Persist the exact ciphertext wire bytes before making the request.
4. Retry a lost response with byte-identical wire material. Never reseal under
   the same sequence.
5. Accept only the expected acknowledgement or a defined fail-closed rejection.
   Acknowledgement removes only the exact outbox version sent; a newer coalesced
   local mutation survives.
6. For each download, require a strictly increasing cursor, reconstruct the
   Protocol v1 header, verify the source against the signed roster, verify the
   signature, decrypt locally, validate the payload, and merge the CRDT.
7. Advance the cursor with compare-and-swap only after the complete page is
   durably merged. Empty pages cannot advance it. `has_more` causes another
   bounded background round, never unbounded work in a keyboard process.

The CRDT behavior is also frozen:

- usage is a per-device G-Counter merged by component-wise maximum;
- pinned state and settings are HLC-LWW with deterministic ties;
- presence is remove-wins within one generation, so counts cannot resurrect a
  deletion;
- only an explicit re-add increments the generation and restores the object;
- unknown setting keys remain opaque for forward compatibility;
- remote merges do not echo a new local outbox event.

## 4. Extensible data plane, currently fixed control plane

The data plane uses arbitrary source device IDs and a dynamically sized map of
verification keys derived from an authenticated roster. Mobile databases,
models, and UI lists therefore have no compiled device-count capacity.

The current enrollment/revocation control plane is intentionally the fixed
two-device preview. The relay's `maxActiveDevices` guard, the exact signed-roster
checks in YPCB validation, disabled recovery, and disabled general revocation
are security controls. A third-device registration/pairing attempt must surface
the stable relay conflict and leave local credentials, roster, outbox, cursor,
and snapshots unchanged.

Do not raise or bypass the current server constant to make a mobile enrollment
demo pass. Enrollment beyond the preview remains disabled until a monotonically
versioned signed roster-chain protocol provides authenticated add/revoke/replace
transitions, downgrade and fork rejection, durable migration, and cross-device
acceptance tests. Relay `/v1/devices` output alone never enables a device.

## 5. Recovery is out of scope and fail-closed

The mobile clients expose no account-recovery route, recovery-key generator,
recovery-key import, recovery-authentication request, reset-to-escape flow, or
recovery secret display. The reserved relay recovery endpoint remains disabled.
Ordinary login, claim, pairing, synchronization, upgrade, and rollback must not
prompt for recovery material.

If an existing credential cannot be authenticated or migrated, the client
preserves it, reports a redacted blocked state, and stops. It must not create a
replacement account, rotate keys, delete cloud/local data, or silently reset the
profile.

## 6. Containing app and system keyboard boundary

The Android containing app and iOS containing app own:

- runtime relay selection and endpoint-policy validation;
- secure storage and bounded use of a separately delivered, already-paired
  opaque device credential;
- any future login, pairing/enrollment, and signed-roster verification path;
- Keystore/Keychain access and encrypted local database lifecycle;
- outbox, Protocol v1 transport, cursor processing, CRDT merge, retry/backoff,
  background scheduling, snapshot publication, status, and diagnostics.

The Android `InputMethodService` and iOS keyboard extension own only:

- loading the last validated immutable local snapshot;
- bounded, network-free candidate queries and composition state;
- committing a minimal local learning event through a bounded app-owned handoff
  when the platform/privacy context permits it;
- fail-closed secure-field, one-time-code, private/incognito, and unavailable
  shared-container behavior.

This first slice implements app-owned manual mutation entrypoints but no
keyboard-to-app learning handoff and no login/pairing UI. Those future paths
must remain in the containing app and pass the same privacy and roster gates;
the keyboard must never gain direct credential, queue, or network access.

The shared candidate C ABI remains version `1`. Its protected-context bitset is
the cross-platform fail-closed boundary for password fields, private/incognito
mode, one-time inputs, no-personalized-learning contexts, and an unavailable
shared snapshot. An unknown bit is invalid; any known protected bit returns zero
private candidates without inspecting the snapshot.

The keyboard/extension does not own an HTTP client, relay endpoint, login
session, device bearer, private key, pairing API, recovery API, database
migration, or background scheduler. A key event never waits for disk or network.
On iOS, `RequestsOpenAccess=false` is the first-slice default. In that mode the
extension cannot use the containing app's shared container, so it returns zero
private candidates rather than weakening the sandbox. A future build that
requests App Group access requires a separate privacy review and explicit user
Full Access; even then the extension remains read-only and the containing app
remains the only network actor.

## 7. Background work, observability, and rollback

Android uses the native `JobScheduler` for persisted periodic and
constraint-aware one-shot work. iOS uses BGTask where granted plus
app-activation catch-up. Neither platform promises
continuous or exact-period execution. All attempts are bounded, single-flight,
cancellable, and use capped exponential backoff with jitter. Offline or OS-
deferred work leaves the outbox and last-good snapshot intact.

User-visible status may include a redacted state, last-success time, pending
count, attempt/failure category, cursor progress, and whether more pages remain.
Logs and telemetry exclude phrase/Pinyin text, input events, snapshots, request
bodies, ciphertext, identifiers, endpoint origins, credentials, keys, passwords,
pairing invitations, and clipboard contents. Production diagnostics are bounded
and local unless the user explicitly exports a redacted report.

This slice creates only the initial encrypted database schema and performs no
database or credential migration. Before the first schema change, the client
must add an explicit schema version, a forward migration, preservation of the
prior compatible credential/database, and rollback tests; that future work is
not claimed here. The implemented upgrade gate validates the shared core and
the exact current snapshot, publishes snapshots only after validation, and may
restore only a matching last-known-good snapshot without discarding the
encrypted outbox or moving the cursor backward. Any irreversible migration or
server-side mutation is a separate reviewed release gate.

## 8. Repository and test privacy boundary

Only synthetic, non-identifying data may appear in mobile tests. The repository
and mobile artifacts exclude personal dictionaries, third-party private
dictionary exports, input replay traces, credentials, live pairing material,
private imports, absolute user paths, deployment host aliases, and live server
addresses. Tests must continue to scan for these categories and for common
secret literal formats.

## 9. Human gates

The following are intentionally not automated by this implementation task:

- developer certificates, signing identities, provisioning profiles, and store
  accounts;
- physical-device trust/authorization and enabling a system IME or keyboard
  extension;
- iOS Full Access or any other privacy-sensitive system permission;
- external publishing, TestFlight/Play distribution, notarization, or rollout;
- enrollment against or mutation of a live relay, migration of an existing
  account/roster, and any live infrastructure change.

Crossing one of these gates requires an explicit human action and a separate
acceptance record. Simulator/unit-test success is not evidence that a system
keyboard is enabled, background delivery is guaranteed, or a live account was
modified.
