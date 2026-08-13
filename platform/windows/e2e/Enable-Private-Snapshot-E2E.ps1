# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [switch]$ConfirmPrivateSnapshotE2E,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9A-Fa-f]{64}$')]
    [string]$ExpectedPublicOverlaySha256
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (-not $ConfirmPrivateSnapshotE2E) {
    throw "Private snapshot activation is E2E-only. Re-run with -ConfirmPrivateSnapshotE2E."
}
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "The private snapshot E2E gate runs only on Windows"
}

$artifactRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $artifactRoot "Private-Snapshot-E2E.Common.ps1")
$gateLock = Enter-YunPinPrivateSnapshotGateLock
try {
    $expected = ConvertTo-YunPinSha256 -Value $ExpectedPublicOverlaySha256
    Assert-YunPinPrivateArtifactBinding -ArtifactRoot $artifactRoot `
        -ExpectedPublicOverlaySha256 $expected
    $paths = Get-YunPinPrivateSnapshotFixedPaths
    $deploy = { Invoke-YunPinDeployer -Path $paths.DeployerPath }.GetNewClosure()
    $result = Invoke-YunPinPrivateSnapshotEnable -Paths $paths `
        -ExpectedPublicOverlaySha256 $expected -DeployAction $deploy
} finally {
    Exit-YunPinPrivateSnapshotGateLock -Lock $gateLock
}

Write-Host "Private snapshot E2E gate result: $result"
Write-Host "session_learning remains uniquely false."
Write-Host "Overlay-only gate: this does not set yunpin_learning_allowed or prove candidate visibility."
