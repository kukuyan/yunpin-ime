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
| `POST` | `/v1/accounts` | none | Create an account and its first device |
| `POST` | `/v1/accounts/{id}/recover` | recovery authentication | Add a recovery device |
| `POST` | `/v1/pairings` | bearer device token | Create a 10-minute one-time pairing |
| `GET` | `/v1/pairings/{id}` | creating device token | Read state and pending public keys |
| `PUT` | `/v1/pairings/{id}` | pairing secret | Submit the new device public keys |
| `POST` | `/v1/pairings/{id}/approve` | bearer device token | Approve and attach an encrypted keyring |
| `POST` | `/v1/pairings/{id}/claim` | pairing secret | Claim a new one-time device token |
| `POST` | `/v1/sync` | bearer device token | Idempotent upload and cursor-based download |
| `GET` | `/v1/devices` | bearer device token | List devices |
| `DELETE` | `/v1/devices/{id}` | bearer device token | Revoke another device |
| `GET`, `PUT` | `/v1/keyring` | bearer device token | Fetch or publish opaque key epochs |

Device tokens are returned once and stored only as SHA-256 digests. The client creates the 256-bit human recovery key, presents it as checksummed `yprec1…` text/QR, and derives `recovery_authentication` with HKDF-SHA-256 domain `yunpin-recovery-authentication-v1`. Only the SHA-256 of that 32-byte authentication output is stored; the human recovery key and the recovery encryption key are never sent to or derived by the relay. Pairing secrets are likewise hash-only.

Device display names are supplied and returned as client-encrypted `device_name_ciphertext`, never plaintext. Device listings include Ed25519 and X25519 public keys so clients can verify downloaded records and pair safely; private keys never leave DPAPI/Credential Manager or Apple Keychain. Clients should encode the account ID and pairing ID/secret in a QR code.

The create response includes the creator's X25519 public key for the one-time pairing QR; the decoded 24-byte `pairing_secret` also serves as the protocol session nonce. The creating device then polls authenticated `GET /v1/pairings/{id}`. Once state is `joined`, the response contains only the pending encrypted name and Ed25519/X25519 public keys. Both devices can now derive the same X25519 session key. The creator encrypts the keyring locally and submits that opaque box to `/approve`; the relay never derives the pairing key.

Encrypted keyrings use the protocol's complete `YPBX` v1 sealed-box wire representation, not a bare AEAD ciphertext. Both `/approve` and `/v1/keyring` strictly validate its public magic, version and declared length while treating the nonce and ciphertext as opaque. Their shared decoded complete-blob maximum is 256 KiB.

## Signed envelope format

The relay reconstructs the exact `protocol.Header` from upload fields and bearer identity. Its canonical CBOR map uses integer keys `1..8` for version, 16-byte account ID, 16-byte opaque object ID, key epoch, 16-byte device ID, device sequence, optional 32-byte previous record hash, and 24-byte nonce. The Ed25519 message is `canonical_header_cbor || ciphertext`, exactly matching `protocol/crypto.go`. Payload ciphertext must contain one or more 512-byte padded buckets plus the 16-byte XChaCha20-Poly1305 tag.

Account and upload-device IDs are inferred from the bearer token and cannot be overridden in a request. `kind`, HLC, phrases, pinyin, counts, and settings never appear outside ciphertext. Downloads include the source `device_id` so clients can reconstruct the header and verify it.

For sequence 1, `previous_hash` is absent. Later records use `SHA-256(canonical_header_cbor || ciphertext || signature)` of the immediately preceding record from that device. The relay rejects gaps and incorrect chain links. An exact `(device_id, device_seq)` replay is accepted idempotently; reuse with different signed bytes appears in `rejected_sequences` as `sequence_conflict`.

`POST /v1/sync` accepts at most 256 upload envelopes and a 1 MiB JSON body. Canonical plaintext is capped at 512 KiB, so decoded envelope ciphertext is capped at 524816 bytes on both client and relay. It returns `accepted_sequences`, explicit `rejected_sequences`, downloaded envelopes, `next_cursor`, `has_more`, and `current_key_epoch`. Each download page is bounded by both 256 records and 524816 cumulative decoded ciphertext bytes; `has_more` remains true when either limit cuts the page. `ack_cursor` records per-device progress for future retention policies.

Embedded SQL migrations are applied transactionally and recorded in `schema_migrations` with a SHA-256 checksum. Startup is idempotent and refuses to continue if an already-applied migration's bytes no longer match its ledger entry.
