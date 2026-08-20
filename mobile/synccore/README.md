<!-- SPDX-License-Identifier: Apache-2.0 -->

# YunPin mobile sync core

`mobilecore` is a narrow, background-only Go facade over the existing
`protocol`, `localstore`, `syncclient`, and opaque `YPCB` device credential.
It reuses the production envelope encryption/signature rules, encrypted SQLite
outbox, CRDT merge/delete behavior, exact retry checkpoint, and relay cursor.

The native containing app loads the opaque credential from Keychain or Android
Keystore, opens `Facade` for one bounded job, reads a redacted result, and
closes it. The credential is never written by this module. Account creation,
recovery, device enrollment, roster mutation, server discovery, analytics and
keystroke capture are intentionally absent.

The keyboard process does not link the sync facade. It loads only the immutable
TSV snapshot through `mobile/shared` and performs local candidate queries. The
snapshot contains selected phrase identities and aggregate metadata, never raw
keystrokes, surrounding text, source application names or input replay.

`Status` is the complete observability boundary: cursor/outbox counts, snapshot
presence and the control-plane capability gate. Native clients must map errors
to stable local categories and must not persist or upload raw error text.

The existing pairing-v2 control plane cannot safely enroll a third device.
`ControlPlaneGate` therefore remains `signed_roster_chain_required`. Do not
remove that gate or raise a server constant; enable enrollment only after a
signed roster replacement chain and migration tests are implemented.

Host verification:

```sh
GOCACHE=/private/tmp/yunpin-mobile-go-cache go test ./...
```

Generating Android/iOS bindings is a later toolchain step. Generated archives
and credentials are build artifacts and must not be committed. Developer
signing, device installation, keyboard enablement, distribution and any relay
deployment are explicit human gates.
