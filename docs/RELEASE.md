# Release Policy

Source and unsigned development artifacts may be published as previews. They must be labeled **unsigned** and are not eligible for automatic update or stable channels. A macOS preview DMG must also say that its ad-hoc-signed app and unsigned package are **not notarized**; packaging them in a checksum-verified DMG does not change that trust status.

The macOS development DMG contains only the unsigned `.pkg`, its matching GPL
corresponding-source archive, installation guidance and an internal SHA-256
manifest. Its build must be byte-reproducible for identical inputs, pass
`hdiutil verify`, mount read-only for an exact-file allowlist comparison, and
record the DMG SHA-256 in the canonical release `SHA256SUMS`. Personal
dictionaries, user configuration and credentials are forbidden from release
media.

Stable Windows artifacts require:

- x86 and x64 TSF components plus the x64 input service;
- Authenticode SHA-256 signatures with an RFC 3161 timestamp;
- signature verification after packaging and clean install/upgrade/uninstall tests.

Stable macOS artifacts require:

- Universal arm64/x86_64 binaries targeting macOS 13+;
- hardened runtime and Developer ID signatures for nested code and package;
- Apple notarization, stapling, `codesign` verification and Gatekeeper verification.

Signing identities and tokens exist only in a protected GitHub Release environment. Pull requests and forks never receive them. A release also includes source, third-party notices, SBOM, SHA-256 checksums, and the exact submodule commits.

## Unsigned preview publication

Unsigned desktop previews use strict tags such as `v0.1.0-preview.1`. A matching
tag starts the normal `CI` workflow; it does not start a shortened release-only
build. The reusable publisher runs only after `required`, `windows-client` and
`macos-client` all succeed in that same workflow run. Manual dispatch cannot
bypass these gates.

Before pushing a preview tag, repository administrators must enable GitHub
Immutable Releases. The publisher refuses to report success unless the public
prerelease reports `isImmutable=true` and its GitHub release attestation
verifies. This locks the published tag and assets after the draft becomes
public; the workflow still re-fetches the tag immediately before and after that
transition because draft tags remain mutable.

The publisher downloads only those same-run artifacts, verifies both platform
metadata records against the tag commit, scans inspectable archives for private
exports, validates the Windows platform checksums and the macOS DMG, and emits
one canonical `SHA256SUMS`, `RELEASE-METADATA.json` and SPDX 2.3 SBOM. It then:

1. re-fetches the tag and fails if it moved;
2. refuses to overwrite an existing Release;
3. creates a non-public draft marked prerelease;
4. resolves that draft from the authenticated paginated Release list using an
   exact, unique `tag + run-owned title + draft=true` identity and a per-run
   body marker; it rejects non-numeric IDs, malformed responses and ambiguous
   matches rather than querying the draft-invisible `/releases/tags/{tag}`
   endpoint;
5. uploads the exact seven-asset allowlist directly by the verified numeric
   Release ID and compares every remote asset's `uploaded` state, byte size and
   SHA-256 digest with the local file;
6. removes the private run marker and makes the draft public in one transition
   only after the comparison passes, then reads the Release back by numeric ID
   and requires the exact final title/body, `draft=false` and
   `prerelease=true` before the immutable-release and attestation gates run.

A failed upload re-lists and re-verifies the run-owned draft instead of trusting
the working ID variable, then removes only that exact draft. A malformed ID or
JSON response, another draft with the same identity, a missing ownership marker,
or an identity change leaves all drafts untouched for manual inspection. No
partially uploaded Release becomes visible. All external Actions are pinned to
full commit hashes; publication itself uses the runner-provided `gh` CLI and
`GITHUB_TOKEN` with job-scoped `contents: write` permission.

The public preview assets are:

- Windows runtime and matching corresponding-source ZIP archives;
- macOS Universal DMG and matching corresponding-source tar archive;
- SPDX 2.3 JSON SBOM;
- canonical `SHA256SUMS`;
- `RELEASE-METADATA.json` binding every payload asset to the tag commit.

The download page is [GitHub Releases](https://github.com/kukuyan/yunpin-ime/releases).

Every release candidate must also pass the offline supply-chain gates:

- all Go checksum versions exactly match the reviewed license map;
- every external Docker `FROM` has both a non-`latest` tag and SHA-256 digest;
- every external GitHub Action uses a full 40-character commit;
- owned YAML parses, the OpenAPI document has valid core structure, and
  `docker compose config --quiet` succeeds;
- the final distroless sync image starts as packaged and answers `/healthz`.

The release SBOM is SPDX 2.3 JSON generated only from the committed dependency
locks. It covers the YunPin repository, Windows and macOS frontends, locked
Rime/data upstreams and macOS nested components, both platform Boost versions,
Sparkle, and every distinct external or local `module@version` in
`third_party/go-modules.lock.json`. It never scans the worktree, build outputs,
or personal dictionaries and never performs network access. Generate and
verify it with:

```console
SOURCE_DATE_EPOCH=0 python3 scripts/generate_release_sbom.py \
  --tag v0.1.0-preview.1 \
  --commit 0123456789abcdef0123456789abcdef01234567 \
  --output build/release/YunPin-IME-v0.1.0-preview.1.spdx.json
python3 scripts/check_release_sbom.py \
  --tag v0.1.0-preview.1 \
  --commit 0123456789abcdef0123456789abcdef01234567 \
  build/release/YunPin-IME-v0.1.0-preview.1.spdx.json
```

`SOURCE_DATE_EPOCH` is optional and defaults to `0`, making identical tag,
commit and lock inputs byte-for-byte reproducible. The output filename is not
part of the SBOM identity and may be renamed by the Release workflow. The
checker independently requires the exact tag/commit namespace, stable unique
SPDX IDs, complete Go lock coverage, structured locked licenses and canonical
JSON generation.

Changing a dependency version, base-image digest, Action commit, or license
classification requires review in the same pull request. See
`SUPPLY_CHAIN.md`.
