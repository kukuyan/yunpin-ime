# Test and Acceptance Plan

## Ranking goldens

- Full Pinyin, compact Pinyin, explicit apostrophe boundaries, and ambiguous segmentation (`xian` versus `xi'an`).
- Initials and common fuzzy pairs, with negative checks that one to three initials cannot promote a pinned long phrase.
- Pinned, demoted, learned-once, learned-twice, imported, public, base, and tombstoned candidates.
- At most two personal entries among the first eight.
- `中国石化销售股份有限公司河北石家庄石油分公司` is top three for `zhongguo...`, `zhongguoshihua...`, and `zgsh...`, and first for the complete Pinyin.
- 50,000 synthetic personal phrases, warm P95 no more than 20 ms.

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

- The union of all `module@version` rows in the four Go checksum files equals
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
