<!-- SPDX-License-Identifier: Apache-2.0 -->

# YunPin encrypted sync relay

This service is a deliberately content-blind relay. Clients encrypt phrase CRDTs and keyring records before upload. SQLite contains random account/device/object identifiers, sequence numbers, cursors, token hashes, public keys, nonces, signatures, and opaque ciphertext only.

## Run

```bash
go run ./cmd/yunpin-sync
```

Environment variables:

- `YUNPIN_LISTEN` defaults to `:8080`.
- `YUNPIN_DATABASE` defaults to `/data/yunpin-sync.db`.

The production image runs as a non-root distroless user. Put TLS and coarse network rate limiting at a reverse proxy; the application additionally enforces a 1 MiB JSON limit, per-envelope/keyring limits, a per-IP fixed-window limit, strict JSON fields, and HTTP timeouts. The in-process limiter trusts only the TCP peer in `RemoteAddr` and deliberately ignores `X-Forwarded-For` and similar spoofable headers. Behind a reverse proxy it therefore sees the proxy address: configure real-source handling at a trusted network layer and enforce client-aware limits at the proxy before forwarding. Expired application limiter entries are swept so inactive source addresses do not accumulate indefinitely.

`./scripts/smoke-container.sh` builds the image, starts it with its default non-root user and anonymous `/data` volume, checks `/healthz`, and removes the test container.

The Dockerfile exposes explicit `test` and `runtime` targets. The complete HTTP contract is in [`openapi.yaml`](openapi.yaml).

## API summary

| Method | Path | Authentication | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | none | Database liveness |
| `POST` | `/v1/auth/register` | none | Create a self-hosted username/password login and return an opaque session |
| `POST` | `/v1/auth/login` | none | Create a bounded opaque session for a selected server |
| `POST` | `/v1/auth/logout` | user session | Revoke the current opaque session |
| `POST` | `/v1/accounts` | user session | Create an account and its first device |
| `POST` | `/v1/accounts/{id}/claim` | user session + recovery authentication | Bind a pre-login encrypted account to its owner |
| `DELETE` | `/v1/accounts/{id}` | short-lived rollback capability | Roll back an otherwise-unused, unsealed new account |
| `POST` | `/v1/accounts/{id}/seal` | provisioning device token | Seal the first device after durable local commit |
| `POST` | `/v1/accounts/{id}/recover` | recovery authentication | Reserved; fail-closed in the fixed two-device preview |
| `POST` | `/v1/pairings` | bearer device token | Create a 10-minute one-time pairing |
| `GET` | `/v1/pairings/{id}` | creating device token | Read state and pending public keys |
| `PUT` | `/v1/pairings/{id}` | PSK-derived relay verifier | Submit a client-generated device ID, public keys, and join proof |
| `POST` | `/v1/pairings/{id}/approve` | bearer device token | Approve, attach an encrypted keyring, and open a 24-hour claim window |
| `POST` | `/v1/pairings/{id}/claim` | verifier + joining-device signature | Within the claim window, atomically install the client-generated device token and claim the opaque package |
| `POST` | `/v1/pairings/{id}/ready` | joining device token | Acknowledge durable local credential, database, and signed-roster commit within 24 hours of claim |
| `POST` | `/v1/pairings/{id}/finalize` | creating device token | Finalize a ready pairing and release the joining device from quarantine |
| `DELETE` | `/v1/pairings/{id}` | creating device token | Idempotently cancel a pairing before the joining device reports ready |
| `POST` | `/v1/sync` | bearer device token | Idempotent upload and cursor-based download |
| `GET` | `/v1/devices` | bearer device token | List devices |
| `DELETE` | `/v1/devices/current` | joining rollback capability + exact account/device/pairing tuple | Roll back a joined, approved, or pre-ready claimed tuple after a failed local commit |
| `DELETE` | `/v1/devices/{id}` | bearer device token | Reserved; fail-closed until signed roster replacement exists |
| `GET`, `PUT` | `/v1/keyring` | bearer device token | Fetch or publish opaque key epochs |

Device IDs and tokens are generated client-side; the relay stores only token SHA-256 digests. Human account login is separate from device credentials and never requires a recovery prompt during ordinary setup, claiming, or synchronization.

## User login and selectable relay

The relay is selected per desktop profile, not compiled into the input method.
`configure-server --endpoint <HTTPS URL>` writes only the selected endpoint and
its explicit private-HTTP policy. `register --username <name>` and
`login --username <name>` read the password privately from the terminal; the
password is never accepted as a command argument or stored on either desktop.
The relay stores a salted PBKDF2-SHA-256 verifier and hashes every opaque
30-day session. The session itself is held only in macOS Keychain or
current-user Windows DPAPI.

Daily synchronization authenticates with the per-device bearer and signed
roster; it does not require a long-lived user-password session. Existing
pre-login accounts are adopted once with
`claim-account --confirm-claim-existing-account`: the already protected local
active device proves the claim while the logged-in user session selects the
owner. No recovery key, password, or extra terminal input is requested.

Device display names are supplied and returned as client-encrypted `device_name_ciphertext`, never plaintext. Relay device listings are operational metadata and are never a trust root. Each credential persists the creator-signed, versioned roster delivered inside the encrypted pairing package; private keys never leave DPAPI/Credential Manager or Apple Keychain.

This preview is intentionally fixed to exactly the Mac and R0W peers. Recovery
device creation, a third pairing, and general device revocation fail closed;
only rollback of an unfinalized second-device joining tuple (`joined`,
`approved`, or pre-ready `claimed`) is available. Expanding or replacing the
roster requires a future signed roster-chain protocol rather than trusting
relay device listings.

Pairing v2 begins with a client-generated 16-byte pairing ID and 32-byte PSK. The QR/private handoff carries the PSK plus the creator account/device IDs and Ed25519/X25519 public keys; the raw PSK is never sent to the relay. The relay stores only a domain-separated HMAC verifier. The joining client generates its device ID, token, keys, and an HMAC proof over the complete transcript. The creator verifies that proof, signs a monotonically versioned trust roster, and encrypts the roster plus epoch/object keys using `HKDF(PSK || X25519, transcript_hash)` with the complete transcript as AEAD AAD. Claim additionally requires the joining Ed25519 private key to sign the transcript and device-token hash. Relay key substitution, database-verifier theft, and response loss therefore fail closed or replay idempotently without revealing the package.

The visible lifecycle is `created -> joined -> approved -> claimed -> ready -> finalized`. Join and the first approval must complete during the original ten-minute invitation. Approval opens a separate 24-hour claim deadline. A successful claim installs the joining identity in quarantine and opens another 24-hour `ready_expires_at` deadline. Until the joining client has durably committed its OS credential, encrypted local database, and creator-signed roster, it must not call `ready`. Its ordinary bearer-authenticated account APIs return HTTP 409 `pairing_finalization_pending` both before and after `ready`. The creator finalizes only after observing `ready`; finalization has no additional deadline, and only then may the joining device use normal sync, device, pairing, and keyring APIs. `expires_at` and `expired` in pairing status always describe the original invitation, not the later claim or ready windows.

The creator may cancel `created`, `joined`, `approved`, or not-yet-ready `claimed` state. An untouched `created` invitation has no joining tuple and is deleted directly. Cancellation of `joined` or `approved` writes a permanent hash-only tombstone for the exact account/device/pairing/capability tuple before deleting the reservation. Cancellation of `claimed` writes the same tombstone and atomically removes the quarantined joining device. That tombstone makes both a lost creator-cancel response and a later exact joining-rollback retry return HTTP 204 without resurrecting either identity. Repeating a successful cancellation returns HTTP 204; cancellation after `ready` fails with HTTP 409 `pairing_cancel_not_safe`.

Pairing mutations are serialized within one relay process and use SQLite immediate write transactions across connections/processes. Concurrent creator cancellation and joining rollback therefore have one linear outcome: both callers receive HTTP 204, and the database converges on no pairing, no provisional joining device, and exactly one matching tombstone. A committed `ready` or `finalized` transition wins instead and makes rollback fail closed.

Exact create retries are accepted only while the original invitation remains unexpired and its persisted state is no later than `approved`; retry after invitation expiry or claim returns `pairing_invitation_conflict`. Exact join, approval, and claim retries remain idempotent at later visible states and return their original transition response. A repeated `ready` returns `ready`, or `finalized` if the creator has already finalized; repeated finalization returns `finalized`. A first `ready` after its deadline fails with HTTP 409 `pairing_ready_window_expired`, and finalization before `ready` fails with HTTP 409 `pairing_not_ready_to_finalize`. Conflicting replay material fails with HTTP 409, while invalid or expired verifier/proof material fails with HTTP 401. Clients preserve the HTTP status and stable relay code in `syncclient.APIError` and must retry only the same operation with byte-identical identity and cryptographic material.

Provisioning failures are reversible only while they are still provably empty.
Account rollback requires one device and no pairing or envelope. Joining
rollback is authorized by the dedicated 32-byte rollback capability plus the
exact `account_id`, `device_id`, and `pairing_id`; it never depends on a normal
device bearer, because no device row exists yet in `joined` or `approved`.
Those pre-claim states may be rolled back atomically. A `claimed` rollback also
requires another active device, exactly one matching claimed pairing, and no
envelope, keyring, or pairing written by the joining device. An untouched
`created` invitation, an absent row without an exact tombstone, a wrong tuple,
or a wrong capability fails closed with HTTP 409 `device_rollback_not_safe`.
Once `ready_at` or `finalized_at` is present, rollback always fails without any
device, pairing, or tombstone mutation with HTTP 409
`device_rollback_after_ready`. Successful rollback leaves only a persistent
identity-and-capability hash tombstone. It contains no plaintext token, phrase,
or key, makes Join/Claim/DELETE response-loss recovery idempotent, and
permanently prevents the retired random identity from being resurrected.

Encrypted keyrings use the protocol's complete `YPBX` v1 sealed-box wire representation, not a bare AEAD ciphertext. Both `/approve` and `/v1/keyring` strictly validate its public magic, version and declared length while treating the nonce and ciphertext as opaque. Their shared decoded complete-blob maximum is 256 KiB.

## Signed envelope format

The relay reconstructs the exact `protocol.Header` from upload fields and bearer identity. Its canonical CBOR map uses integer keys `1..8` for version, 16-byte account ID, 16-byte opaque object ID, key epoch, 16-byte device ID, device sequence, optional 32-byte previous record hash, and 24-byte nonce. The Ed25519 message is `canonical_header_cbor || ciphertext`, exactly matching `protocol/crypto.go`. Payload ciphertext must contain one or more 512-byte padded buckets plus the 16-byte XChaCha20-Poly1305 tag.

Account and upload-device IDs are inferred from the bearer token and cannot be overridden in a request. `kind`, HLC, phrases, pinyin, counts, and settings never appear outside ciphertext. Downloads include the source `device_id` so clients can reconstruct the header and verify it.

For sequence 1, `previous_hash` is absent. Later records use `SHA-256(canonical_header_cbor || ciphertext || signature)` of the immediately preceding record from that device. The relay rejects gaps and incorrect chain links. An exact `(device_id, device_seq)` replay is accepted idempotently; reuse with different signed bytes appears in `rejected_sequences` as `sequence_conflict`.

`POST /v1/sync` accepts at most 256 upload envelopes and a 1 MiB JSON body. Canonical plaintext is capped at 512 KiB, so decoded envelope ciphertext is capped at 524816 bytes on both client and relay. It returns `accepted_sequences`, explicit `rejected_sequences`, downloaded envelopes, `next_cursor`, `has_more`, and `current_key_epoch`. Each download page is bounded by both 256 records and 524816 cumulative decoded ciphertext bytes; `has_more` remains true when either limit cuts the page. `ack_cursor` records per-device progress for future retention policies.

Embedded SQL migrations are applied transactionally and recorded in `schema_migrations` with a SHA-256 checksum. Startup is idempotent and refuses to continue if an already-applied migration's bytes no longer match its ledger entry.
