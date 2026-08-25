# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [switch]$AcceptUnsignedDevelopmentBuild,
    [string]$InstallRoot = "",
    [string]$UserDataRoot = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function ConvertTo-NativeCommandLineArgument {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Argument)

    if ($Argument.Length -gt 0 -and $Argument -notmatch '[\s"]') {
        return $Argument
    }

    $quoted = New-Object Text.StringBuilder
    [void]$quoted.Append([char]34)
    $backslashes = 0
    foreach ($character in $Argument.ToCharArray()) {
        if ($character -eq [char]92) {
            $backslashes++
            continue
        }
        if ($character -eq [char]34) {
            [void]$quoted.Append([char]92, (2 * $backslashes) + 1)
            [void]$quoted.Append([char]34)
            $backslashes = 0
            continue
        }
        if ($backslashes -gt 0) {
            [void]$quoted.Append([char]92, $backslashes)
            $backslashes = 0
        }
        [void]$quoted.Append($character)
    }
    if ($backslashes -gt 0) {
        [void]$quoted.Append([char]92, 2 * $backslashes)
    }
    [void]$quoted.Append([char]34)
    return $quoted.ToString()
}

function Invoke-CheckedExecutable {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $false)][string[]]$Arguments = @()
    )
    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $FilePath
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    if ($Arguments.Count -gt 0) {
        $startInfo.Arguments = (($Arguments | ForEach-Object {
            ConvertTo-NativeCommandLineArgument -Argument $_
        }) -join " ")
    }
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        throw "Failed to start executable: $FilePath"
    }
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) {
        throw "Command failed with exit code $($process.ExitCode): $FilePath $($Arguments -join ' ')"
    }
}

function Set-YunPinMachineRegistry64 {
    param([Parameter(Mandatory = $true)][string]$RuntimeRoot)

    $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey(
        [Microsoft.Win32.RegistryHive]::LocalMachine,
        [Microsoft.Win32.RegistryView]::Registry64
    )
    $path = "Software\YunPin\IME"
    try {
        $existing = $base.OpenSubKey($path)
        try {
            if ($existing) {
                $registeredRoot = $existing.GetValue("WeaselRoot")
                if ($registeredRoot -and $registeredRoot -ne $RuntimeRoot) {
                    throw "A different 64-bit YunPin runtime is already registered: $registeredRoot"
                }
            }
        } finally {
            if ($existing) {
                $existing.Dispose()
            }
        }
        $key = $base.CreateSubKey($path)
        try {
            $key.SetValue(
                "WeaselRoot",
                $RuntimeRoot,
                [Microsoft.Win32.RegistryValueKind]::String
            )
            $key.SetValue(
                "ServerExecutable",
                "YunPinServer.exe",
                [Microsoft.Win32.RegistryValueKind]::String
            )
        } finally {
            $key.Dispose()
        }
    } finally {
        $base.Dispose()
    }
}

function Assert-BundleManifest {
    param([Parameter(Mandatory = $true)][string]$BundleRoot)
    $manifest = Join-Path $BundleRoot "MANIFEST.sha256"
    if (-not (Test-Path $manifest -PathType Leaf)) {
        throw "MANIFEST.sha256 is missing"
    }
    $prefix = [IO.Path]::GetFullPath($BundleRoot).TrimEnd("\") + "\"
    $expected = @{}
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
        $expected[$Matches[2]] = $Matches[1]
    }
    return $expected
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

function Read-YunPinStrictUtf8File {
    param(
        [Parameter(Mandatory = $true)][string]$Path
    )
    $strictUtf8 = New-Object Text.UTF8Encoding($false, $true)
    try {
        $content = [IO.File]::ReadAllText($Path, $strictUtf8)
    } catch [Text.DecoderFallbackException] {
        throw "YunPin text file is not valid UTF-8: $Path"
    }
    if ($content.Contains([char]0xfffd)) {
        throw "YunPin text file contains the Unicode replacement character: $Path"
    }
    return $content
}

function Get-YunPinBooleanOptIn {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Name
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return $false
    }
    $content = Read-YunPinStrictUtf8File -Path $Path
    $key = [regex]::Escape($Name)
    $truePattern = '(?m)^[ \t]*"' + $key + '"[ \t]*:[ \t]*true[ \t]*(?:#[^\r\n]*)?\r?$'
    $falsePattern = '(?m)^[ \t]*"' + $key + '"[ \t]*:[ \t]*false[ \t]*(?:#[^\r\n]*)?\r?$'
    return (
        [regex]::Matches($content, $truePattern).Count -eq 1 -and
        [regex]::Matches($content, $falsePattern).Count -eq 0
    )
}

function Preserve-YunPinBooleanOptIns {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][bool]$PrivateCandidates,
        [Parameter(Mandatory = $true)][bool]$SessionLearning
    )
    if (-not $PrivateCandidates -and -not $SessionLearning) {
        return
    }
    $content = Read-YunPinStrictUtf8File -Path $Path
    foreach ($choice in @(
        @{ Name = 'yunpin/enabled'; Preserve = $PrivateCandidates },
        @{ Name = 'yunpin/session_learning'; Preserve = $SessionLearning }
    )) {
        if (-not $choice.Preserve) {
            continue
        }
        $key = [regex]::Escape([string]$choice.Name)
        $falsePattern = '(?m)^(?<prefix>[ \t]*"' + $key + '"[ \t]*:[ \t]*)false(?<suffix>[ \t]*(?:#[^\r\n]*)?\r?)$'
        if ([regex]::Matches($content, $falsePattern).Count -ne 1) {
            throw "Packaged overlay does not contain one disabled $($choice.Name) setting."
        }
        $content = [regex]::Replace($content, $falsePattern, '${prefix}true${suffix}')
    }

    $attempt = [guid]::NewGuid().ToString('N')
    $temporary = $Path + '.preserve-' + $attempt + '.tmp'
    $metadataBackup = $Path + '.preserve-' + $attempt + '.bak'
    try {
        [IO.File]::WriteAllText($temporary, $content, (New-Object Text.UTF8Encoding($false, $true)))
        if ((Read-YunPinStrictUtf8File -Path $temporary) -cne $content) {
            throw "UTF-8 overlay staging did not preserve the decoded configuration exactly."
        }
        [IO.File]::Replace($temporary, $Path, $metadataBackup, $true)
    } finally {
        Remove-Item -LiteralPath $temporary, $metadataBackup -Force -ErrorAction SilentlyContinue
    }
    if (($PrivateCandidates -and -not (Get-YunPinBooleanOptIn -Path $Path -Name 'yunpin/enabled')) -or
        ($SessionLearning -and -not (Get-YunPinBooleanOptIn -Path $Path -Name 'yunpin/session_learning'))) {
        throw "Existing YunPin opt-in settings were not preserved."
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
$bundleManifest = Assert-BundleManifest -BundleRoot $bundleRoot
$metadata = Get-Content -LiteralPath (Join-Path $bundleRoot "BUILD-METADATA.json") -Raw | ConvertFrom-Json
if ($metadata.signed -ne $false -or $metadata.productionReady -ne $false -or $metadata.privateCandidateSnapshotEnabled -ne $false) {
    throw "Unexpected development-preview metadata"
}
$privateConfig = Read-YunPinStrictUtf8File -Path (Join-Path $bundleRoot "rime-data\rime_ice.custom.yaml")
if ($privateConfig -notmatch '(?m)^\s*"yunpin/enabled": false\s*$') {
    throw "Private candidate snapshot must remain disabled in this preview"
}
if ($privateConfig -notmatch '(?m)^\s*"yunpin/short_input_guard": true\s*$') {
    throw "Short-input upstream guard must remain enabled in this preview"
}
if ($privateConfig -notmatch '(?m)^\s*"yunpin/session_learning": false\s*$') {
    throw "Session learning must remain disabled until the Windows secure-input gate passes"
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
$existingRimeOverlay = Join-Path $UserDataRoot "rime_ice.custom.yaml"
$preservePrivateCandidates = Get-YunPinBooleanOptIn -Path $existingRimeOverlay -Name 'yunpin/enabled'
$preserveSessionLearning = Get-YunPinBooleanOptIn -Path $existingRimeOverlay -Name 'yunpin/session_learning'

try {
    $supportRoot = Join-Path $InstallRoot "support"
    New-Item -ItemType Directory -Path $supportRoot -Force | Out-Null
    foreach ($supportFile in @(
        "Install-Preview.ps1", "Uninstall-Preview.ps1", "README.txt",
        "BUILD-METADATA.json", "MANIFEST.sha256"
    )) {
        Copy-Item -LiteralPath (Join-Path $bundleRoot $supportFile) -Destination $supportRoot -Force
    }
    $syncBundleRoot = Join-Path $bundleRoot "sync-agent"
    $syncSupportRoot = Join-Path $supportRoot "sync-agent"
    New-Item -ItemType Directory -Path $syncSupportRoot -Force | Out-Null
    Get-ChildItem -LiteralPath $syncBundleRoot -File | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $syncSupportRoot -Force
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
    Preserve-YunPinBooleanOptIns -Path (Join-Path $UserDataRoot "rime_ice.custom.yaml") `
        -PrivateCandidates $preservePrivateCandidates -SessionLearning $preserveSessionLearning

    $setup = Join-Path $current "YunPinSetup.exe"
    $deployer = Join-Path $current "YunPinDeployer.exe"
    $server = Join-Path $current "YunPinServer.exe"
    Invoke-CheckedExecutable -FilePath $setup -Arguments @(('/userdir:' + $UserDataRoot))
    Invoke-CheckedExecutable -FilePath $setup -Arguments @('/du')
    Invoke-CheckedExecutable -FilePath $setup -Arguments @('/s')
    Set-YunPinMachineRegistry64 -RuntimeRoot $current
    Invoke-CheckedExecutable -FilePath $deployer -Arguments @('/deploy')

    $runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
    New-Item -Path $runKey -Force | Out-Null
    New-ItemProperty -Path $runKey -Name "YunPinIMEPreview" -PropertyType String -Value ('"' + $server + '"') -Force | Out-Null
    Start-Process -FilePath $server | Out-Null

    $syncInstaller = Join-Path $syncSupportRoot "Install-SyncAgent.ps1"
    $syncVerifier = Join-Path $syncSupportRoot "Verify-SyncAgent.ps1"
    $syncAgent = Join-Path $syncSupportRoot "yunpin-sync-agent.exe"
    $syncResident = Join-Path $syncSupportRoot "yunpin-sync-resident.exe"
    $syncManifestPath = "sync-agent/yunpin-sync-agent.exe"
    $syncResidentManifestPath = "sync-agent/yunpin-sync-resident.exe"
    if (-not $bundleManifest.ContainsKey($syncManifestPath)) {
        throw "Public sync agent is absent from the verified bundle manifest."
    }
    if (-not $bundleManifest.ContainsKey($syncResidentManifestPath)) {
        throw "Windowless sync resident is absent from the verified bundle manifest."
    }
    & $syncInstaller -AgentPath $syncAgent -ExpectedSha256 $bundleManifest[$syncManifestPath] `
        -ResidentPath $syncResident -ResidentExpectedSha256 $bundleManifest[$syncResidentManifestPath]
    & $syncVerifier

    $state = [ordered]@{
        schemaVersion = 1
        installedAtUtc = [DateTime]::UtcNow.ToString("o")
        currentRuntime = $current
        userData = $UserDataRoot
        userOverlayBackup = $userBackup
        previousRuntime = $(if (Test-Path $previous) { $previous } else { $null })
        registry64Runtime = $current
        unsignedDevelopmentBuild = $true
        syncAgentRegistration = "disabled"
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
Write-Host "YunPinSyncAgent is installed but its scheduled task remains disabled pending private E2E setup."
