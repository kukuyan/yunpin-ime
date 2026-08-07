# Roadmap

## Preview 0.1

- Shared ranking/recall engine and golden tests.
- Offline private-vocabulary importer with sensitive-data preview.
- Opaque encrypted-envelope sync API and Docker Compose.
- Weasel/Squirrel configuration overlays and pinned upstream strategy.
- Source-only and explicitly unsigned development builds.

## Desktop alpha

- Integrate the engine as a merged librime plugin.
- Build Weasel x86/x64 TSF components plus x64 service.
- Build Squirrel Universal arm64/x86_64 InputMethodKit package.
- Complete encrypted local SQLite, OS keychain adapters, atomic snapshot reload, installers and cross-application smoke tests.

## Signed desktop release

- Windows Authenticode signing and installer verification.
- macOS Developer ID signing, notarization and stapling.
- Protected GitHub Release environment and reproducible source bundles.

## iOS phase two

- Independent Swift host app and custom keyboard extension.
- BSD/Apache-compatible librime integration; no GPL desktop source.
- App Group read-only snapshots without Full Access and opt-in learning writeback with Full Access.
