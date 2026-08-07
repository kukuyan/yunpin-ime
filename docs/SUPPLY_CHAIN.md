# Supply-chain policy

YunPin's CI verifies dependency metadata from repository state. License checks
must be reproducible offline: CI may download modules for compilation in other
jobs, but it never uses a network service or heuristic classifier to decide a
license.

## Go module licenses

`third_party/go-modules.lock.json` maps every distinct external
`module@version` in these checksum files:

- `protocol/go.sum`
- `localstore/go.sum`
- `sync/go.sum`
- `integration/go.sum`

The map includes versions represented only by a `/go.mod` checksum because they
remain part of the committed lock state. Local replacements are recorded
separately with their exact go.mod version, relative path, and repository
license. The checker requires exact set equality, so stale map rows are errors
as well as missing rows.

When a Go dependency changes:

1. update go.mod/go.sum using normal Go tooling;
2. inspect the authoritative license file at that exact tag or commit;
3. record its SPDX identifier and versioned evidence URL in the lock;
4. preserve required notices and confirm compatibility before merging;
5. run `python3 scripts/check_licenses.py` with networking disabled if desired.

Do not auto-fill an unknown license and do not treat package-index metadata as
the legal source.

## Immutable build references

`scripts/check_supply_chain.py` examines all first-party Dockerfiles and GitHub
workflows. An external `FROM` must use both a descriptive, non-`latest` tag and
`@sha256:<64 lowercase hex>`. A later Docker stage may refer to a previously
declared stage name. Image build arguments are forbidden in `FROM` because they
hide the reviewed reference.

Remote GitHub Actions and reusable workflows must use a full 40-character
commit, with the human-readable release version kept only as a comment. Local
Actions are allowed by relative path. Docker Actions require a SHA-256 image
digest.

## Configuration and runtime checks

`scripts/check_yaml.rb` uses the Ruby standard-library YAML parser for every
owned `.yml`/`.yaml` file, excluding checked-out upstreams and generated
directories. It additionally checks the OpenAPI 3 version, metadata, paths and
operation response mappings. `docker compose config --quiet` provides the
Compose-specific semantic pass.

The sync CI job then builds the exact runtime stage and invokes the existing
container smoke test. Success requires the packaged non-root image to start,
open its published local port, return the expected `/healthz` JSON, and be
removed by the cleanup trap.

Run the local gates from the repository root:

```console
python3 scripts/check_licenses.py
python3 scripts/check_supply_chain.py
ruby scripts/check_yaml.rb
docker compose config --quiet
docker build --target runtime -t yunpin-sync:runtime ./sync
sh sync/scripts/smoke-container.sh yunpin-sync:runtime
```

These checks are an audit control, not a substitute for legal review, SBOM
generation, vulnerability scanning, signature verification, or provenance
attestations required by a stable release.
