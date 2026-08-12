# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$OutputRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-GoBuild {
    param(
        [Parameter(Mandatory = $true)][string]$AgentRoot,
        [Parameter(Mandatory = $true)][string]$Output,
        [string]$BuildTag = ""
    )
    $arguments = @("build", "-trimpath", "-buildvcs=false")
    if (-not [string]::IsNullOrWhiteSpace($BuildTag)) {
        $arguments += ("-tags=" + $BuildTag)
    }
    $arguments += @("-o", $Output, "./cmd/yunpin-sync-agent")
    Push-Location $AgentRoot
    try {
        & go @arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Go sync-agent build failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
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

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Build-SyncAgents.ps1 must run on Windows"
}
if (-not [Environment]::Is64BitOperatingSystem) {
    throw "The YunPin sync agent requires a 64-bit Windows build host"
}
if ($null -eq (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is required to build the YunPin sync agent"
}

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = [IO.Path]::GetFullPath((Join-Path $scriptRoot "..\..\.."))
$agentRoot = Join-Path $repoRoot "desktopagent"
$OutputRoot = [IO.Path]::GetFullPath($OutputRoot)
$publicRoot = Join-Path $OutputRoot "desktopagent\public"
$privateRoot = Join-Path $OutputRoot "e2e-private\windows"
New-Item -ItemType Directory -Path $publicRoot, $privateRoot -Force | Out-Null
$publicBinary = Join-Path $publicRoot "yunpin-sync-agent.exe"
$privateBinary = Join-Path $privateRoot "yunpin-sync-agent.exe"
Remove-Item -LiteralPath $publicBinary, $privateBinary,
    (Join-Path $privateRoot "BUILD-METADATA.json"),
    (Join-Path $privateRoot "SHA256SUMS") -Force -ErrorAction SilentlyContinue

Push-Location $agentRoot
try {
    & go mod verify
    if ($LASTEXITCODE -ne 0) { throw "Go module verification failed" }
} finally {
    Pop-Location
}

$savedEnvironment = @{
    CGO_ENABLED = $env:CGO_ENABLED
    GOOS = $env:GOOS
    GOARCH = $env:GOARCH
}
try {
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    Invoke-GoBuild -AgentRoot $agentRoot -Output $publicBinary
    Invoke-GoBuild -AgentRoot $agentRoot -Output $privateBinary -BuildTag "yunpin_pairing_private"
} finally {
    foreach ($name in $savedEnvironment.Keys) {
        if ($null -eq $savedEnvironment[$name]) {
            Remove-Item ("Env:" + $name) -ErrorAction SilentlyContinue
        } else {
            Set-Item ("Env:" + $name) $savedEnvironment[$name]
        }
    }
}

$probe = Invoke-AgentCapture -Executable $publicBinary -Arguments @("install-probe")
if ($probe.ExitCode -ne 0) {
    throw "Public sync agent install-probe failed"
}
$publicPrivate = Invoke-AgentCapture -Executable $publicBinary -Arguments @("pairing-invite")
if ($publicPrivate.ExitCode -eq 0 -or $publicPrivate.Output -cne "yunpin-sync-agent: unknown command") {
    throw "Public sync agent exposes a private pairing command"
}
$privateGate = Invoke-AgentCapture -Executable $privateBinary -Arguments @("pairing-invite")
if ($privateGate.ExitCode -eq 0 -or
    -not $privateGate.Output.Contains("pairing-invite requires --confirm-display-invitation")) {
    throw "Private E2E sync agent does not expose the confirmation-gated pairing command"
}

$licenseRoot = Join-Path $OutputRoot "desktopagent\licenses"
& python (Join-Path $repoRoot "scripts\package_go_licenses.py") `
    --go-module $agentRoot --output $licenseRoot
if ($LASTEXITCODE -ne 0) { throw "Sync-agent license bundle generation failed" }
Copy-Item -LiteralPath $licenseRoot -Destination (Join-Path $privateRoot "licenses") -Recurse -Force

$repoCommit = ""
if (Test-Path (Join-Path $repoRoot ".git")) {
    $repoCommit = (& git -C $repoRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Unable to resolve repository commit" }
} elseif (Test-Path (Join-Path $repoRoot "BUILD-SOURCE-METADATA.json") -PathType Leaf) {
    $sourceMetadata = Get-Content -LiteralPath (Join-Path $repoRoot "BUILD-SOURCE-METADATA.json") -Raw | ConvertFrom-Json
    $repoCommit = [string]$sourceMetadata.repositoryCommit
} else {
    $repoCommit = "source-export"
}
$metadata = [ordered]@{
    schemaVersion = 1
    repositoryCommit = $repoCommit
    target = "windows-amd64"
    buildTag = "yunpin_pairing_private"
    purpose = "private Mac-R0W E2E acceptance only"
    publicReleaseEligible = $false
}
$metadataPath = Join-Path $privateRoot "BUILD-METADATA.json"
$metadata | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $metadataPath -Encoding UTF8
$privatePrefix = $privateRoot.TrimEnd("\") + "\"
$hashRows = @(Get-ChildItem -LiteralPath $privateRoot -File -Recurse | Where-Object {
    $_.Name -ne "SHA256SUMS"
} | Sort-Object FullName | ForEach-Object {
    $relative = $_.FullName.Substring($privatePrefix.Length).Replace("\", "/")
    (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant() + "  " + $relative
})
[IO.File]::WriteAllLines((Join-Path $privateRoot "SHA256SUMS"), $hashRows, ([Text.UTF8Encoding]::new($false)))

Write-Host "Built public Windows sync agent: $publicBinary"
Write-Host "Built private E2E-only Windows sync agent: $privateBinary"
