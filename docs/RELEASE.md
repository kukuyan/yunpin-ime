# Release Policy

Source and unsigned development artifacts may be published as previews. They must be labeled **unsigned** and are not eligible for automatic update or stable channels.

Stable Windows artifacts require:

- x86 and x64 TSF components plus the x64 input service;
- Authenticode SHA-256 signatures with an RFC 3161 timestamp;
- signature verification after packaging and clean install/upgrade/uninstall tests.

Stable macOS artifacts require:

- Universal arm64/x86_64 binaries targeting macOS 13+;
- hardened runtime and Developer ID signatures for nested code and package;
- Apple notarization, stapling, `codesign` verification and Gatekeeper verification.

Signing identities and tokens exist only in a protected GitHub Release environment. Pull requests and forks never receive them. A release also includes source, third-party notices, SBOM, SHA-256 checksums, and the exact submodule commits.

Every release candidate must also pass the offline supply-chain gates:

- all Go checksum versions exactly match the reviewed license map;
- every external Docker `FROM` has both a non-`latest` tag and SHA-256 digest;
- every external GitHub Action uses a full 40-character commit;
- owned YAML parses, the OpenAPI document has valid core structure, and
  `docker compose config --quiet` succeeds;
- the final distroless sync image starts as packaged and answers `/healthz`.

Changing a dependency version, base-image digest, Action commit, or license
classification requires review in the same pull request. See
`SUPPLY_CHAIN.md`.
