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
- 100,000 synthetic personal phrases, warm P95 no more than 20 ms.

## Experimental automatic Pinyin correction

- Assert both shipped overlays set `translator/enable_correction: false`,
  `yunpin/typo_correction: false` and
  `yunpin/typo_reviewed_confusions: false`. With the component name present but
  YunPin correction disabled, its factory must return no corrector and must not
  instantiate or fall back to `NearSearchCorrector`.
- Unit-test the bounded generator for physical QWERTY neighbours, one missing
  key, one extra key and one adjacent transposition. Reject non-lowercase,
  one-letter and 128-byte-or-more adversarial segments; enforce six-byte
  syllables and 768 raw variants. `you` → `yao` must be absent by default and
  appear only when the separately reviewed-confusion experiment is explicit.
- Apply the exact version-locked compatibility patch to clean librime 1.16 and
  1.17 trees, verify each locked SHA-256 and reject base-commit drift. Keep the
  schema-local `yunpin_corrector`; do not replace the global corrector used by
  other schemas.
- In a revised opt-in real ScriptTranslator/Dictionary/Prism/Rime C API suite,
  construct inputs with multiple possible segmentations and assert that any
  complete all-normal exact path suppresses all correction searches for the
  whole composition.
- For input without a complete exact path, assert every admitted correction is
  a single bridge from a forward exact-reachable offset to a reverse
  exact-suffix-reachable offset. No composition may add corrections at more than
  one input offset; attempted correction searches are capped at 32, and Prism
  validation/deduplication exposes at most 16 edges at the searched offset.
- Do not retain the historical expectation that a two-offset/double-error input
  must return the intended sentence first. Add a negative test proving the
  second correction offset is unavailable. Likewise, default mode must never
  rely on `you` → `yao`.
- For normalized input of at least 12 characters, assert the filter retains at
  most one correction: total rank #2 with no private leader, total rank #3 with
  one, and none with two private leaders, correction-only upstream, or a
  correction beyond the first-page window. No discarded correction may leak to
  a later page.
- Run the revised explicit experiment against locked production public packs
  and a 100,000-entry synthetic personal snapshot. Warm and time final-key
  processing, enumerate candidates, report P50/P95/P99 and enforce P95 no more
  than 20 ms. Historical double-error and reviewed-confusion timings are not
  evidence for the current graph policy.
- No correction test may open the network or read a model. A future optional
  local-model sidecar needs separate deadline, crash, malformed-output and
  offline tests proving timeout fails closed to the ordinary exact path without
  delaying the key path. There is no NearSearch/model fallback today.

## Private snapshot capacity and R0W migration

- Parse and retain exactly 100,000 unique snapshot rows, including an explicit
  regression above the legacy 50,000 boundary. Row 100,001 must be counted as
  rejected rather than accepted; malformed and duplicate rows also remain
  explicitly accounted for.
- Exercise high-collision prefixes in at least five percent of timed queries.
  Enforce query P95 ≤ 20 ms, parse plus build ≤ 15 seconds and incremental peak
  RSS ≤ 256 MiB. The verified development run measured P95 1.672 ms and peak
  increment 80.172 MiB; record machine/runtime details and do not generalize it
  to native hosts.
- Keep importer and runtime hard caps aligned at 100,000. Verify frequency sort
  before truncation, duplicate merge and `over_private_snapshot_capacity`
  accounting with synthetic data.
- Treat the R0W 94,382-row conversion as incoming only. Before deployment,
  verify the immutable source hash, complete TSV row count, zero capacity loss,
  output outside Git and a newly built 100k-capable binary. The currently
  installed legacy 50k binary is not valid full-migration acceptance evidence.

## Replay Lab

- Test strict `EventV1` decoding, unknown-field rejection, 8 KiB event limit,
  UTF-8/text/candidate bounds, one-based selection consistency, canonical time,
  contiguous sequence and episode lifecycle.
- Test the append-only local store, fsync-before-metadata crash window, strict
  status repair, lifecycle CLI, explicit ingest, deterministic report, export
  outside the lab root and confirmed safe clear. Real traces must be rejected
  from Git/private-data scans.
- Test the default-disabled fixed 64-slot C++ SPSC ring, bounded native event
  validation/serialization, 8 KiB drain boundary, FIFO order, overflow
  `drop_count`, native-frame parsing and conversion to `EventV1`.
- Until Squirrel/Weasel producers and a background sink are connected, assert
  that `start` only creates a session/resume record and captures no input by
  itself. Native acceptance requires continuous producer→ring→sink→store proof,
  immediate pause/protected-context suppression, no disk/network wait on the
  key path and explicit loss accounting.

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

R0W network access and the read-only conversion preview have been restored. The
94,382-row complete TSV remains incoming and must not be described as deployed
until a 100k-capable binary passes the migration checks above.

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
