# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [switch]$HoldMutexChild,
    [string]$ReadyPath = "",
    [string]$ReleasePath = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Private snapshot fixture tests require Windows"
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Assert-Throws {
    param([scriptblock]$Action, [string]$Pattern)
    try {
        & $Action
    } catch {
        if ($_.Exception.Message -notmatch $Pattern) {
            throw "Unexpected failure: $($_.Exception.Message)"
        }
        return
    }
    throw "Expected failure matching: $Pattern"
}

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $scriptRoot "Private-Snapshot-E2E.Common.ps1")
if ($HoldMutexChild) {
    if ([string]::IsNullOrWhiteSpace($ReadyPath) -or
        [string]::IsNullOrWhiteSpace($ReleasePath)) {
        throw "Mutex child requires ready and release paths"
    }
    $childLock = Enter-YunPinPrivateSnapshotGateLock
    try {
        [IO.File]::WriteAllText($ReadyPath, "ready")
        for ($attempt = 0; $attempt -lt 150; $attempt++) {
            if (Test-Path -LiteralPath $ReleasePath -PathType Leaf) {
                return
            }
            Start-Sleep -Milliseconds 100
        }
        throw "Mutex child timed out waiting for release"
    } finally {
        Exit-YunPinPrivateSnapshotGateLock -Lock $childLock
    }
}

$repoRoot = [IO.Path]::GetFullPath((Join-Path $scriptRoot "..\..\.."))
$sourceOverlay = Join-Path $repoRoot "platform\windows\rime\rime_ice.custom.yaml"
$fixtureRoot = Join-Path ([IO.Path]::GetTempPath()) (
    "YunPinPrivateSnapshotGate-" + [guid]::NewGuid().ToString("N")
)

try {
    $appData = Join-Path $fixtureRoot "Roaming"
    $localAppData = Join-Path $fixtureRoot "Local"
    New-Item -ItemType Directory -Path $appData, $localAppData -Force | Out-Null
    $userData = Join-Path $appData "YunPin\Rime"
    $installRoot = Join-Path $localAppData "Programs\YunPinIME\Preview"
    $stateRoot = Join-Path $localAppData "YunPinIME\E2E\private-snapshot-gate"
    $paths = [pscustomobject]@{
        AppDataBase = $appData
        LocalAppDataBase = $localAppData
        UserDataRoot = $userData
        OverlayPath = Join-Path $userData "rime_ice.custom.yaml"
        PrivateSnapshotPath = Join-Path $userData "yunpin\private.tsv"
        InstallRoot = $installRoot
        RuntimeRoot = Join-Path $installRoot "current"
        DeployerPath = Join-Path (Join-Path $installRoot "current") "YunPinDeployer.exe"
        StateRoot = $stateRoot
        StatePath = Join-Path $stateRoot "state.json"
        BackupPath = Join-Path $stateRoot "rime_ice.custom.yaml.public-disabled.backup"
    }
    New-Item -ItemType Directory -Path $paths.UserDataRoot,
        (Split-Path -Parent $paths.PrivateSnapshotPath), $paths.RuntimeRoot -Force | Out-Null
    Copy-Item -LiteralPath $sourceOverlay -Destination $paths.OverlayPath
    [IO.File]::WriteAllText($paths.PrivateSnapshotPath, "fixture-private-body-not-for-gate`n")
    [IO.File]::WriteAllBytes($paths.DeployerPath, [byte[]](0x4d, 0x5a, 0x00, 0x00))

    $publicSha = Get-YunPinFileSha256 -Path $paths.OverlayPath
    $privateItem = Get-Item -LiteralPath $paths.PrivateSnapshotPath
    $privateLengthBefore = $privateItem.Length
    $privateWriteTimeBefore = $privateItem.LastWriteTimeUtc
    New-Item -ItemType Directory -Path $paths.StateRoot -Force | Out-Null
    $staleBackup = Join-Path $paths.StateRoot ".backup-11111111111111111111111111111111.tmp"
    $staleReplace = Join-Path $paths.UserDataRoot ".yunpin-e2e-replace-22222222222222222222222222222222.tmp"
    [IO.File]::WriteAllText($staleBackup, "stale")
    [IO.File]::WriteAllText($staleReplace, "stale")
    $script:enableDeployAttempts = 0
    $failEnableOnce = {
        $script:enableDeployAttempts++
        if ($script:enableDeployAttempts -eq 1) { throw "fixture deploy failure" }
    }
    Assert-Throws -Action {
        Invoke-YunPinPrivateSnapshotEnable -Paths $paths `
            -ExpectedPublicOverlaySha256 $publicSha -DeployAction $failEnableOnce
    } -Pattern "fixture deploy failure"
    Assert-True (-not (Test-Path -LiteralPath $staleBackup)) `
        "Enable resume left a stale durable-backup temporary file"
    Assert-True (-not (Test-Path -LiteralPath $staleReplace)) `
        "Enable resume left a stale atomic-replace temporary file"
    Assert-True (Test-Path -LiteralPath $paths.BackupPath -PathType Leaf) `
        "Backup was not retained after enable deploy failure"
    Assert-YunPinEnabledOverlay -Text (Read-YunPinStrictUtf8File -Path $paths.OverlayPath).Text

    $enabledResult = Invoke-YunPinPrivateSnapshotEnable -Paths $paths `
        -ExpectedPublicOverlaySha256 $publicSha -DeployAction $failEnableOnce
    Assert-True ($enabledResult -ceq "enabled") "Enable resume did not complete"
    Assert-True (Test-Path -LiteralPath $paths.BackupPath -PathType Leaf) `
        "Enable removed the rollback backup"
    $enabledState = Get-Content -LiteralPath $paths.StatePath -Raw | ConvertFrom-Json
    Assert-True ($enabledState.phase -ceq "enabled") "Enable state was not finalized"

    # Exact crash point for safe cleanup ordering: journal deletion has happened
    # but backup deletion has not. Disable must recover from backup + target.
    Remove-Item -LiteralPath $paths.StatePath -Force
    Assert-True (Test-Path -LiteralPath $paths.BackupPath -PathType Leaf) `
        "Cleanup-order crash fixture did not retain the durable backup"

    $script:disableDeployAttempts = 0
    $failDisableOnce = {
        $script:disableDeployAttempts++
        if ($script:disableDeployAttempts -eq 1) { throw "fixture disable deploy failure" }
    }
    Assert-Throws -Action {
        Invoke-YunPinPrivateSnapshotDisable -Paths $paths `
            -ExpectedPublicOverlaySha256 $publicSha -DeployAction $failDisableOnce
    } -Pattern "fixture disable deploy failure"
    Assert-True ((Get-YunPinFileSha256 -Path $paths.OverlayPath) -ceq $publicSha) `
        "Disable did not atomically restore the public overlay"
    Assert-True (Test-Path -LiteralPath $paths.BackupPath -PathType Leaf) `
        "Backup was not retained after disable deploy failure"

    $disabledResult = Invoke-YunPinPrivateSnapshotDisable -Paths $paths `
        -ExpectedPublicOverlaySha256 $publicSha -DeployAction $failDisableOnce
    Assert-True ($disabledResult -ceq "disabled") "Disable resume did not complete"
    Assert-True (-not (Test-Path -LiteralPath $paths.BackupPath)) `
        "Backup remained after successful disabled deploy"
    $privateAfter = Get-Item -LiteralPath $paths.PrivateSnapshotPath
    Assert-True ($privateAfter.Length -eq $privateLengthBefore -and
        $privateAfter.LastWriteTimeUtc -eq $privateWriteTimeBefore) `
        "The private snapshot fixture metadata changed"

    Copy-Item -LiteralPath $sourceOverlay -Destination $paths.OverlayPath -Force
    Add-Content -LiteralPath $paths.OverlayPath -Value '  yunpin/enabled: false'
    Assert-Throws -Action {
        Invoke-YunPinPrivateSnapshotEnable -Paths $paths `
            -ExpectedPublicOverlaySha256 (Get-YunPinFileSha256 -Path $paths.OverlayPath) `
            -DeployAction { }
    } -Pattern "exactly one yunpin/enabled"
    Assert-True (-not (Test-Path -LiteralPath $paths.BackupPath)) `
        "Duplicate enabled gate created a backup"
    if (Test-Path -LiteralPath $paths.StateRoot -PathType Container) {
        Remove-Item -LiteralPath $paths.StateRoot -Recurse -Force
    }

    Copy-Item -LiteralPath $sourceOverlay -Destination $paths.OverlayPath -Force
    $unsafeLearning = (Get-Content -LiteralPath $paths.OverlayPath -Raw).Replace(
        '"yunpin/session_learning": false', '"yunpin/session_learning": true'
    )
    [IO.File]::WriteAllText($paths.OverlayPath, $unsafeLearning, (New-Object Text.UTF8Encoding($false)))
    Assert-Throws -Action {
        Invoke-YunPinPrivateSnapshotEnable -Paths $paths `
            -ExpectedPublicOverlaySha256 (Get-YunPinFileSha256 -Path $paths.OverlayPath) `
            -DeployAction { }
    } -Pattern "session_learning"
    if (Test-Path -LiteralPath $paths.StateRoot -PathType Container) {
        Remove-Item -LiteralPath $paths.StateRoot -Recurse -Force
    }

    Copy-Item -LiteralPath $sourceOverlay -Destination $paths.OverlayPath -Force
    $fakeOwner = New-Object Security.Principal.SecurityIdentifier("S-1-5-18")
    if ($fakeOwner.Value -ceq (Get-YunPinCurrentUserSid).Value) {
        $fakeOwner = New-Object Security.Principal.SecurityIdentifier("S-1-5-32-544")
    }
    Assert-Throws -Action {
        Assert-YunPinOwnedNonReparsePath -Path $paths.OverlayPath `
            -ExpectedOwnerSid $fakeOwner
    } -Pattern "not owned by the current user"

    $junctionTarget = Join-Path $fixtureRoot "junction-target"
    $junctionPath = Join-Path $fixtureRoot "junction"
    New-Item -ItemType Directory -Path $junctionTarget | Out-Null
    New-Item -ItemType Junction -Path $junctionPath -Target $junctionTarget | Out-Null
    Assert-Throws -Action {
        Assert-YunPinOwnedNonReparsePath -Path $junctionPath
    } -Pattern "reparse point"

    $readyPath = Join-Path $fixtureRoot "mutex-ready"
    $releasePath = Join-Path $fixtureRoot "mutex-release"
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = (Get-Process -Id $PID).Path
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in @(
        "-NoLogo", "-NoProfile", "-NonInteractive", "-File", $MyInvocation.MyCommand.Path,
        "-HoldMutexChild", "-ReadyPath", $readyPath, "-ReleasePath", $releasePath
    )) {
        [void]$startInfo.ArgumentList.Add($argument)
    }
    $child = [Diagnostics.Process]::new()
    $child.StartInfo = $startInfo
    $childStarted = $false
    try {
        $childStarted = $child.Start()
        Assert-True $childStarted "Failed to start the mutex fixture child"
        for ($attempt = 0; $attempt -lt 100; $attempt++) {
            if (Test-Path -LiteralPath $readyPath -PathType Leaf) { break }
            if ($child.HasExited) {
                throw "Mutex fixture child exited early: $($child.StandardError.ReadToEnd())"
            }
            Start-Sleep -Milliseconds 100
        }
        Assert-True (Test-Path -LiteralPath $readyPath -PathType Leaf) `
            "Mutex fixture child did not acquire the fixed lock"
        Assert-Throws -Action {
            $unexpectedLock = Enter-YunPinPrivateSnapshotGateLock
            try { } finally { Exit-YunPinPrivateSnapshotGateLock -Lock $unexpectedLock }
        } -Pattern "Another private snapshot E2E gate process is active"
        [IO.File]::WriteAllText($releasePath, "release")
        Assert-True ($child.WaitForExit(10000)) "Mutex fixture child did not exit"
        Assert-True ($child.ExitCode -eq 0) `
            ("Mutex fixture child failed: " + $child.StandardError.ReadToEnd())
    } finally {
        if ($childStarted -and -not $child.HasExited) {
            $child.Kill()
            $child.WaitForExit()
        }
        $child.Dispose()
    }
} finally {
    if (Test-Path -LiteralPath $fixtureRoot) {
        Remove-Item -LiteralPath $fixtureRoot -Recurse -Force
    }
}

Write-Host "Windows private snapshot E2E gate fixture tests passed."
