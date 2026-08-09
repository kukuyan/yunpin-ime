# Roadmap

## Preview 0.1

- Shared ranking/recall engine and golden tests.
- Offline private-vocabulary importer with sensitive-data preview.
- Opaque encrypted-envelope sync API and Docker Compose.
- Reusable headless desktop sync worker with durable idempotent upload state,
  verified/decrypted download merge, and strict endpoint configuration.
- Read-only librime private overlay, independent short-input upstream guard and
  bounded word-scoped session correction connected to librime notifiers, with
  protected-context filtering and first-eight stable reranking.
- Desktop-agent skeleton for non-synchronizing macOS Keychain, current-user
  Windows DPAPI, local status, one-shot sync and single-instance retry loop;
  production account creation remains fail-closed.
- Weasel/Squirrel configuration overlays and pinned upstream strategy.
- Source-only and explicitly unsigned development builds.

## Preview 0.2: encrypted clipboard sync

- Add an opt-in background clipboard agent for Windows and macOS. Clipboard
  capture, encryption, transport and replay remain outside the IME key-event
  path, so an unavailable server cannot delay typing.
- Synchronize UTF-8 text first, with a 256 KiB item limit, origin/sequence loop
  suppression and explicit per-device pause, revoke and clear controls. Images,
  files and rich clipboard formats remain out of scope for the first iteration.
- Derive a separate clipboard encryption key from the account root key. Reuse
  XChaCha20-Poly1305 envelopes and Ed25519 device signatures, but never reuse a
  vocabulary key or nonce. The server sees only ciphertext, expiry metadata and
  opaque device identifiers.
- Keep server retention short and bounded, keep no durable clipboard history by
  default, and never place clipboard contents in logs, crash reports, Git, test
  fixtures or analytics.
- Suppress automatic upload for private mode, configured password-manager/source
  applications, private keys, authentication headers, credential-bearing URLs
  and known token formats. Provide a deliberate “send this clipboard” action
  for content the user chooses to share.
- On iOS, the host app performs encrypted sync and writes an App Group snapshot;
  the keyboard extension stays offline and reads the snapshot. Sending the
  current iPhone pasteboard requires a user-intent action such as a paste control,
  share action or Shortcut. UIKit reports or prompts on pasteboard reads without
  user intent, so this phase does not claim Apple-private Universal Clipboard
  parity. See [UIPasteboard](https://developer.apple.com/documentation/uikit/uipasteboard),
  [UIPasteControl](https://developer.apple.com/documentation/uikit/uipastecontrol)
  and [custom keyboard open access](https://developer.apple.com/documentation/uikit/configuring-open-access-for-a-custom-keyboard).

Acceptance requires encrypted Windows↔macOS transfer, offline/duplicate replay
without clipboard loops, expiry and remote clear, negative tests for protected
content, and an iOS user-driven send/receive flow that works without giving the
keyboard extension network access.

## Desktop alpha

- Integrate the engine as a merged librime plugin.
- Build Weasel x86/x64 TSF components plus x64 service.
- Build Squirrel Universal arm64/x86_64 InputMethodKit package.
- Harden and package the Keychain/current-user DPAPI background agent, including
  rollback-safe account provisioning, authenticated device roster, pairing,
  recovery, revoke and key rotation.
- Strengthen the existing librime correction bridge with trusted native
  protected-context and host-deletion evidence, then connect its word-level
  aggregates and scores to encrypted local persistence and an explicit local
  habit-monitor UI/CLI.
- Rebuild immutable candidate snapshots after learning or sync, atomically swap
  generations and trigger Rime reload; transport-only tests do not satisfy this
  desktop gate.
- Complete installers, signed background-service registration and
  cross-application smoke tests.

## Deferred expression search and favorites

- Keep `expression_search` fail-closed: no action candidate, browser launch or
  favorite-file write is enabled in the development preview.
- Design an unforgeable typed/armed native action channel before restoring any
  action. Ordinary dictionary, import and synchronized text must remain data.
- Define provider/licensing, content-safety and confirmation policy, a bounded
  image/GIF object model, cache/delete behavior and a separate end-to-end
  encrypted favorite envelope before claiming a WeChat-like experience.

## Signed desktop release

- Windows Authenticode signing and installer verification.
- macOS Developer ID signing, notarization and stapling.
- Protected GitHub Release environment and reproducible source bundles.

## iOS phase two

- Independent Swift host app and custom keyboard extension.
- BSD/Apache-compatible librime integration; no GPL desktop source.
- App Group read-only snapshots without Full Access and opt-in learning writeback with Full Access.
