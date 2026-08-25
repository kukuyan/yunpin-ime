# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$BundleRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = [IO.Path]::GetFullPath((Join-Path $scriptRoot "..\..\.."))

function Assert-BundleManifest {
    param([Parameter(Mandatory = $true)][string]$Root)
    $manifest = Join-Path $Root "MANIFEST.sha256"
    if (-not (Test-Path $manifest -PathType Leaf)) {
        throw "Package manifest is missing"
    }
    $expected = @{}
    foreach ($line in Get-Content -LiteralPath $manifest) {
        if ($line -notmatch '^([0-9a-f]{64})  (.+)$') {
            throw "Malformed manifest row: $line"
        }
        if ($expected.ContainsKey($Matches[2])) {
            throw "Duplicate manifest path: $($Matches[2])"
        }
        $expected[$Matches[2]] = $Matches[1]
    }
    $rootPrefix = [IO.Path]::GetFullPath($Root).TrimEnd("\") + "\"
    $observed = @{}
    Get-ChildItem -LiteralPath $Root -File -Recurse | ForEach-Object {
        $relative = $_.FullName.Substring($rootPrefix.Length).Replace("\", "/")
        if ($relative -ne "MANIFEST.sha256") {
            $observed[$relative] = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant()
        }
    }
    foreach ($path in $expected.Keys) {
        if (-not $observed.ContainsKey($path) -or $observed[$path] -ne $expected[$path]) {
            throw "Manifest mismatch: $path"
        }
    }
    foreach ($path in $observed.Keys) {
        if (-not $expected.ContainsKey($path)) {
            throw "Unmanifested package file: $path"
        }
    }
}

function Get-PeMachine {
    param([Parameter(Mandatory = $true)][string]$Path)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $reader = [IO.BinaryReader]::new($stream)
    try {
        if ($reader.ReadUInt16() -ne 0x5A4D) {
            throw "Not a PE file: $Path"
        }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 0x40 -or $peOffset -gt ($stream.Length - 6)) {
            throw "Invalid PE header offset: $Path"
        }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) {
            throw "Invalid PE signature: $Path"
        }
        return $reader.ReadUInt16()
    } finally {
        $reader.Dispose()
        $stream.Dispose()
    }
}

function Invoke-AgentCapture {
    param(
        [Parameter(Mandatory = $true)][string]$Executable,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $Executable
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.Arguments = $Arguments -join " "
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) { throw "Failed to start sync agent: $Executable" }
        $stdout = $process.StandardOutput.ReadToEnd()
        $stderr = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        return [pscustomobject]@{
            Output = (($stdout + $stderr).Trim())
            ExitCode = $process.ExitCode
        }
    } finally {
        $process.Dispose()
    }
}

$BundleRoot = [IO.Path]::GetFullPath($BundleRoot)
Assert-BundleManifest -Root $BundleRoot
$privateE2ENames = @(
    "Private-Snapshot-E2E.Common.ps1",
    "Enable-Private-Snapshot-E2E.ps1",
    "Disable-Private-Snapshot-E2E.ps1"
)
foreach ($privateE2EName in $privateE2ENames) {
    $leaks = @(Get-ChildItem -LiteralPath $BundleRoot -File -Recurse | Where-Object {
        $_.Name -ceq $privateE2EName
    })
    if ($leaks.Count -ne 0) {
        throw "Private E2E activation script entered the public runtime: $privateE2EName"
    }
    if (Select-String -LiteralPath (Join-Path $BundleRoot "MANIFEST.sha256") `
        -SimpleMatch -Pattern $privateE2EName -Quiet) {
        throw "Public runtime manifest references a private E2E activation script: $privateE2EName"
    }
}
$runtime = Join-Path $BundleRoot "runtime"
$expectedMachines = [ordered]@{
    "yunpin.dll" = 0x014c
    "yunpinx64.dll" = 0x8664
    "YunPinServer.exe" = 0x8664
    "YunPinDeployer.exe" = 0x8664
    "YunPinSetup.exe" = 0x014c
    "rime.dll" = 0x8664
}
foreach ($entry in $expectedMachines.GetEnumerator()) {
    $path = Join-Path $runtime $entry.Key
    if (-not (Test-Path $path -PathType Leaf)) {
        throw "Required packaged runtime is missing: $($entry.Key)"
    }
    $machine = Get-PeMachine -Path $path
    if ($machine -ne $entry.Value) {
        throw ("Wrong PE machine for {0}: expected 0x{1:x4}, observed 0x{2:x4}" -f $entry.Key, $entry.Value, $machine)
    }
}

$syncAgentRoot = Join-Path $BundleRoot "sync-agent"
$syncAgent = Join-Path $syncAgentRoot "yunpin-sync-agent.exe"
if (-not (Test-Path $syncAgent -PathType Leaf)) {
    throw "Public sync agent is missing"
}
if ((Get-PeMachine -Path $syncAgent) -ne 0x8664) {
    throw "Public sync agent is not an x64 PE executable"
}
# The scheduled task runs this one, and it must be windowless. The subsystem is
# not observable until a user logs in, so the packaged image is checked here.
$syncResident = Join-Path $syncAgentRoot "yunpin-sync-resident.exe"
if (-not (Test-Path $syncResident -PathType Leaf)) {
    throw "Windowless sync resident is missing"
}
if ((Get-PeMachine -Path $syncResident) -ne 0x8664) {
    throw "Windowless sync resident is not an x64 PE executable"
}
$settingsLauncher = Join-Path $syncAgentRoot "yunpin-settings.exe"
if (-not (Test-Path $settingsLauncher -PathType Leaf)) {
    throw "Windowless settings launcher is missing"
}
if ((Get-PeMachine -Path $settingsLauncher) -ne 0x8664) {
    throw "Windowless settings launcher is not an x64 PE executable"
}
$replayLab = Join-Path $syncAgentRoot "yunpin-replay-lab.exe"
if (-not (Test-Path $replayLab -PathType Leaf)) {
    throw "Replay Lab CLI is missing"
}
if ((Get-PeMachine -Path $replayLab) -ne 0x8664) {
    throw "Replay Lab CLI is not an x64 PE executable"
}
$subsystemChecker = Join-Path $repoRoot "scripts\check_pe_subsystem.py"
if (Test-Path -LiteralPath $subsystemChecker -PathType Leaf) {
    & python $subsystemChecker gui $syncResident
    if ($LASTEXITCODE -ne 0) { throw "Packaged sync resident is not linked for the Windows GUI subsystem" }
    & python $subsystemChecker gui $settingsLauncher
    if ($LASTEXITCODE -ne 0) { throw "Packaged settings launcher is not linked for the Windows GUI subsystem" }
    & python $subsystemChecker console $syncAgent
    if ($LASTEXITCODE -ne 0) { throw "Packaged interactive sync agent must stay console-subsystem" }
    & python $subsystemChecker console $replayLab
    if ($LASTEXITCODE -ne 0) { throw "Packaged Replay Lab CLI must stay console-subsystem" }
}
foreach ($supportFile in @(
    "Install-SyncAgent.ps1", "Verify-SyncAgent.ps1",
    "Enable-SyncAgent.ps1", "Uninstall-SyncAgent.ps1", "README.md"
)) {
    if (-not (Test-Path (Join-Path $syncAgentRoot $supportFile) -PathType Leaf)) {
        throw "Public sync-agent support file is missing: $supportFile"
    }
}
foreach ($privateArtifactFile in @("BUILD-METADATA.json", "SHA256SUMS")) {
    if (Test-Path (Join-Path $syncAgentRoot $privateArtifactFile)) {
        throw "Private E2E artifact metadata entered the public package: $privateArtifactFile"
    }
}
if (-not (Test-Path (Join-Path $BundleRoot "licenses\YunPin-Sync-Agent-Go\LICENSES.json") -PathType Leaf)) {
    throw "Public sync-agent license-text bundle is missing"
}
if (-not (Test-Path (Join-Path $BundleRoot "licenses\YunPin-Replay-Lab-Go\LICENSES.json") -PathType Leaf)) {
    throw "Replay Lab license-text bundle is missing"
}
$replayLicenseManifest = Get-Content -LiteralPath `
    (Join-Path $BundleRoot "licenses\YunPin-Replay-Lab-Go\LICENSES.json") -Raw | ConvertFrom-Json
if ([string]$replayLicenseManifest.artifact -cne "yunpin-replay-lab") {
    throw "Replay Lab license manifest names the wrong artifact"
}
$probe = Invoke-AgentCapture -Executable $syncAgent -Arguments @("install-probe")
if ($probe.ExitCode -ne 0) {
    throw "Public sync agent install-probe failed"
}
$privateCommand = Invoke-AgentCapture -Executable $syncAgent -Arguments @("pairing-invite")
if ($privateCommand.ExitCode -eq 0 -or $privateCommand.Output -cne "yunpin-sync-agent: unknown command") {
    throw "Public Windows package exposes a private pairing command"
}
$privateBaselineCommand = Invoke-AgentCapture -Executable $syncAgent -Arguments @("e2e-init-empty-baseline")
if ($privateBaselineCommand.ExitCode -eq 0 -or
    $privateBaselineCommand.Output -cne "yunpin-sync-agent: unknown command") {
    throw "Public Windows package exposes the private empty-baseline E2E command"
}
$replayUsage = Invoke-AgentCapture -Executable $replayLab -Arguments @("help")
if ($replayUsage.ExitCode -eq 0 -or
    -not $replayUsage.Output.StartsWith("error: usage: yunpin-replay-lab")) {
    throw "Packaged Replay Lab CLI usage probe failed"
}

$setupBinaryText = [Text.Encoding]::Unicode.GetString(
    [IO.File]::ReadAllBytes((Join-Path $runtime "YunPinSetup.exe"))
)
if (-not $setupBinaryText.Contains("yunpin.dll") -or $setupBinaryText.Contains("weasel.dll")) {
    throw "Packaged setup binary has the wrong runtime identity"
}
$serverBinaryText = [Text.Encoding]::Unicode.GetString(
    [IO.File]::ReadAllBytes((Join-Path $runtime "YunPinServer.exe"))
)
if (-not $serverBinaryText.Contains("YunPinDeployer.exe") -or $serverBinaryText.Contains("WeaselDeployer.exe")) {
    throw "Packaged server binary has the wrong runtime identity"
}

$forbiddenRuntime = @(
    "WinSparkle.dll", "weasel.dll", "weaselx64.dll", "WeaselServer.exe",
    "WeaselDeployer.exe", "WeaselSetup.exe", "install.nsi", "install.bat"
)
foreach ($name in $forbiddenRuntime) {
    if (Test-Path (Join-Path $runtime $name)) {
        throw "Forbidden upstream/update runtime was packaged: $name"
    }
}
if (Test-Path (Join-Path $BundleRoot "rime-data\yunpin\private.tsv")) {
    throw "A private phrase snapshot must never be packaged"
}
$forbiddenPrivate = Get-ChildItem -LiteralPath $BundleRoot -File -Recurse | Where-Object {
    $_.Name -match '\.(userdb|scel|sgpybin|sqlite|sqlite3|dpapi|bin)$'
}
if ($forbiddenPrivate) {
    throw "Private dictionary/database artifact found: $($forbiddenPrivate[0].FullName)"
}
$config = Get-Content -LiteralPath (Join-Path $BundleRoot "rime-data\rime_ice.custom.yaml") -Raw
foreach ($pattern in @(
    'yunpin_filter@yunpin',
    'yunpin_comment_filter@yunpin_comment_visibility',
    'name: yunpin_show_candidate_pinyin',
    'states: \[拼音关, 拼音开\]',
    '(?m)^\s*"translator/keep_comments": true\s*$',
    '(?m)^\s*"corrector": "［\{comment\}］"\s*$',
    '(?m)^\s*"yunpin/enabled": false\s*$',
    '(?m)^\s*"yunpin/short_input_guard": true\s*$',
    '(?m)^\s*"yunpin/session_learning": false\s*$',
    '(?m)^\s*"translator/enable_correction": false\s*$',
    '(?m)^\s*"translator/corrector_component": yunpin_corrector\s*$',
    '(?m)^\s*"yunpin/typo_correction": false\s*$',
    '(?m)^\s*"yunpin/max_candidates": 2\s*$'
)) {
    if ($config -notmatch $pattern) {
        throw "Packaged Rime configuration is missing safety setting: $pattern"
    }
}
$defaultConfig = Get-Content -LiteralPath (Join-Path $BundleRoot "rime-data\default.custom.yaml") -Raw
if ($defaultConfig -notmatch 'switcher/save_options/@after last": yunpin_show_candidate_pinyin') {
    throw "Packaged Rime defaults do not save the candidate-Pinyin preference"
}
$installer = Get-Content -LiteralPath (Join-Path $BundleRoot "Install-Preview.ps1") -Raw
if ($installer -notmatch 'AcceptUnsignedDevelopmentBuild' -or $installer -notmatch 'Assert-BundleManifest') {
    throw "Development installer lacks explicit unsigned-build acceptance or manifest verification"
}
$metadata = Get-Content -LiteralPath (Join-Path $BundleRoot "BUILD-METADATA.json") -Raw | ConvertFrom-Json
if ($metadata.signed -ne $false -or $metadata.productionReady -ne $false -or
    $metadata.mergedPlugin -ne "librime-yunpin" -or
    $metadata.syncAgent.build -cne "public-default-tag" -or
    $metadata.syncAgent.privatePairingCommands -ne $false -or
    $metadata.syncAgent.residentDefault -cne "disabled") {
    throw "Unexpected package metadata"
}

Write-Host "Windows package verification passed: manifest, PE architecture, privacy, and preview gates"
