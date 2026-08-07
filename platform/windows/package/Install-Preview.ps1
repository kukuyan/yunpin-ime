# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [switch]$AcceptUnsignedDevelopmentBuild,
    [string]$InstallRoot = "",
    [string]$UserDataRoot = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-CheckedExecutable {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $false)][string[]]$Arguments = @()
    )
    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed with exit code ${LASTEXITCODE}: $FilePath $($Arguments -join ' ')"
    }
}

function Assert-BundleManifest {
    param([Parameter(Mandatory = $true)][string]$BundleRoot)
    $manifest = Join-Path $BundleRoot "MANIFEST.sha256"
    if (-not (Test-Path $manifest -PathType Leaf)) {
        throw "MANIFEST.sha256 is missing"
    }
    $prefix = [IO.Path]::GetFullPath($BundleRoot).TrimEnd("\") + "\"
    foreach ($line in Get-Content -LiteralPath $manifest) {
        if ($line -notmatch '^([0-9a-f]{64})  (.+)$') {
            throw "Malformed manifest row: $line"
        }
        $relative = $Matches[2].Replace("/", "\")
        $path = [IO.Path]::GetFullPath((Join-Path $BundleRoot $relative))
        if (-not $path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Manifest path escapes the bundle: $relative"
        }
        if (-not (Test-Path $path -PathType Leaf)) {
            throw "Manifest file is missing: $relative"
        }
        $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        if ($observed -ne $Matches[1]) {
            throw "Manifest hash mismatch: $relative"
        }
    }
}

function Copy-OverlayWithBackup {
    param(
        [Parameter(Mandatory = $true)][string]$SourceRoot,
        [Parameter(Mandatory = $true)][string]$DestinationRoot,
        [Parameter(Mandatory = $true)][string]$BackupRoot
    )
    $sourcePrefix = [IO.Path]::GetFullPath($SourceRoot).TrimEnd("\") + "\"
    foreach ($source in Get-ChildItem -LiteralPath $SourceRoot -File -Recurse) {
        $relative = $source.FullName.Substring($sourcePrefix.Length)
        $destination = Join-Path $DestinationRoot $relative
        if (Test-Path $destination -PathType Leaf) {
            $oldHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $destination).Hash
            $newHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $source.FullName).Hash
            if ($oldHash -ne $newHash) {
                $backup = Join-Path $BackupRoot $relative
                New-Item -ItemType Directory -Path (Split-Path $backup -Parent) -Force | Out-Null
                Copy-Item -LiteralPath $destination -Destination $backup -Force
            }
        }
        New-Item -ItemType Directory -Path (Split-Path $destination -Parent) -Force | Out-Null
        Copy-Item -LiteralPath $source.FullName -Destination $destination -Force
    }
}

if (-not $AcceptUnsignedDevelopmentBuild) {
    throw "This archive is unsigned and for development only. Re-run with -AcceptUnsignedDevelopmentBuild after reading README.txt."
}
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT -or -not [Environment]::Is64BitOperatingSystem) {
    throw "YunPin Windows preview requires 64-bit Windows"
}
$windowsVersion = [Environment]::OSVersion.Version
if ($windowsVersion.Major -lt 10 -or $windowsVersion.Build -lt 19045) {
    throw "YunPin Windows preview requires Windows 10 22H2 (build 19045) or newer"
}

$bundleRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
Assert-BundleManifest -BundleRoot $bundleRoot
$metadata = Get-Content -LiteralPath (Join-Path $bundleRoot "BUILD-METADATA.json") -Raw | ConvertFrom-Json
if ($metadata.signed -ne $false -or $metadata.productionReady -ne $false -or $metadata.privateCandidateSnapshotEnabled -ne $false) {
    throw "Unexpected development-preview metadata"
}
$privateConfig = Get-Content -LiteralPath (Join-Path $bundleRoot "rime-data\rime_ice.custom.yaml") -Raw
if ($privateConfig -notmatch '(?m)^\s*"yunpin/enabled": false\s*$') {
    throw "Private candidate snapshot must remain disabled in this preview"
}

if ([string]::IsNullOrWhiteSpace($InstallRoot)) {
    $InstallRoot = Join-Path $env:LOCALAPPDATA "Programs\YunPinIME\Preview"
}
if ([string]::IsNullOrWhiteSpace($UserDataRoot)) {
    $UserDataRoot = Join-Path $env:APPDATA "YunPin\Rime"
}
$InstallRoot = [IO.Path]::GetFullPath($InstallRoot)
$UserDataRoot = [IO.Path]::GetFullPath($UserDataRoot)
$timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
$incoming = Join-Path $InstallRoot ("incoming-" + [guid]::NewGuid().ToString("N"))
$current = Join-Path $InstallRoot "current"
$backupRoot = Join-Path $InstallRoot "previous"
$previous = Join-Path $backupRoot $timestamp
$userBackup = Join-Path $UserDataRoot ("yunpin-preview-backups\" + $timestamp)
New-Item -ItemType Directory -Path $InstallRoot, $backupRoot, $UserDataRoot -Force | Out-Null

try {
    $supportRoot = Join-Path $InstallRoot "support"
    New-Item -ItemType Directory -Path $supportRoot -Force | Out-Null
    foreach ($supportFile in @(
        "Install-Preview.ps1", "Uninstall-Preview.ps1", "README.txt",
        "BUILD-METADATA.json", "MANIFEST.sha256"
    )) {
        Copy-Item -LiteralPath (Join-Path $bundleRoot $supportFile) -Destination $supportRoot -Force
    }
    New-Item -ItemType Directory -Path $incoming -Force | Out-Null
    Get-ChildItem -LiteralPath (Join-Path $bundleRoot "runtime") -Force | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $incoming -Recurse -Force
    }

    $existingServer = Join-Path $current "YunPinServer.exe"
    if (Test-Path $existingServer -PathType Leaf) {
        & $existingServer "/quit" | Out-Null
    }
    if (Test-Path $current -PathType Container) {
        Move-Item -LiteralPath $current -Destination $previous
    }
    Move-Item -LiteralPath $incoming -Destination $current

    Copy-OverlayWithBackup -SourceRoot (Join-Path $bundleRoot "rime-data") -DestinationRoot $UserDataRoot -BackupRoot $userBackup

    $setup = Join-Path $current "YunPinSetup.exe"
    $deployer = Join-Path $current "YunPinDeployer.exe"
    $server = Join-Path $current "YunPinServer.exe"
    Invoke-CheckedExecutable -FilePath $setup -Arguments @(('/userdir:' + $UserDataRoot))
    Invoke-CheckedExecutable -FilePath $setup -Arguments @('/du')
    Invoke-CheckedExecutable -FilePath $setup -Arguments @('/s')
    Invoke-CheckedExecutable -FilePath $deployer -Arguments @('/deploy')

    $runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
    New-Item -Path $runKey -Force | Out-Null
    New-ItemProperty -Path $runKey -Name "YunPinIMEPreview" -PropertyType String -Value ('"' + $server + '"') -Force | Out-Null
    Start-Process -FilePath $server | Out-Null

    $state = [ordered]@{
        schemaVersion = 1
        installedAtUtc = [DateTime]::UtcNow.ToString("o")
        currentRuntime = $current
        userData = $UserDataRoot
        userOverlayBackup = $userBackup
        previousRuntime = $(if (Test-Path $previous) { $previous } else { $null })
        unsignedDevelopmentBuild = $true
    }
    $state | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $InstallRoot "install-state.json") -Encoding UTF8
} catch {
    if (Test-Path $incoming -PathType Container) {
        Move-Item -LiteralPath $incoming -Destination (Join-Path $InstallRoot ("failed-" + [guid]::NewGuid().ToString("N")))
    }
    throw
}

Write-Host "YunPin Windows development preview installed."
Write-Host "Runtime: $current"
Write-Host "User data: $UserDataRoot"
Write-Host "Private YunPin candidates remain disabled pending the secure-input and IPC gates."
