<!-- SPDX-License-Identifier: Apache-2.0 -->

# Mobile compatibility contracts

`mobile-protocol-freeze-v1.json` is the machine-readable companion to
`docs/MOBILE_PROTOCOL_FREEZE.md`. It freezes the compatibility and privacy
boundary shared by the Android app/IME, iOS app/keyboard, and any mobile sync
core.

The contract contains no endpoint, credential, key, pairing invitation, device
identity, or user data. Runtime implementations must obtain deployment settings
from the user and secret material from the platform credential store. Contract
fixtures and tests are synthetic only.

Changing a field under a frozen section requires a new contract version and
cross-platform migration tests. Expanding enrollment beyond the current preview
additionally requires the signed roster-chain gate described in the normative
document; a client must not work around the relay guard.
