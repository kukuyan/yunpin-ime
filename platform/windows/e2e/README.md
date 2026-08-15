# Windows private snapshot E2E gate

These scripts are a short-lived private CI artifact for an authorized Windows
host acceptance run. They are not copied into the public runtime archive or a
GitHub Release asset.

Before use, independently verify `SHA256SUMS` and read `BUILD-METADATA.json`.
Pass its `sameRunPublicOverlay.sha256` value explicitly; the gate also requires
an unambiguous confirmation switch:

```powershell
.\Enable-Private-Snapshot-E2E.ps1 `
  -ConfirmPrivateSnapshotE2E `
  -ExpectedPublicOverlaySha256 <64-hex-same-run-overlay-sha>
```

The entry scripts accept no path overrides. They use the installed preview at
`%LOCALAPPDATA%\Programs\YunPinIME\Preview\current` and the user data at
`%APPDATA%\YunPin\Rime`. They reject reparse points and paths not owned by the
current user. Activation changes only the unique `yunpin/enabled: false` token
to `true`; the unique `yunpin/session_learning: false` gate must remain false.
The private snapshot file is checked only for fixed path, type, ownership and
reparse safety. Its body is never opened, hashed or parsed.

A durable public-overlay backup and recovery phase are written before atomic
replacement. A failed or interrupted `/deploy` keeps that backup. Resume by
running the same command again. A fixed per-user named mutex rejects concurrent
enable/disable processes. The deployer is bounded to 120 seconds; on timeout it
is terminated and the journal plus backup are retained. Roll back with:

```powershell
.\Disable-Private-Snapshot-E2E.ps1 `
  -ConfirmDisablePrivateSnapshotE2E `
  -ExpectedPublicOverlaySha256 <64-hex-same-run-overlay-sha>
```

Disable restores the exact same-run public overlay atomically and retains the
backup until `YunPinDeployer.exe /deploy` succeeds.

This helper proves only the temporary overlay transaction and rollback
boundary. It never sets the process-local `yunpin_learning_allowed` capability.
The Windows host currently does not supply that capability, so changing the
overlay alone is insufficient to make private candidates visible and is not
real typing acceptance evidence.
