# YunPin Sync Protocol v1

The server transports opaque envelopes. Clients own encryption, signatures, CRDT merge, and local index rebuilding.

The Go reference also implements checksummed recovery text, encrypted recovery packages, domain-separated recovery authentication, and X25519-derived one-time pairing boxes. A QR code transports the recovery text or pairing session payload; it never changes the cryptographic encoding.

## Identifiers and keys

- Account and device identifiers are random 128-bit values.
- Each device has X25519 pairing and Ed25519 signing key pairs.
- The recovery key is 256 random bits encoded as checksummed `yprec1...` text and QR.
- HKDF-SHA-256 separates recovery encryption/authentication material.
- An epoch data key encrypts phrase/settings objects; a distinct ID key derives stable opaque object IDs.

`opaque_object_id = HMAC-SHA256(K_id, canonical_phrase || 0x00 || canonical_pinyin)[0:16]`，其中 `canonical_phrase` 是移除 Unicode 空白后的 NFKC 文本；`canonical_pinyin` 与两个离线导入器共用同一规则（小写 ASCII、声调/数字移除、`u:`/`ü` 归一为 `v`、音节间单空格）。

## Envelope

The clear canonical header contains protocol version, account ID, object ID, key epoch, device ID, device sequence, previous record hash, and a random 192-bit nonce. Key epoch and device sequence are restricted to `1..MaxInt64`, matching SQLite and the JSON `int64` contract. The payload is canonical CBOR (at most 512 KiB), padded to a 512-byte bucket, and encrypted with XChaCha20-Poly1305 using the header as AAD. The device signs `header || ciphertext` with Ed25519. A valid ciphertext is therefore `n*512 + 16` bytes and at most 524816 bytes.

The server validates size, identity, monotonic sequence/idempotency, and signature metadata; it never receives plaintext or a data key.

## Sealed-box wire

Pairing approval and keyring fields carry one complete versioned sealed box as unpadded base64url. `EncodeSealedBox` and `DecodeSealedBox` are the public codec. After base64url decoding, the exact big-endian layout is:

```text
"YPBX" (4 bytes) || version (u8) || ciphertext_length (u32be) || nonce (24 bytes) || ciphertext
```

Version 1 requires an AEAD ciphertext of at least 16 bytes, rejects unknown versions, padding, truncation, declared-length mismatch and trailing data, and caps the complete decoded wire blob at 262144 bytes. Consequently `MaxSealedBoxCiphertextSize` is 262111 bytes after subtracting the 33-byte framing. The relay stores this framing opaquely and applies the same complete-blob limit to `/v1/pairings/{id}/approve` and `/v1/keyring`.

## Merge rules

- Per-device usage counts form a G-Counter and merge by component-wise maximum.
- Pinned state and settings use HLC-LWW. Alias synchronization is reserved for a later protocol extension; unknown setting keys remain opaque for forward compatibility.
- Existence/deletion uses remove-wins generations. A removal dominates every concurrent state in the same generation; counts do not resurrect it. Only an explicit re-add increments the generation and restores the object.
- Unknown settings are retained for forward compatibility.

## Sync

`POST /v1/sync` accepts a cursor, acknowledgement cursor, and at most 256 envelopes/1 MiB. Upload is idempotent on `(device_id, device_seq)`. The response contains accepted/rejected sequences, downloaded envelopes, `next_cursor`, `has_more`, and current key epoch. A download page is bounded by both 256 records and 524816 cumulative decoded ciphertext bytes.

On upload JSON, account/device IDs are omitted because the bearer token authenticates them. `version`, `device_seq`, lowercase-hex `object_id`, `key_epoch`, optional base64url `previous_hash`, base64url `nonce`, `ciphertext`, and `signature` are flat fields. Downloads additionally carry the source `device_id`, which selects the Ed25519 verification key and reconstructs the authenticated header. Clients use `Envelope.ToWire` for upload and strict `EnvelopeFromDownload(accountID, wire)` for download; missing, non-lowercase or malformed source IDs are rejected.

Endpoints also cover account creation/recovery, one-time pairing, device list/revocation, epoch keyring access/rotation, and health. See the OpenAPI document in `sync/openapi.yaml`.
