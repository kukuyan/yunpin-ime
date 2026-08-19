<!-- SPDX-License-Identifier: Apache-2.0 -->

# YunPin mobile foundation

This directory starts the Android and iOS implementation without duplicating
the ranking policy. `shared/` exposes the existing C++17 phrase engine through
a small stable C ABI that both JNI and Swift can import. It is already bounded,
network-free, snapshot-based, and uses the same first-selection learning rule
as the desktop builds.

The mobile product is split into two processes/components on each platform:

1. the keyboard/IME loads an immutable local snapshot and performs candidate
   queries only; it never performs network I/O from a key event;
2. the containing app owns server selection, username/password login, device
   enrollment, background sync, atomic snapshot publication, and user-visible
   status.

The server profile is user-selectable. A profile consists of a display name,
an absolute HTTP(S) endpoint, and the existing private-LAN HTTP opt-in. Login
uses the existing YunPin username/password API. The apps must use platform
credential storage and must never generate a recovery secret that the user did
not explicitly request and receive.

## Android next slice

- Kotlin container app plus `InputMethodService` keyboard;
- CMake/NDK JNI wrapper over `yunpin_mobile_core`;
- WorkManager periodic sync and immediate sync after a local commit;
- Android Keystore-backed account/device credentials and app-private snapshot;
- secure-field/incognito checks before learning or displaying a private row.

## iOS next slice

- Swift container app plus custom keyboard extension;
- App Group directory for the atomically published snapshot;
- Keychain-backed login/device credentials owned by the containing app;
- BGTask/app-activation sync, because iOS does not guarantee continuous
  background execution for a keyboard extension;
- secure text fields and contexts without Full Access remain local-only and do
  not expose private candidates.

Before either mobile client can enroll as a third device, the current fixed
two-device signed-roster protocol must be replaced by a signed multi-device
roster chain. The mobile UI and storage should not encode a two-device limit.
