# Test and Acceptance Plan

## Ranking goldens

- Full Pinyin, compact Pinyin, explicit apostrophe boundaries, and ambiguous segmentation (`xian` versus `xi'an`).
- Initials and common fuzzy pairs, with negative checks that one to three initials cannot promote a pinned long phrase.
- For the `he` short-input case, retain one- and two-character candidates but
  filter three-or-more-character pure-CJK upstream predictions such as
  `合并为`; retain English, mixed and malformed text. Verify the same guard with
  Windows private snapshot injection disabled, then verify it becomes inert
  only when `short_input_guard` is also false.
- Require `hebing` before recalling a non-pinned three-syllable phrase and
  reject its short-initial form `hb`; preserve the four-initial escape hatch
  only for explicitly pinned long personal phrases.
- Pinned, demoted, learned-once, learned-twice, imported, public, base, and tombstoned candidates.
- At most two personal entries among the first eight.
- `中国石化销售股份有限公司河北石家庄石油分公司` is top three for `zhongguo...`, `zhongguoshihua...`, and `zgsh...`, and first for the complete Pinyin.
- 50,000 synthetic personal phrases, warm P95 no more than 20 ms.

## Deterministic Pinyin typo correction

- Unit-test the bounded generator for physical QWERTY neighbours, one missing
  key, one extra key, one adjacent transposition and the reviewed one-way
  `you` → `yao` confusion. Reject non-lowercase, one-letter and 128-byte-or-more
  adversarial segments; enforce the six-byte syllable and 768-raw-variant caps.
  After Prism validation/deduplication, assert deterministic ordering and no
  more than 16 correction edges at any input offset.
- Apply the exact version-locked component-selector/exact-classification patch
  to clean librime 1.16 and 1.17 trees, verify each locked SHA-256, and reject
  base-commit drift. Register `yunpin_corrector` only; assert that the global
  `corrector` is not replaced for other schemas. Verify exact matches are keyed
  by `(spelling ID, consumed input length)`, not spelling ID alone.
- Through the real merged ScriptTranslator, Dictionary, Prism and Rime C API,
  require all of these first-candidate results from synthetic public fixtures:
  exact `shousubijiaokuaideshihou`, neighbour-only `shouxubijiaokuaideshihou`,
  missing `shosubijiaokuaideshihou`, omitted `o` in
  `shousubijiakuaideshihou`, extra-key `shouusubijiaokuaideshihou`, transposed
  `shuosubijiaokuaideshihou`, reviewed
  `youjubeiyidingdejiucuolianxiangnengli`, and missing final key
  `zhuantai`. The original two-error input
  `shouxubijiakuaideshihou` must also return `手速比较快的时候` first and expose
  canonical spelling in its correction comment.
- Protect short exact input in the same real pipeline: `xu` must keep `需`
  first and `you` must keep `有` first. For `shangban`, include both exact
  “上班” and extra-key correction “山班”, give the correction 50 times the
  synthetic dictionary weight, and require exact before correction while
  retaining both candidates. This catches a regression where a corrected
  `shan` consuming five bytes inherits the exact four-byte-prefix match. Add
  production-dictionary collision probes before claiming this as a hard
  ordering guarantee for every exact entry; generator non-emission of an
  unchanged spelling alone is insufficient.
- For each corrected long performance probe, warm 10 times, then time only the
  final key and enumerate the resulting candidates for 100 samples. P95 must be
  no more than 20 ms. The completed merged-librime synthetic run measured
  534–841 µs for the two-error `shouxubijiakuaideshihou` input and
  907–1611 µs for the 37-byte reviewed `you` → `yao` input across two
  independent runs. Record future machine/runtime changes;
  these two numbers are not full Rime Ice or desktop-host guarantees.
- Run collision and pollution tests against the locked production public packs
  and a 50,000-entry synthetic personal snapshot. Stock librime applies one
  fixed `log(0.01)` credibility to every correction edge, so explicitly test
  whether dictionary/context weights let a corrected candidate displace an
  exact candidate beyond the existing 50× collision; YunPin edit cost is not a
  fine-grained ranking penalty.
- No correction test may open the network or read a model. A future optional
  local-model sidecar needs separate deadline, crash, malformed-output and
  offline tests proving timeout fails closed to existing exact/deterministic
  results without delaying the key path. Chinese-English mixed-input tests are
  deferred until a concrete segmenter/ranker exists; listing that direction is
  not an acceptance result.

## Correction learning and expression safety

- Recognize only an immediate single-word commit, exactly one unmodified
  Backspace per UTF-8 Unicode scalar, then same-pinyin/different-word commit
  within five seconds. Too few/many Backspaces, modifiers, different Pinyin,
  timeout, abort, multi-nonempty-segment, `sentence`, unknown candidate type,
  unrelated edits and protected contexts sever adjacency. Applying feedback
  must rerank the first eight upstream candidates stably and invalidate the
  previous engine candidate revision.
- Reject password, private, one-shot, host-opted-out, URL/email/path-like,
  credential-like, control-character and oversized events. Stress 50,000
  word-level aggregates without recording surrounding sentences or app/window
  fields.
- Run the merged Universal librime through the real Rime C API and verify
  `日长` commit → two Backspaces → `日常` commit → requery places `日常` before
  `日长`; stub notifier tests alone are insufficient. Treat the report CLI and
  TSV helpers as explicit plaintext interchange only; encrypted persistence,
  installed monitor UI/CLI and cross-restart behavior remain separate desktop
  acceptance gates.
- With `expression_search` absent, false or manually true, inject no action for
  empty, one-candidate or short upstream translations. Ordinary candidates
  beginning `yunpin-search:` or `yunpin-fav:` must be committed unchanged with
  no browser or filesystem side effect on either platform.

## Sync and cryptography

- Shared deterministic vectors for opaque object IDs, recovery text, canonical envelope encoding, encryption, signature validation, and X25519 pairing.
- A real `protocol.Seal().ToWire()` envelope is accepted, downloaded, reconstructed with the relay-provided source device ID, signature-verified, and decrypted through the sync HTTP handler.
- Two synthetic headless desktop workers create/recover one account over a real
  `httptest` TCP server, exchange encrypted localstore phrase records in both
  directions, converge their CRDT counts, and retry the exact prepared envelope
  after a simulated lost HTTP response without advancing the device sequence
  twice.
- Duplicate device sequences are idempotent; tampering, wrong signatures, future versions, oversize batches, expired pairing, repeated claim, and revoked tokens fail closed.
- G-Counters, HLC-LWW fields, and remove-wins tombstones converge for duplicate and permuted events.
- Server database and captured logs do not contain synthetic plaintext phrase/Pinyin probes.
- Server outage, rate limiting, read-only storage, and disk-full simulation never enters the key-event path.

## Desktop background agent

- Canonical credential-bundle round trips, malformed/oversized records,
  self-trust validation and best-effort mutable-secret clearing use synthetic
  values only.
- The injectable secret-store tests never touch a user's real Keychain or DPAPI
  state. Platform jobs must compile the macOS Keychain and current-user Windows
  DPAPI adapters; isolated platform integration tests are still required before
  desktop acceptance.
- Production `init-account` must reject before network access even after the
  recovery-display acknowledgement until rollback-safe provisioning exists;
  only a package-private, unexported helper may exercise the synthetic
  in-memory protocol flow. External callers must have no switch or option that
  enables non-atomic provisioning.
- `status` remains local-only and emits no identifiers or keys. `sync-once` is
  the only one-shot network command. `run` enforces a per-user single-instance
  lock, bounded retry/backoff and redacted event codes.
- A successful agent sync is not a native sync acceptance result until trusted
  device roster, persistent correction learning, encrypted snapshot rebuild
  and Rime reload are verified on two installed clients.

## Migration

- Source SHA-256 is identical before and after conversion.
- Preview precedes every write; imports require explicit confirmation.
- Duplicate phrase/readings merge deterministically and useful frequency is retained.
- URL, IP, email, path, credential, token, long number, and code-block fixtures are rejected.
- Test fixtures are synthetic and never copied from a real user export.

## Windows desktop alpha

- Windows 10 22H2 and Windows 11, x64 OS.
- Notepad, Office, Chrome, Terminal, and at least one 32-bit and one 64-bit host.
- TSF registration, marked composition, selection, paging, backspace, per-monitor DPI, candidate placement, light/dark mode, upgrade and uninstall.
- Verify Authenticode on every DLL/EXE and installer before a stable release.

## macOS desktop alpha

- macOS 13+ on native Apple Silicon and Rosetta.
- TextEdit, Safari, Office, Terminal; composition lifecycle, app switching, candidate placement, light/dark mode, activation and uninstall.
- Verify Universal architectures, nested code signatures, notarization ticket, stapling and Gatekeeper before a stable release.

## iOS phase two

- Without Full Access, the keyboard reads a host-app-synchronized App Group snapshot and cannot write learning events.
- With Full Access, opt-in learning writeback works while network access remains in the host app.
- Secure text fields, phone-pad substitution, offline use, memory pressure, extension termination and App Review privacy disclosures are validated.

R0W is deliberately excluded until its network returns and the operator re-authorizes a read-only snapshot procedure.

## Supply chain and interface syntax

- The union of all first-party `module@version` rows in Go checksum files equals
  the reviewed license mapping exactly; local replacements match their declared
  path and Apache-2.0 license.
- Project Dockerfiles reject floating tags, digest-only references, `latest`,
  build-argument image substitution, malformed SHA-256 values, and unknown
  internal stages.
- All remote Actions references are full commit SHAs; Docker-based Actions use
  immutable image digests.
- Owned YAML files parse without network access. The OpenAPI version, info,
  paths and operation responses are structurally present.
- Docker Compose renders without interpolation errors. The built sync runtime
  image starts and returns the expected health response before it is removed.
