# Security Policy

YunPin is in development preview and has no stable security-supported release yet. Please report vulnerabilities privately through GitHub Security Advisories instead of opening a public issue.

Never include a real recovery key, device token, personal phrase, conversation excerpt, server database, or signing credential in a report. Use synthetic fixtures and attach sensitive evidence only through the private advisory.

## Security boundaries

- The sync server is designed to be unable to decrypt phrase content.
- TLS is still required: end-to-end payload encryption does not hide IP addresses, timing, account identifiers, ciphertext length, or availability metadata.
- A malicious or compromised server can delete, delay, replay, or withhold opaque records. Signed device chains detect tampering but do not guarantee availability.
- Desktop sync credential and session storage is delegated to current-user
  DPAPI on Windows and atomic `0600` files below the current user's `0700`
  YunPin state directory on macOS. The macOS files rely on that user ownership
  and permission boundary rather than Keychain data protection; this is the
  explicit personal-use tradeoff that permits unattended background sync. A
  retained login Keychain item is only a one-time interactive migration source
  for an existing default device credential, and background loads never access
  it.
- Mobile key storage remains delegated to Android Keystore and iOS Keychain.
- Password fields, private mode, and one-time input contexts must not emit learning events.

See [the threat model](docs/THREAT_MODEL.md) and [protocol](protocol/README.md).
