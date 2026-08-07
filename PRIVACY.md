# Privacy Model

YunPin separates public language data, local personal data, and opaque synchronized data.

## What stays local

- Original keystrokes and surrounding sentences.
- Application names and clipboard contents.
- ChatGPT/Codex exports and source documents.
- Sogou exports, backups, proprietary databases, and conversion copies.
- Device private keys and the human recovery key.

The local importer retains only reviewed phrases, normalized Pinyin, a source category, and a coarse usage count. It filters code blocks, URLs, IP addresses, emails, paths, credentials, tokens, long identifiers, and sentence-like fragments. It does not save source sentences.

## What the server may store

- Random account and device identifiers.
- Device public keys and token hashes.
- Monotonic cursors, device sequence numbers, key epochs, ciphertext sizes, and timestamps.
- XChaCha20-Poly1305 ciphertext envelopes and Ed25519 signatures.

Request bodies are never logged. Reverse proxies must also disable body logging. The service cannot conceal IP addresses, traffic timing, or ciphertext size.

## Repository rule

No real personal vocabulary, source conversation, export archive, database, key, or token belongs in this repository, CI cache, test fixture, release artifact, or log. Tests use generated data and one explicitly documented public organization-name acceptance string.
