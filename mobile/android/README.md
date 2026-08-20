<!-- SPDX-License-Identifier: Apache-2.0 -->

# YunPin Android native preview

This is a minimal native Android containing app plus `InputMethodService`.
It intentionally contains no account-creation, recovery, pairing, or release
workflow. It also contains no bundled endpoint, device identity, vocabulary,
credential, input replay, or private import data.

## Process and ownership boundary

- The containing app owns selectable relay profiles, the Android
  Keystore-wrapped opaque `YPCB` credential, `JobScheduler` periodic/one-shot
  execution, redacted status, and the upgrade health journal.
- `mobile/synccore` remains the only owner of protocol v1 framing, E2E crypto,
  dynamic trusted-device roster, encrypted SQLite/outbox, exact retry,
  CRDT conflict/delete behavior, cursor advancement, and atomic snapshot
  publication. Kotlin has only a narrow binding adapter and does not reproduce
  those rules.
- The `:ime` process links only the C++ candidate-query core. It reads the
  immutable app-private TSV snapshot, never accesses Keystore, never writes a
  learning event, and has no sync/network dependency.

Snapshot file reading and C++ parsing run on one loader executor, never in
`onStartInput` or a key callback. A fresh engine is built in isolation and
swapped under a short lock only after full validation; queries keep using the
last-good engine while loading. Missing, invalid, interrupted, or failed loads
discard only the replacement and never clear the last-good in-memory index.
Candidate calculation and commit consult only in-memory slot state. Before the
app writes a new active-profile pointer, it synchronously calls a non-exported
provider in `:ime`; that barrier clears the old slot first, and only its finish
phase permits asynchronous loading. A `FileObserver` is retained as a
crash/external-change fallback. A mid-editor switch clears candidates and
composition, while no pointer or TSV payload is read on a key-event path.

`YunPinApplication` explicitly does no scheduling or app-state initialization
inside `:ime`. The package must request `INTERNET` for the containing app. A
single-package Android permission is UID-wide, so process separation alone is
not claimed as an OS network sandbox; the buildable source/dependency boundary
and the absence of any network call from `ime/` are the enforceable preview
gate.

Private candidates fail closed when the editor is any of:

- text password;
- visible text password;
- web password;
- numeric password; or
- `IME_FLAG_NO_PERSONALIZED_LEARNING`.

Text editors that set `TYPE_TEXT_FLAG_NO_SUGGESTIONS` also fail closed.
Cooperating apps may additionally place the exact, namespaced
`io.github.kukuyan.yunpin.private_mode` or
`io.github.kukuyan.yunpin.one_time_input` token in `privateImeOptions`; these
are wired to the shared private/one-time context flags. Android exposes no
reliable general signal that distinguishes an ordinary number/phone field from
an unmarked OTP field. Such editors must use number-password,
`IME_FLAG_NO_PERSONALIZED_LEARNING`, or the explicit token; the preview does
not claim to detect an unmarked OTP heuristically.

The same flags are passed through JNI to `yunpin_mobile_engine_query`, so a UI
regression cannot bypass the shared-core check. A missing or invalid snapshot
also produces zero private candidates. The IME performs no learning in this
phase.

## Sync and storage

Users may create any number of named relay profiles. HTTPS is the default;
plaintext HTTP is accepted only after explicit opt-in and only for localhost or
a private IP literal. `syncclient.ParseEndpoint` revalidates the same boundary
inside the Go core. No relay address is compiled into the application. A
profile's normalized endpoint and plaintext policy are immutable after
creation: switching servers creates a new profile and requires a distinct
credential through the approved enrollment gate, so an existing bearer cannot
be rebound to another endpoint.

The app and IME use the same app-private `AtomicFile` UUID pointer as the only
runtime active-profile authority. The SharedPreferences selector is metadata
only. Profile selection is ordered as IME-clear, atomic pointer write, then
IME-load; if the barrier or pointer write fails, selection fails closed and the
UI reloads the actual pointer-backed profile with a fixed redacted error.

`KeystoreCredentialStore` creates one non-exportable AES-GCM wrapping key and
stores only authenticated ciphertext in non-backed-up app preferences. Its AAD
is a fixed application prefix plus the canonical profile UUID, so copying an
envelope to another profile fails closed. The wrapped bytes stay opaque to the
app. This key is not a YunPin recovery key, and the app never creates, imports,
resets, or displays recovery material.

Periodic sync uses Android `JobScheduler` at the platform minimum interval;
"Sync now" and successful containing-app `recordSelection`, `saveExplicit`,
`delete`, and `publishSnapshot` operations schedule one-shot work. Protected
selection contexts produce no write and therefore schedule nothing. The IME
has no reference to these APIs and remains incapable of learning.
Jobs require a network, use bounded execution in `mobile/synccore`, and never
start a foreground service, notification, sound, or audio path. Offline/error
state leaves the encrypted core outbox and last immutable snapshot in place.
On a new application version, persisted jobs are cancelled and background
sync plus containing-app mutations remain fail-closed until the foreground
local health gate succeeds; only then are periodic and one-shot jobs enabled.
Status persistence contains categories, counters, booleans, and timestamps
only—never endpoint origins, IDs, phrases, ciphertext, request bodies, or raw
errors.

Both legacy backup and Android 12+ cloud/device-transfer extraction are denied.
The manifest retains `allowBackup=false` and `fullBackupContent=false`, while
`data_extraction_rules.xml` excludes credential envelopes, encrypted state,
snapshots, preferences, databases, files, roots, external files, and every
device-protected counterpart from cloud backup and D2D migration.

## Generated binding gate

The checked-in Android source remains runnable as a configuration/IME shell
without a generated Go archive. Background sync then fails closed as
`protocol_core_required`. In a prepared, reviewed Android build environment,
generate an AAR from `mobile/synccore` and place it in `app/libs/`; the adapter
expects the normal gomobile `go.mobilecore.Mobilecore.openFacade` surface.
Generated archives are build artifacts and must not be committed.

Do not install Go/Android tools as part of an ordinary source checkout. A
maintainer with an already provisioned toolchain may run, from the repository
root. All toolchains and mutable build caches must point at a maintainer-chosen
external-volume directory; the placeholder below is intentionally not a local
machine path:

```sh
: "${YUNPIN_EXTERNAL_DEV_ROOT:?set this to an existing external-volume development directory}"
export GOPATH="$YUNPIN_EXTERNAL_DEV_ROOT/go"
export GOMODCACHE="$YUNPIN_EXTERNAL_DEV_ROOT/go/pkg/mod"
export GOCACHE="$YUNPIN_EXTERNAL_DEV_ROOT/cache/go-build"
export GRADLE_USER_HOME="$YUNPIN_EXTERNAL_DEV_ROOT/cache/gradle"
export ANDROID_HOME="$YUNPIN_EXTERNAL_DEV_ROOT/android-sdk"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
# JAVA_HOME and PATH must likewise reference the pre-provisioned external JDK,
# Gradle, Go, and gomobile installations.

cd mobile/synccore
gomobile bind -target=android -androidapi 28 \
  -o ../android/app/libs/yunpin-synccore.aar .
cd ../android
gradle :app:testDebugUnitTest :app:assembleDebug
gradle :app:connectedDebugAndroidTest
```

The repository intentionally does not include a downloaded Gradle wrapper or
generated AAR. An external JDK, Gradle, and Android command-line tools are
available, but the required Android SDK packages were not installed because
the Google SDK license has not been accepted. Android compilation and device
tests therefore remain unverified until a human accepts that license and runs
the above gate.

## Human gates and rollback

The following are never automated here: developer signing, device
authorization, APK installation, enabling the system IME, publishing an
artifact, changing live infrastructure, or migrating the existing account or
trusted roster. Enrollment remains blocked by the signed-roster-chain gate;
the Android client does not patch a two-device server limit.

The Go core retains the previous immutable candidate snapshot and exposes an
explicit rollback call. The app upgrade journal detects an interrupted version
health check. `Application` only records the new version as pending. After the
main activity has completed view/state restoration, it runs a background,
network-free gate: the C++ candidate ABI must open, any existing snapshot must
pass bounded validation and a full candidate-core parse, and configured state
must unlock its credential and report the same snapshot presence from the Go
core. Pending/healthy state and last-known-good snapshot presence, SHA-256, and
monotonic generation are isolated by canonical profile UUID (with a separate
unconfigured app-shell scope). Empty state can become healthy only before that
profile has ever established a snapshot generation. The coordinator holds its
single-flight monitor and profile lease across local core status, the exact
current snapshot fingerprint, and the journal commit, so neither a concurrent
data-plane mutation nor a mid-check switch can mark stale state healthy. A
background-only launch conservatively leaves health pending. APK rollback itself requires a
known-good signed artifact and a human decision. Neither rollback path moves
the sync cursor backward, deletes the encrypted outbox, nor regenerates a
secret. Candidate-snapshot rollback is additionally bound to the selected
profile's current journal LKG digest: the retained rollback file must pass a
full native parse and match that digest before native restore, the restored
current file must parse and match afterward, and profile plus journal leases
are held throughout. A queued request with a stale digest never opens the core.

All tests use synthetic terms and synthetic byte arrays. Instrumented tests
cover Keystore wrapping, JNI privacy flags, the app-to-core boundary, and the
upgrade pending/healthy transition; JVM tests cover endpoint identity,
credential-profile identity, secure editor signals, profile-bound last-good
replacement, queued/active job cancellation ownership, backup extraction
rules, snapshot headers, retry policy, and redacted status/result parsing.
