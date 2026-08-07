# iOS phase two boundary

The iOS 17+ client will be an independent Swift host app and custom keyboard
extension. It may reuse Apache-2.0 YunPin protocol/engine code and BSD-licensed
librime, but it must not copy or link the GPL desktop frontend implementations.

The host app owns account pairing, key material and synchronization. It writes
an atomically replaced, encrypted App Group snapshot. The keyboard extension
reads that snapshot and never performs network requests. Without “Allow Full
Access” it remains read-only; learning-event handoff is enabled only after the
user explicitly grants access. Secure text fields and phone-pad fields fall
back to the system keyboard as required by iOS.
