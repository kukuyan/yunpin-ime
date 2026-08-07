# Contributing

Thank you for helping YunPin. Use a `codex/*` or descriptive feature branch, keep commits focused, and open a pull request against `main`.

Before submitting:

1. Run `make check`.
2. Use only synthetic or clearly public test data.
3. Do not commit personal dictionaries, conversation exports, SCEL/BIN files, databases, keys, credentials, signing assets, or production logs.
4. Add an SPDX identifier to new source files and preserve third-party notices.
5. Document protocol or ranking changes and add deterministic tests.

Pull requests from forks do not receive signing or deployment secrets. Native stable releases require protected release environments, Authenticode for Windows, and Developer ID signing plus notarization for macOS.

By contributing, you agree that your contribution is licensed under the license declared by the target directory. If a file has no narrower declaration, Apache-2.0 applies.
