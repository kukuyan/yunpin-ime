# Desktop background installation contract

The public macOS and Windows package builders compile
`cmd/yunpin-sync-agent` with the default build tags and carry only that public
binary plus these per-user support scripts. The public executable does not
register private pairing commands; its packaged-binary gate requires
`pairing-invite` to return exactly `unknown command`. The corresponding-source
archive still contains the audited build-tagged source. Private-tag executable
files are separate one-day CI E2E artifacts and are never downloaded by the
release workflow or included in its exact seven public assets.

On a clean E2E device, that private executable additionally exposes
`e2e-init-empty-baseline --confirm-create-empty-baseline`. The public executable
must return exactly `unknown command`. The private command uses only fixed
platform paths and the common process lock; it creates the immutable five-column
empty baseline only when both `baseline.tsv` and `private.tsv` are absent. It
never reads or overwrites an existing vocabulary file and intentionally has no
automatic delete/reset command.

The installer's `install-probe` checks only that the binary starts; it never
loads an endpoint, credential, database, dictionary, or network client, so
interactive account/pairing setup may safely follow installation. Neither
installer embeds an endpoint, bearer token, recovery root, personal dictionary,
learned phrase, or replay data.

- macOS: `Install-LaunchAgent.sh /absolute/path/to/yunpin-sync-agent`
- Windows: `Install-SyncAgent.ps1 -AgentPath C:\absolute\path\yunpin-sync-agent.exe -ExpectedSha256 <manifest SHA-256> -ResidentPath C:\absolute\path\yunpin-sync-resident.exe -ResidentExpectedSha256 <manifest SHA-256>`
  Two binaries are installed. `yunpin-sync-agent.exe` is the interactive one used for
  `status`, configuration and pairing; it writes JSON to stdout and is console-subsystem.
  `yunpin-sync-resident.exe` is what the scheduled task runs: it implements only the
  background loop and is linked for the Windows GUI subsystem, so logging in does not
  leave a console window on screen for the life of the session.
  The verified preview support directory also carries `yunpin-settings.exe`, a
  GUI-subsystem image of the public command package used only by the tray's
  Settings action; it is never registered as an autostart process.

Installation is deliberately two-phase. The commands above copy the binary and
register an autostart job in a **disabled and stopped** state. They never start
the long-running `run` command, so installation and its verifier cannot retain
the single-process lock or the Rime user database lock. After endpoint,
credential, account, and pairing setup are complete, enable explicitly:

- macOS: `Enable-LaunchAgent.sh`
- Windows: `Enable-SyncAgent.ps1`

Each enable script runs only the local redacted `resident-ready` gate with its
output fully suppressed, then enables and starts only the exact registered job.
That gate requires a valid active v2 credential with a creator-signed exact
two-device roster containing the current device, trust maps derived exactly
from that roster, a configured endpoint, private encrypted database and
sidecars, no protected provisioning/creator/joining journal, and the fixed
private Rime bridge metadata and directories. The ordinary `status` command
remains available as a more general local diagnostic and is not sufficient to
enable residency. Failure leaves the job disabled and stopped.

The package integration is intentionally staged, not enabled:

1. CI builds the same audited source as a universal macOS public executable and
   a Windows amd64 public executable. It also builds private-tag counterparts
   into platform-specific `e2e-private` directories; only those directories are
   uploaded as the independent private E2E artifacts.
2. Windows preview installation runs the matching per-user installer and
   verifier after the input method is installed. macOS's root package
   transaction only places the public executable and scripts inside
   `YunPin.app`; the signed-in user separately runs `Install-LaunchAgent.sh`
   without `sudo`. Both paths leave the resident registration disabled and
   stopped. The constant, identifier-free `install-probe` validates only the
   staged executable and cannot treat account state as an installation
   prerequisite. Never copy a user credential, endpoint, journal, database,
   snapshot, or spool into the package staging directory.
3. Run `Verify-LaunchAgent.sh` or `Verify-SyncAgent.ps1`. These verify the exact
   resident executable, disabled registration, and absence of the resident
   process, then call only the agent's state-free, identifier-free
   `install-probe`. They do not load user sync material or contact the relay.
4. After account/pairing setup, run the matching explicit enable script. The
   enabler suppresses the complete local `resident-ready` stream and remains fail-closed
   if setup is incomplete. Only then require encrypted transfer, private
   snapshot replacement, and reload evidence on both Mac and R0W. Real private-
   candidate visibility is a separate host-capability gate: until Squirrel and
   Weasel provide a trustworthy per-field `yunpin_learning_allowed` signal, the
   filter rejects private injection as well as native event publication. Never
   set that option globally merely to make an E2E test pass. A one-off
   `sync-once` acceptance run no longer requires stopping the resident: one
   instance lock keeps the background loop unique, while the operation lock is
   held only during an actual round. If a round is already active, the one-off
   request returns the bounded busy result and can be retried.
5. On any failure, run the matching uninstaller. It retires only the resident
   executable and registration while retaining the private recovery state.

The remaining package boundary is deliberate and concrete:

- the macOS `.pkg` postinstall runs as root and must not create, start, or read
  a particular user's LaunchAgent or Keychain item; resident setup belongs to a
  signed per-user setup step, separate from the root package transaction;
- the Windows preview installer must run resident setup in the signed-in user
  context, pin the staged agent's signed identity/hash, and never copy state or
  DPAPI material from the package staging directory;
- the Weasel native-event producer and consumer use the same Known Folder path
  and exact protected user+SYSTEM ACL contract. Keep this identity and ACL
  contract locked in both native and agent tests;
- all `TestWindows*` ACL, reparse, process-lock, atomic-replace, database-sidecar,
  and DPAPI tests passed under PowerShell 7 on R0W. Preserve that native gate;
  cross-compilation alone is not future native acceptance;
- the two-device invitation/Ready/Finalize response-loss tests and a real
  commit-to-candidate reload on both machines must pass in the independent
  private E2E flow before any explicit enable step is accepted.

The macOS DMG and Windows preview builders use explicit payload allowlists. The
only agent payload is the public default-tag target binary plus the matching
installer, verifier, enabler, uninstaller, and this contract. Package tests run
the state-free probe and exact unknown-command boundary; private E2E metadata or
tagged executables are rejected from the public payload.

Uninstalling only the resident executable and autostart registration is
recoverable: each uninstaller retires them under the private sync state. It
deliberately retains endpoint configuration, encrypted SQLite, Keychain/DPAPI
items, snapshots, and dictionaries.

The installers copy the agent into the current user's private YunPin state,
register a disabled resident job, and verify it remains stopped. The separate
enable script starts it only after local setup succeeds. The agent itself
uses the current user's local, non-synchronizable login Keychain on macOS and
current-user DPAPI on Windows. The endpoint must already have been written
with `yunpin-sync-agent configure` before the enabler succeeds. Account
preparation and device pairing are separate, interactive operations and are
never performed by the resident job.
