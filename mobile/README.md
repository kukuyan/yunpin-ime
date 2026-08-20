<!-- SPDX-License-Identifier: Apache-2.0 -->

# YunPin mobile foundation

This directory starts the Android and iOS implementation without duplicating
the protocol or ranking policy. `shared/` exposes the existing C++17 phrase
engine through a small stable C ABI for network-free keyboard queries.
`synccore/` is the background-only Go facade over the existing `protocol`,
`localstore`, `syncclient`, and YPCB credential decoder. It owns E2E envelopes,
the encrypted offline outbox, CRDT merges, cursor checkpoints, and atomic
snapshot publication for both native clients.

The mobile product is split into two processes/components on each platform:

1. the keyboard/IME loads an immutable local snapshot and performs candidate
   queries only; it never performs network I/O from a key event;
2. the containing app owns server selection, secure storage for an opaque
   already-paired credential, background sync, atomic snapshot publication,
   rollback, and redacted user-visible status.

The server profile is user-selectable. A profile consists of a display name,
an absolute HTTP(S) endpoint, and the existing private-LAN HTTP opt-in. This
slice has no account creation, recovery, pairing, roster mutation, credential
reset, or secret-display path. Until signed roster-chain enrollment exists, the
apps only consume a finalized opaque YPCB credential delivered by a separately
reviewed pairing flow.

## Android first slice

- Kotlin container app plus `InputMethodService` keyboard;
- CMake/NDK JNI wrapper over `yunpin_mobile_core`;
- native `JobScheduler` periodic sync and immediate sync after a local commit;
- Android Keystore-backed account/device credentials and app-private snapshot;
- secure-field/incognito checks before learning or displaying a private row.

## iOS first slice

- Swift container app plus custom keyboard extension;
- main-app App Group publication path reserved for a future reviewed handoff;
- Keychain-backed login/device credentials owned by the containing app;
- BGTask/app-activation sync, because iOS does not guarantee continuous
  background execution for a keyboard extension;
- `RequestsOpenAccess=false` by default, which means the extension has neither
  network nor shared-container access and exposes zero private candidates. A
  future App Group reader requires a separate Full Access privacy gate and
  still must contain no network or credential API.

Before either mobile client can enroll as a third device, the current fixed
two-device signed-roster protocol must be replaced by a signed multi-device
roster chain. The mobile UI and storage should not encode a two-device limit.
