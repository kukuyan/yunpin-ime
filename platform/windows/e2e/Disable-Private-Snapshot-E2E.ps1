# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [switch]$ConfirmDisablePrivateSnapshotE2E,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9A-Fa-f]{64}$')]
    [string]$ExpectedPublicOverlaySha256
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (-not $ConfirmDisablePrivateSnapshotE2E) {
    throw "Private snapshot rollback requires -ConfirmDisablePrivateSnapshotE2E."
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
    $result = Invoke-YunPinPrivateSnapshotDisable -Paths $paths `
        -ExpectedPublicOverlaySha256 $expected -DeployAction $deploy
} finally {
    Exit-YunPinPrivateSnapshotGateLock -Lock $gateLock
}

Write-Host "Private snapshot E2E rollback result: $result"
Write-Host "The durable backup was retained until /deploy succeeded."
