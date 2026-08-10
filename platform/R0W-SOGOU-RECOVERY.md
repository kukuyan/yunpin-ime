# R0W Sogou vocabulary recovery runbook

Status: **active recovery preview**. R0W network access was re-verified on
2026-08-10. Reconfirm the live hostname and user before every recovery session;
do not repair networking, change proxy/routes or rely on a historical address
as part of vocabulary migration.

Use this procedure only after the network is restored independently and the
user explicitly reconfirms the recovery session. Historical host identity and
paths are clues, not current facts.

## Non-negotiable controls

- Do not sign in to Sogou sync, upgrade, uninstall, repair, reset, clean, or
  launch an optimizer before the snapshot is complete.
- Start read-only. Confirm the actual Windows host identity, Sogou version and
  data locations; do not assume the candidate paths below exist.
- Prefer Sogou's official local vocabulary export/backup. If only proprietary
  local databases exist, parse copies only.
- Store originals, manifests, converted TSV and previews outside the Git
  checkout. Never upload them to GitHub, CI artifacts, issues or release files.
- Never paste credentials, account identifiers, sync tokens, personal phrases
  or unmasked previews into logs or tickets.

## 1. Read-only discovery

From a local PowerShell session on the confirmed R0W host, record system time,
Windows edition and hostname. Enumerate installed-program registry entries
without modifying them:

```powershell
$YunPinUninstallRoots = @(
  'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*',
  'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*',
  'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*'
)
Get-ItemProperty $YunPinUninstallRoots -ErrorAction SilentlyContinue |
  Where-Object { $_.DisplayName -match '搜狗|Sogou' } |
  Select-Object DisplayName, DisplayVersion, InstallLocation, Publisher
```

Then inspect, but do not alter, likely per-user roots such as
`$env:APPDATA\SogouPY` and `$env:LOCALAPPDATA\SogouPY`. Resolve the real files
from observed settings and timestamps. If Sogou exposes an official backup or
text export, use it before proprietary database recovery.

## 2. Immutable snapshot outside Git

Choose an explicit local destination that is not under a source checkout.
Create it, copy each confirmed source file, and hash both sides. The example
uses placeholders intentionally; resolve every path before running it.

```powershell
$YunPinSnapshotRoot = 'D:\YunPin-Recovery\YYYYMMDD-HHMMSS'
$YunPinObservedSource = 'C:\resolved\after\inspection\personal-backup.bin'
New-Item -ItemType Directory -Path $YunPinSnapshotRoot -ErrorAction Stop

$YunPinSourceHashBefore = Get-FileHash -Algorithm SHA256 -LiteralPath $YunPinObservedSource
$YunPinSnapshotFile = Join-Path $YunPinSnapshotRoot (Split-Path $YunPinObservedSource -Leaf)
Copy-Item -LiteralPath $YunPinObservedSource -Destination $YunPinSnapshotFile -ErrorAction Stop
$YunPinSnapshotHash = Get-FileHash -Algorithm SHA256 -LiteralPath $YunPinSnapshotFile
$YunPinSourceHashAfter = Get-FileHash -Algorithm SHA256 -LiteralPath $YunPinObservedSource

if ($YunPinSourceHashBefore.Hash -ne $YunPinSnapshotHash.Hash -or
    $YunPinSourceHashBefore.Hash -ne $YunPinSourceHashAfter.Hash) {
  throw 'Hash mismatch: stop without conversion'
}
```

Record a redacted manifest with file name, size, source/target hashes, Sogou
version and observation time. Do not record phrases, account values or tokens.
Preserve the original source unchanged.

## 3. Offline preview on the copy

Obtain the ImeWlConverter v3.4.3 release from its official GitHub release,
verify the archive against `upstream-lock.json`, and calculate the exact
executable/DLL SHA-256. Disconnect the conversion workspace from unnecessary
networks if practical. Run `tools/importer` against the snapshot copy, not the
Sogou data directory:

```console
python -m yunpin_importer sogou D:\YunPin-Recovery\...\personal-backup.bin \
  --source-format sgpybin \
  --source-sha256 HASH_FROM_SNAPSHOT \
  --converter D:\Tools\ImeWlConverterCmd.exe \
  --converter-sha256 HASH_OF_EXACT_CONVERTER
```

The importer keeps up to 100,000 merged entries by default, matching the native
private-index limit. The reviewed R0W conversion currently contains 94,382
merged entries, so the confirmed import must retain all 94,382 rather than
reusing the earlier 50,000-row truncated output. The immutable original
snapshot remains the recovery source outside Git even after the complete
private TSV is rebuilt.

The default preview masks phrases and reports duplicates, missing pinyin and
filter counts. Use `--reveal-phrases` only on the local private console. After
review, repeat with `--confirm IMPORT --output D:\YunPin-Private\imported.tsv`.

For `.scel`, use `--source-format scel`. For Sogou text exports, use the normal
`import --kind text` command; they do not require the GPL converter.

## 4. Acceptance evidence

- Source SHA-256 before and after conversion is identical to the snapshot.
- Duplicate rows merge; useful frequency maps to `use_count`; missing readings
  are explicitly counted rather than guessed online.
- The confirmed private TSV reports 94,382 retained entries and no
  `over_private_snapshot_capacity` rejection for the reviewed R0W source.
- Long organization phrases can be found from full pinyin and initials after
  they enter the local YunPin index.
- A repository-wide scan finds no original Sogou file, converted personal TSV,
  unmasked preview, account data or personal phrase fixture.

Only after this evidence is retained outside Git is the R0W migration complete.
No Sogou cleanup or account change is part of this runbook.
