# YunPin desktop sync client

This Apache-2.0 Go module is the reusable, headless background path between a
desktop adapter, `localstore`, `protocol`, and the opaque relay. It is not
called from a keyboard event handler.

The API client creates or recovers accounts and strictly transports the
recovery-encrypted sealed-box keyring. The worker durably stages the exact
signed ciphertext before upload, retries a
lost response idempotently, maintains the per-device sequence/hash chain,
downloads by cursor, verifies each envelope against pairing-authenticated
Ed25519 keys, decrypts locally, merges the phrase CRDT, then advances the cursor
and acknowledges only the exact outbox version sent. Tokens and private keys
are caller-owned and never persisted by this module.

Desktop endpoint configuration contains no credential. A NAS endpoint such as
`http://192.168.1.127:8787` requires the explicit private-LAN exception:

```json
{
  "endpoint": "http://192.168.1.127:8787",
  "allow_private_http": true
}
```

Save this security-sensitive file with user-only write permissions (`0600` on
macOS). Public HTTP, HTTP hostnames other than `localhost`, URL credentials,
redirects, paths, query strings, fragments, writable/symlinked replacements and
unknown configuration fields are rejected. Prefer HTTPS even on a private
network. A native adapter will load
this endpoint-only file from `%LOCALAPPDATA%\\YunPinIME\\sync.json` on Windows
or `~/Library/Application Support/YunPin/sync.json` on macOS.

Still required before desktop sync can be called complete:

- Windows DPAPI/Credential Manager and macOS Keychain session adapters;
- authenticated pairing/keyring persistence and rotation UI;
- a single-instance scheduled background runner with retry/backoff;
- native learning-event ingestion plus atomic private snapshot rebuilding and
  Rime reload after a successful merge;
- password/private-mode host integration and signed release acceptance.
