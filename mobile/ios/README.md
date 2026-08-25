<!-- SPDX-License-Identifier: Apache-2.0 -->

# YunPin iOS foundation

This directory contains a minimal SwiftUI containing app, a custom-keyboard
extension, and three testable Swift Package modules. It deliberately keeps the
mobile data plane in `../synccore`: Swift does not persist or reinterpret
plaintext phrases, envelope queues, CRDT state, device hash chains, or cursors.

## Process boundary

- `YunPinAppServices` owns user-selected endpoint profiles, an opaque paired
  device credential blob in Keychain, lifecycle/background scheduling, fixed
  redacted status codes, and calls into a `SyncCoreBinding`.
- `SyncCoreBindingFactory.production()` selects the reviewed gomobile
  `Mobilecore` module when `canImport(Mobilecore)` is true; otherwise it selects
  the explicit fail-closed binding. The generated adapter opens a short-lived
  `mobile/synccore.Facade` with the
  Keychain blob, runs bounded work, and closes it so mutable key buffers are
  overwritten where the Go runtime permits. The existing string bearer API is
  never persisted or logged, but its heap allocation cannot be promised to be
  scrubbed in place.
`UnavailableSyncCoreBinding` remains the safe result until that generated
framework is linked; the app is not permanently wired to it and never falls
back to a second crypto implementation.

The repository root has no `go.mod`. At the explicit framework-generation
gate, run gomobile from `mobile/synccore`, bind `.`, and write the framework to
the external build cache before a human links it into Xcode; do not use
a repository-root-relative package argument:

```sh
(
  cd mobile/synccore
  gomobile bind -target=ios \
    -o "$YUNPIN_EXTERNAL_DEVELOPER_ROOT/BuildCaches/YunPinIOS/Mobilecore.xcframework" .
)
```
- Synchronize, status, snapshot publish/rollback, selection recording,
  explicit save, and delete share one operation-wide single-flight gate.
  Reentrant status calls receive fixed redacted `operationBusy` state and
  mutating calls fail closed while any operation owns the core. The containing
  app can hand local mutations to the shared encrypted outbox; Swift neither
  persists phrase payloads nor implements a parallel queue.
- BGTask expiration cancels the Swift task and separately enters the
  coordinator to cancel the active native operation. The generated binding's
  locked facade registry makes cancellation sticky between operation start
  and facade registration, then calls `Facade.CancelCurrentOperation` out of
  band while native work is running. Raw native errors are never logged;
  already-decoded fixed error categories are preserved verbatim.
- `YunPinKeyboardCore` contains only privacy policy and read-only snapshot
  access. It has no endpoint, Keychain, sync, delete, enrollment, or network
  API. Phase 1 learning is always disabled, including with Full Access.
- `KeyboardExtension/Info.plist` sets `RequestsOpenAccess=false`. On iOS this
  means the extension has no network and cannot use the containing app's shared
  container, so the shipped default exposes zero private candidates. A future
  snapshot-reading build requires a separate review, changing that value to
  `true`, retaining the App Group entitlement, and the user manually enabling
  Full Access. The extension must still remain free of network and credential
  APIs. Secure text fields are replaced by the system keyboard.

The server profile collection is dynamic and user-selected. No server address,
deployment identity, device count, or two-device exception is encoded in the client.
The existing control plane remains gated on a signed multi-device roster chain;
there is no account creation, enrollment, recovery-secret generation, or
recovery-secret display in this target.

## Storage and rollback

- Keychain uses `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`, disables
  Keychain synchronization, and sets no shared access group. Every endpoint
  profile UUID has a distinct credential account; changing the endpoint or
  private-HTTP policy requires a new profile and separately paired credential.
  The client never migrates, queries, or deletes a legacy global credential
  item. The extension cannot import `YunPinAppServices` and cannot retrieve the
  blob.
- Non-secret endpoint/status/upgrade metadata uses atomic current plus previous
  JSON generations. Status has no free-form error, phrase, URL, account, device,
  or credential fields.
- Background retry state contains only a failure count and the persisted next
  delay. It uses capped exponential backoff with bounded jitter; a successful
  background result resets the delay. BGTask timing remains advisory to iOS.
- Each endpoint profile UUID resolves to its own encrypted database directory
  and App Group snapshot directory. Profiles with valid credentials still
  cannot reuse one another's database, cursor, queue, current snapshot, or
  rollback file.
- Application Support state, encrypted-database roots, App Group snapshot
  roots, and every profile directory are created with
  `isExcludedFromBackup=true` and the value is read back before use. A platform
  or filesystem that cannot persist the exclusion fails closed, preventing a
  ThisDeviceOnly credential from being separated from backed-up private data.
- The shared sync core writes `private.tsv` atomically and retains
  `private.tsv.rollback`. The keyboard resolver is read-only and can use a
  validated rollback in memory; persistent rollback is requested only through
  the containing app's `SyncCoreBinding`.
- Upgrade health is network-independent: a foreground launch needs only a
  successful local `Facade.Status` and a strict fingerprint of the exact
  current snapshot (never a fallback). The profile-local journal atomically
  binds the healthy build to a monotonic snapshot generation and SHA-256
  digest, without storing snapshot contents or paths. Automatic recovery is
  allowed only when a separately validated rollback has that exact digest and
  the restored current file is revalidated after rollback. Missing or
  mismatched rollback enters a human gate without restoration. Binary
  downgrade remains an explicit TestFlight/App Store/MDM/manual gate.
- A newly paired profile whose local-only status reports no snapshot may bind
  a genuine `ENOENT` file state as pre-first-sync healthy; this creates no
  snapshot or digest and remains able to attempt sync after repeated offline
  launches. Once a validated snapshot LKG exists, absence cannot replace its
  generation/digest or relax the matching-rollback gate.

## Local verification

Use the requested Xcode only for each command; do not change the global active
developer directory. Keep caches and build products off the repository:

```sh
export YUNPIN_EXTERNAL_DEVELOPER_ROOT=/path/on-external-drive/Developer
export DEVELOPER_DIR="$YUNPIN_EXTERNAL_DEVELOPER_ROOT/Xcode/Xcode.app/Contents/Developer"
xcrun swift test --package-path mobile/ios --disable-sandbox \
  --scratch-path "$YUNPIN_EXTERNAL_DEVELOPER_ROOT/BuildCaches/YunPinIOS/swiftpm"

xcodebuild -project mobile/ios/YunPinIOS.xcodeproj -scheme YunPin \
  -configuration Debug -destination 'generic/platform=iOS' \
  -derivedDataPath "$YUNPIN_EXTERNAL_DEVELOPER_ROOT/BuildCaches/YunPinIOS/DerivedData" \
  -clonedSourcePackagesDirPath "$YUNPIN_EXTERNAL_DEVELOPER_ROOT/BuildCaches/YunPinIOS/SourcePackages" \
  -packageCachePath "$YUNPIN_EXTERNAL_DEVELOPER_ROOT/BuildCaches/YunPinIOS/PackageCache" \
  CODE_SIGNING_ALLOWED=NO build
```

The Xcode build is intentionally unsigned. Developer certificates, generating
and linking the gomobile framework, device installation, App Group provisioning,
keyboard enablement/Full Access, TestFlight/App Store release, and live
infrastructure changes are explicit human gates.
