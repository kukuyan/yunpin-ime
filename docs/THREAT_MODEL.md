# Threat Model

## Protected against

- Passive theft of the server database or backup revealing phrases or Pinyin.
- A curious service operator reading opaque envelopes.
- Duplicate and out-of-order delivery changing converged counters.
- A revoked token continuing to upload or download new envelopes.
- Accidental inclusion of known personal-data file types in Git or releases.

## Not protected against

- A compromised endpoint reading the user's local unlocked dictionary.
- Traffic analysis of IP address, timing, and padded ciphertext size.
- A malicious server denying service, deleting ciphertext, or serving stale but valid data.
- A user intentionally exporting decrypted vocabulary.

## Required controls

- XChaCha20-Poly1305 payload encryption with unique 192-bit nonces.
- Ed25519 signatures over canonical header plus ciphertext.
- X25519 device pairing and OS-backed device-key storage.
- Token hashing at rest, bounded requests, no request-body logging, and TLS at the edge.
- Recovery-key display only at creation/recovery and explicit key rotation after device loss.
