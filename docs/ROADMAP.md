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
- Schema-local `yunpin_corrector` for deterministic QWERTY-neighbour,
  missing-key, extra-key, adjacent-transposition and reviewed `you` → `yao`
  recovery. The exact librime 1.16/1.17 compatibility patches are version and
  hash locked, and real merged-Rime synthetic E2E protects short exact input
  plus a 50× exact-prefix collision while enforcing a 20 ms
  corrected-final-key P95 gate. The adapter exposes at most 16 deterministic
  correction edges per input offset.
- Desktop-agent skeleton for non-synchronizing macOS Keychain, current-user
  Windows DPAPI, local status, one-shot sync and single-instance retry loop;
  production account creation remains fail-closed.
- Weasel/Squirrel configuration overlays and pinned upstream strategy.
- Source-only and explicitly unsigned development builds.

## Preview 0.2: typing quality and encrypted clipboard sync

### Typing quality

- Run the same correction/collision suite against the locked production Rime
  Ice/public packs and a 50,000-entry synthetic personal index. Report exact
  candidates displaced, page pollution, candidate count, memory and final-key
  P50/P95/P99; the current small synthetic E2E is not this acceptance result.
- Keep short exact Pinyin ahead of correction results. Stock librime gives all
  correction edges the same fixed `log(0.01)` credibility, so use production
  measurements to decide whether a separately reviewed custom translator or a
  further version-locked scoring patch is needed. Do not treat generator edit
  cost as a production ranking score. Retain the `(spelling ID, consumed
  length)` exact-prefix regression and the 16-edge-per-offset graph budget.
- Expand reviewed valid-syllable confusions only through corpus evidence,
  explicit negative tests and one-way review. Keep each per-syllable variant
  one edit and the overall search bounded; measure multi-error long phrases
  separately.
- Prototype Chinese-English mixed-input segmentation, English passthrough and
  boundary ranking. This is a direction only; no current preview capability or
  acceptance claim follows from this roadmap item.
- Consider a local model only as an optional, default-off offline sidecar after
  the deterministic baseline is measured. It must have no network access, a
  minimal bounded IPC payload, a strict deadline and fail-closed fallback that
  preserves exact/deterministic results when the process times out or crashes.

Typing-quality acceptance requires the locked production-dictionary suite on
both desktop runtimes, no short-exact regression, no network or disk wait in a
key event, and documented collision/latency budgets. The current synthetic run
measured corrected-final-key P95 ranges of 534–841 µs for the two-error
`shouxubijiakuaideshihou` case and 907–1611 µs for the 37-byte reviewed
`you` → `yao` case across two independent runs, but those figures are evidence for that fixture and machine,
not production guarantees.

### Encrypted clipboard sync

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

Clipboard acceptance requires encrypted Windows↔macOS transfer, offline/duplicate replay
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
