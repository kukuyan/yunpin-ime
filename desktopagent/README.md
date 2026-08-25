# YunPin desktop synchronization agent

This Apache-2.0 Go module is the out-of-process desktop bridge between native
selection events, encrypted `localstore`, the opaque relay, and an immutable
Rime personal snapshot. Keyboard handlers never perform disk or network I/O.

## Local secret boundary

`CredentialBundleV1` contains the device bearer token, Ed25519 seed, X25519
private key, per-device SQLite key, account object-ID key, epoch keys, and the
authenticated verification roster. It is stored only in the current user's
local, non-synchronizable macOS login Keychain or a current-user Windows DPAPI
record below `%LOCALAPPDATA%`. The ad-hoc signed command-line preview targets
the file-based login Keychain because Apple's data protection Keychain requires
provisioning-profile-authorized access-group entitlements.

The recovery root is never stored. First-device provisioning is deliberately
two phase:

1. `prepare-account --confirm-display-recovery-key` generates everything with
   zero network access, displays the account ID and recovery root, and waits
   for the user to type `SAVED`. Only after that acknowledgement does it save
   a resumable `<profile>.provisioning` journal in Keychain/DPAPI. The journal
   contains recovery authentication and an already sealed recovery keyring,
   but not the recovery root.
2. `init-account --confirm-saved-recovery-key` reuses that exact identity for
   idempotent create/keyring/seal/local-commit transitions. Process death or a
   lost response can be resumed. The pending journal is removed only after the
   account is sealed and its exact-key database and active OS credential are
   committed. `abort-account --confirm-abort-unsealed-account` uses a dedicated
   idempotent rollback capability; it keeps the protected journal unless both
   remote rollback and verified local cleanup complete.

If the relay garbage-collects an unsealed identity, `init-account` recognizes
the exact `account_not_found` / `provisioning_identity_retired` state and stops
retrying. It retains the protected rollback capability and requires the
idempotent `abort-account` tombstone flow before a fresh identity can be
prepared; no active credential or database is promoted in that state.

## User login and server choice

`configure-server` is an alias for the endpoint selector: it accepts one
absolute HTTPS relay URL, or an explicitly opted-in private-LAN HTTP literal.
The selected endpoint JSON intentionally contains no account or token.

Before `init-account`, run `register --username <name>` (first use) or
`login --username <name>` (another desktop). Passwords are read only from an
interactive terminal, never flags, files, shell history, or agent output. The
opaque login session is endpoint-bound and saved only in the current user's
Keychain/DPAPI secret store. `claim-account
--confirm-claim-existing-account` adopts an already provisioned account using
the active device credential already protected by that same OS store; it never
asks for recovery material or any additional terminal input. Normal
`sync-once` and resident `run` continue to use the independent device
credential, so password-session expiry cannot stop background sync.

## Two-device pairing preview

The v2 pairing state machine keeps creator and joining journals only in
Keychain/DPAPI. The invitation carries a high-entropy out-of-band secret; the
relay receives only a domain-separated verifier. Join and claim proofs bind
both client-generated device identities, bearer/rollback capabilities, and
Ed25519/X25519 keys to the complete transcript. The transferred account keys
and exact two-device roster are encrypted with PSK+X25519 and the roster is
signed by the already trusted creator. The joining credential derives all
verification trust only from that signed roster, never relay `ListDevices`.
Approval does not yet modify the creator's active self-only trust. A separate
resumable finalize step first observes the joining device's durable `ready`
state, re-verifies the full
PSK-authenticated joining transcript against the protected signed roster, and
only then atomically promotes the creator credential. If the joining device
rolls back or the invitation terminates, finalize restores canonical self-only
trust and removes the protected journal, including the crash window after the
credential CAS but before journal deletion.
The joining device keeps its journal after `ready`; it deletes that journal
only when an idempotent readiness replay observes `finalized`. A creator cancel
before readiness moves the joiner into durable `rollback_pending` before any
device deletion, so a lost DELETE response resumes from the rollback tombstone
without issuing Claim again.

Private pairing builds also expose `pairing-abort --confirm`. The command takes
its account, device, pairing, and one-purpose rollback identity only from the
protected joining journal. It validates any pending active credential and
encrypted database against that exact journal, persists `rollback_pending`,
then requests the relay's idempotent pre-ready rollback. Local credential,
database, and journal cleanup occurs only after an authenticated 204/tombstone
replay. A transport error, generic 401, wrong identity, or stable 409 keeps the
journal and all local material. If the first Join never reached the relay, an
exact Join replay may remove a journal only for the narrowly authenticated
`pairing_not_found` or expired invitation codes; it never treats HTTP status
alone as successful cleanup. Public builds keep this command unregistered.

This preview is deliberately limited to exactly the Mac and R0W devices. Once
a signed two-device roster exists, further invitation, recovery, or third
device enrollment is rejected. General N-device roster-chain propagation and
same-version fork resolution are outside this release boundary.

Endpoint configuration contains no secret. Plain HTTP is opt-in and limited
to loopback or private IP literals; HTTPS remains preferred.

## Native learning bridge

The host adapter drains a fixed in-process queue into private, atomically
renamed v2 JSON files. V2 carries either one normal selection or one proven
wrong/replacement pair, plus a local date bucket; legacy v1 selection files
remain readable. The agent accepts at most 2,048 files and 8 MiB, consumes at
most 256 per pass, and commits each event ID in the same SQLite transaction as
its phrase update. The receipt table is pruned to a fixed bound.

Word-level learning evidence is sealed with a key derived from the existing
local data key before entering SQLite; no plaintext habit sidecar exists and no
new secret is introduced. At most 50,000 encrypted events are retained. A
snapshot rebuild aggregates them into a bounded signed correction score, so an
explicit correction such as `办公是` to `办公室` remains effective after the IME
or desktop agent restarts. These local correction events are not added to the
sync outbox.

Before consumption, the existing reviewed `private.tsv` is migrated exactly
once to immutable `baseline.tsv`. If both are missing, ingestion fails closed.
Every baseline phrase is a phrase-only local deny entry: selecting it with any
pronunciation records only a receipt and can never create a sync outbox event.
The 100,000-row generated snapshot preserves the complete static baseline and
adds only bounded synchronized learning ordered by pin/count/code/text.

Snapshot replacement is same-directory atomic, private, and followed by a
fixed platform reload (`YunPin --reload` on macOS or the fixed preview
`YunPinDeployer.exe /deploy` path on Windows). A digest marker is written only
after reload succeeds, so a crash between replacement and reload is retried.

## Rime userdb learning bridge

`sync-once --rime-userdb-export /absolute/private/path` can ingest a staged
snapshot of Rime's own cumulative userdb learning without enabling YunPin's
host-sensitive `session_learning` producer. The accepted format is deliberately
the strict uniform userdb snapshot row used by librime:

```text
code<TAB>phrase<TAB>c=<signed-commits> d=<finite-score> t=<tick>
```

The first import applies the complete positive cumulative commit count. Later
imports apply only a positive per-device/object high-water delta. An equal
snapshot is a no-op; a lower count or Rime's negative deletion marker moves the
local high water to the new nonnegative value but never creates a negative CRDT
delta or synchronized deletion. The high water, encrypted learned phrase, HLC,
and encrypted outbox mutation share one SQLite transaction, so a failed batch
cannot suppress a retry. No phrase or code is included in parser errors or
operational summaries.

Providing `--rime-userdb-export` selects this cumulative source for the run and
suppresses native per-selection spool consumption, because ingesting both views
of the same commits would double-count learning. The production helper must
always export one fixed userdb identity to this stable staging path; changing
the source database behind the path is outside the high-water contract.

The immutable baseline remains a phrase-only local deny set. Baseline entries
advance local high water but do not materialize or enter the outbox. Existing
pending variants with any pronunciation are scrubbed in the same transaction;
an already prepared upload with response-loss ambiguity fails closed instead
of being silently discarded or sent.

`configure-rime-bridge --confirm` reads exactly one safe top-level
`installation_id`, creates a private first-state backup of `installation.yaml`,
then atomically sets Rime's `sync_dir` to the agent-owned `rime-sync` directory.
It does not inspect the old default sync directory. Resident `run` invokes only
the fixed installed host (`YunPin --sync <request-nonce>` on macOS or
`YunPinDeployer.exe /sync` on Windows), requires a fresh stable uniform snapshot
under `rime-sync/<installation_id>/rime_ice.userdb.txt`, validates the directory
contains only that device and no symlinks, hardens the host output, and atomically
copies it to `rime-userdb.snapshot` before every ingest. A static
`--rime-userdb-export` remains available only to explicit `sync-once`; `run`
rejects it.

On macOS, exiting the `--sync` helper is not completion. The agent supplies a
random request nonce and waits for a matching private fixed-path acknowledgement
written only after `sync_user_data()` and `join_maintenance_thread()` complete.
The host patch providing that acknowledgement must ship before resident Rime
learning is enabled. Windows `/sync` is a synchronous maintenance boundary but
still needs a native R0W acceptance after packaging. Neither platform invokes a
shell, consults `PATH`, accepts an output path, prints phrase text, or logs the
snapshot body.

There is no raw LevelDB snapshot helper in the production or corresponding-source
package. Even nominal read-only LevelDB opens may create or update database
metadata, so resident synchronization uses only the fixed installed host's
maintenance/export boundary and its platform acknowledgement contract.

## Vocabulary management

`phrase add|pin|unpin|remove|list|report` are the supported way to inspect or
correct the personal
vocabulary. Before them the only reachable lever was hand-editing
`yunpin/private.tsv`, which is a generated snapshot the next rebuild overwrites,
so corrections did not survive.

Every edit goes through the same `SaveExplicit`/`Delete` path a learned phrase
takes, so it lands in the same mutation-plus-outbox transaction and converges on
the other devices by the ordinary merge rules. Each edit also republishes the
snapshot and reloads the host, because a change that only reached the database
would leave the user seeing no difference in the candidate window.

An explicit add carries a use count of one. A count of zero is filtered out of
the generated snapshot, so without it the phrase would never become a candidate.

`phrase list` reports counts only. Phrases and readings require `--show-text`,
which also prints a warning to stderr. The explicit `--show-text` flags are the
only paths that put personal vocabulary or habits on a terminal. No vocabulary
reaches the run-event log, the health record, or `status` through any of these
commands.

`phrase report [--since YYYY-MM-DD] [--corrections-only]` reads the encrypted
learning history and defaults to aggregate counts grouped by local date.
Word-level entries require its own explicit `--show-text` opt-in and emit the
same stderr disclosure warning; `--limit` bounds only that opt-in entry list,
not the aggregate totals.

## Local settings page

`settings` opens one temporary browser page bound only to an ephemeral
`127.0.0.1` port. A per-process unguessable path scopes every GET and POST;
responses are non-cacheable and the process exits after 30 minutes. The page
contains only four product functions: the three ranking/correction booleans,
redacted synchronization health, a real `sync-once` action, and the existing
personal-vocabulary operations. It has no endpoint, account/device identifier,
credential, recovery, reset, clear or re-pair field.

Saving guards replaces only the exact `yunpin/short_input_guard`,
`yunpin/long_correction_guard`, and `yunpin/typo_correction` boolean tokens in
`rime_ice.custom.yaml`; comments and every unrelated byte are preserved. The
write is same-directory atomic and the fixed platform deployer is invoked even
when the selected values were already on disk.

The resident now holds a distinct instance lock for its lifetime and the
ordinary operation lock only during a synchronization round. This preserves
exactly one resident while allowing the settings page or CLI `sync-once` to run
during the interval between rounds. A collision with an active round remains a
bounded busy/deferred result rather than a second concurrent database writer.

## Background installation

`install/` contains reviewed per-user resident-service install, rollback, and
uninstall contracts. They contain no endpoint, credential, recovery key,
dictionary, learned phrase, or replay data. Public input-method packages carry
only the default-tag executable and these scripts. Windows stages its scheduled
task as disabled and stopped; the macOS root package only installs the files,
leaving per-user LaunchAgent staging to an explicit non-root command. Private
pairing-tag executables remain separate CI-only E2E artifacts.

The ordinary `status` command remains a general local diagnostic. Autostart
activation uses only the stricter, identifier-free `resident-ready` command.
That local gate requires the finalized creator-signed exact two-device roster
containing the current device, trust maps reconstructed from that roster, no
protected provisioning/creator/joining journal, endpoint and private SQLite
state, and fixed private Rime bridge metadata. Its Rime check never invokes
maintenance or reads vocabulary rows; it validates only configuration,
filesystem identity, permissions, and bounded metadata.

`status` separates configuration readiness from observability. A ready device
reports `health_available=false` when its health row cannot be opened or read,
instead of presenting that condition as a never-synchronized zero record. It
also reports `event_log_available`; the resident continues synchronizing when
the bounded log cannot be opened. Existing and rotated log generations must be
private regular non-link files, and rotation retains exactly one prior
generation.

Failed rounds persist only the closed `last_failure_class` values `network`,
`auth`, `relay_protocol`, or `local_store`; successful and deferred rounds use
`none`. Original error text, endpoint, account and device identifiers never
enter health or the run-event log. Historical `sync_failed` rows created before
classification migrate to the explicit value `unknown` rather than having a
cause invented retroactively.

Health is observational, not part of the relay checkpoint transaction. The
agent writes it through the same already-open local store, after the sync work
and before that store is closed; it no longer reloads credentials or reopens the
database after `SyncOnce`. A power loss between a committed cursor/outbox change
and the best-effort health write can therefore leave health stale, but can never
roll back synchronization or make logging a prerequisite for it.

Private-tag CI binaries expose one clean-device bootstrap command:
`e2e-init-empty-baseline --confirm-create-empty-baseline`. It holds the fixed
agent process lock and performs an OS-level no-replace publish of only the exact
five-column empty header when both fixed baseline and snapshot paths are absent.
Existing files are never opened, read, replaced, or automatically deleted. The
default/public binary reports `unknown command` for this E2E-only operation.

## Honest completion boundary

The first-device provisioning, two-device pairing primitives/journals, native
event and strict Rime-userdb staging ingestion, encrypted sync worker, baseline
merge, atomic snapshot, reload retry, and resident-service contracts are
implemented and unit tested. Pairing commands remain absent from the public
binary and exist only in checksum-bound, short-lived private E2E artifacts until
the exact two-device flow, rollback response-loss, platform integration, signed
packaging, and real Mac↔R0W runtime acceptance are all green. The Rime userdb
path additionally requires the fixed platform host maintenance and fresh
acknowledgement boundary described above. This is therefore not yet a deployable
multi-device release.
