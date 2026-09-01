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

The selected relay is an endpoint-only profile setting. `Register` and
`Login` exchange a user password for an opaque bounded session; callers keep
that session in the platform secret store and pass it only through
`WithUserSession` for account creation or account claim. Device bearer tokens
continue to authorize routine encrypted sync, so a user-password session is
not attached to phrase data uploads or stored in endpoint JSON.

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

`desktopagent` now supplies the platform storage boundary (current-user Windows
DPAPI or the macOS atomic private-file store, including one-time interactive
legacy-Keychain migration), authenticated pairing/keyring persistence, the
single-instance resident with retry/backoff, native learning-event ingestion,
and atomic private snapshot rebuilding and reload.

The following installed-host acceptance gates remain before desktop sync can
be called complete:

- real Weasel `0010` lifecycle evidence on Windows, including exactly one
  windowless resident and no foreground agent;
- live two-device pairing/finalization, encrypted transfer, snapshot reload,
  and candidate visibility with rollback evidence on both installed hosts;
- trustworthy password/private-mode host integration; and
- exact signed release/installer acceptance on each supported platform.
