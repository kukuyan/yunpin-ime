# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [switch]$ConfirmUninstall,
    [string]$InstallRoot = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-CheckedExecutable {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $false)][string[]]$Arguments = @()
    )
    $startParameters = @{
        FilePath = $FilePath
        Wait = $true
        PassThru = $true
    }
    if ($Arguments.Count -gt 0) {
        $startParameters.ArgumentList = $Arguments
    }
    $process = Start-Process @startParameters
    if ($process.ExitCode -ne 0) {
        throw "Command failed with exit code $($process.ExitCode): $FilePath $($Arguments -join ' ')"
    }
}

if (-not $ConfirmUninstall) {
    throw "Re-run with -ConfirmUninstall. The runtime will be retired; user dictionaries are retained."
}
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Uninstall-Preview.ps1 must run on Windows"
}
if ([string]::IsNullOrWhiteSpace($InstallRoot)) {
    $InstallRoot = Join-Path $env:LOCALAPPDATA "Programs\YunPinIME\Preview"
}
$InstallRoot = [IO.Path]::GetFullPath($InstallRoot)
$current = Join-Path $InstallRoot "current"
$setup = Join-Path $current "YunPinSetup.exe"
$server = Join-Path $current "YunPinServer.exe"
if (-not (Test-Path $setup -PathType Leaf)) {
    throw "Installed YunPin preview runtime was not found at $current"
}

if (Test-Path $server -PathType Leaf) {
    & $server "/quit" | Out-Null
}
Invoke-CheckedExecutable -FilePath $setup -Arguments @('/u')
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
Remove-ItemProperty -Path $runKey -Name "YunPinIMEPreview" -ErrorAction SilentlyContinue

$retiredRoot = Join-Path $InstallRoot "retired"
New-Item -ItemType Directory -Path $retiredRoot -Force | Out-Null
$retired = Join-Path $retiredRoot ([DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ"))
Move-Item -LiteralPath $current -Destination $retired
$state = [ordered]@{
    schemaVersion = 1
    uninstalledAtUtc = [DateTime]::UtcNow.ToString("o")
    retiredRuntime = $retired
    userDataRetained = $true
}
$state | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $InstallRoot "uninstall-state.json") -Encoding UTF8

Write-Host "YunPin preview unregistered. Runtime retained at: $retired"
Write-Host "User dictionaries were not removed."
