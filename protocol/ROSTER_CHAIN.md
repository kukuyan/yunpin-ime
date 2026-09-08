# Add-only signed device roster

This implements trusted-device additions, not revocation, device replacement,
epoch rotation, or recovery after losing every trusted device. The tested
resource limit is **128 devices per account**, not a two-device pairing limit.
Public network reachability and device enrollment are separate requirements.

## Trust and compatibility

- Existing two-device `PairingRoster` v1 canonical signing bytes and signatures
  are unchanged. A current device's protected checkpoint is the trust anchor;
  `/v1/devices` is never imported as verification trust.
- An addition contains the complete next roster, version exactly `previous+1`,
  and the SHA-256 digest of the exact signed previous roster. The predecessor
  is covered by a separate `yunpin-pairing-roster-chain-v1` signature domain.
  An existing trusted member must sign; every existing ID and both public keys
  must remain unchanged; exactly one new member is allowed.
- The relay stores only finalized public signed rosters. Approval's encrypted
  grant is not a published checkpoint. A reserved base version/digest plus the
  existing per-account reservation gate prevent competing additions. The
  ready-to-finalized transition and roster publication commit in one SQLite
  transaction. The new device cannot use ordinary sync before this commit.
- An existing two-device client can publish its unchanged signed anchor after
  the relay's additive migration. The relay checks it against enrolled keys;
  peers still verify from their own local anchor, not the relay's assertion.
- Chained checkpoints use YPCB v3. Existing account/device IDs, bearer, signing
  seed, X25519 private key, local data key, object ID key and epochs are kept.
  A new device has its own identity through the existing approved pairing flow.
  No existing private identity is copied to another device or rotated.
- Old v2 clients reject v3 credentials and cannot create/finalize a third-device
  pairing using legacy requests. Upgrade all participating clients before
  adding devices; an old peer cannot authenticate uploads from unknown members.

## Background integration

Under the existing desktop process lock, call `RefreshTrustedRoster` with the
resident's in-memory bundle before constructing a sync worker session. It
verifies a continuous signed chain, then uses the existing exact protected-store
replacement before changing memory. Failed verification or persistence must
stop the round before cursor advancement. An unchanged checkpoint performs no
protected credential load/save. Pairing journals block an unrelated trust
advance until their own state machine is resumed or cancelled safely.

GET pages contain at most 16 updates. Refresh has a 32-page / 8-MiB total budget
in addition to the client's 2-MiB individual HTTP response cap; duplicate,
out-of-order, truncated, no-progress and mismatched-head responses are rejected.
The maximum-size test catches up the full 128-device chain in eight pages.

## Durable intent, cancellation and rollback

The creator journal stores the exact original credential alongside the proposed
credential and sealed grant, inside the existing 64-KiB protected slot. The
worst-case test includes 128 members, 64 epochs, a 512-byte token and maximum
invitation length. No secret-slot read bound is widened. Legacy journals without
an original copy may reconstruct only their original first-pairing self-only
credential, never a multi-device checkpoint.

Before finalization intent, cancellation preserves the exact previous roster.
Immediately before the finalize request the creator durably records
`FinalizationPending`. After that, **resume the same pairing; do not cancel,
delete its journal, synthesize a new attempt, or restore an older trust head**.
This applies to lost responses and explicit conflict responses: a client must
not infer from a response alone that its already-signed addition was never
published. A retry completes when the recorded tuple and chain remain valid.
If a persistent CAS conflict prevents that, stop for authenticated state
reconciliation; automatic rollback/revocation is not implemented.

Migration `005_signed_roster_chain.sql` is additive. Deploy and verify the relay
before upgraded clients need the roster endpoint. Binary rollback before any
new addition can retain the original v2 checkpoint. After a v3 checkpoint is
published, rolling a client back to a v2-only binary is not a supported rollback;
retain the known-good v3 binary and exact journals and resolve the recorded
transaction without downgrading trust or regenerating credentials.

## Executable coverage

- Protocol and independently built relay share fixed canonical signature/digest
  goldens; signed old-key replacement, new-member signer, removal, wrong account,
  wrong predecessor, skipped generation and signature tampering are rejected.
- Desktop tests cover protected save/CAS failure, unchanged resident state on
  failure, pending journals, bad pagination, exact previous-roster restoration,
  and worst-case protected-journal / offline-chain bounds.
- `integration/roster_chain_test.go` drives real HTTP relay plus desktop pairing
  state machines through four devices, including approval by a non-first trusted
  device, unpublished grants, legacy bypass rejection, reservation conflict,
  cancelled third-device approval, lost and conflict finalize replies, offline
  signed-chain refresh, and four real stores/workers converging learning and
  deletion. OS stores are synthetic in-memory fixtures; this is not real-device
  UI/candidate, background authorization, or public-network deployment acceptance.

Logs add only the fixed `/v1/roster` route label. Tokens, roster hashes, dynamic
member identifiers, query strings and encrypted grants must not be logged.
