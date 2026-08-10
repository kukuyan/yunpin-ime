# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [switch]$ConfirmUninstall,
    [string]$InstallRoot = ""
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

function Remove-YunPinMachineRegistry64 {
    param([Parameter(Mandatory = $true)][string]$ExpectedRuntimeRoot)

    $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey(
        [Microsoft.Win32.RegistryHive]::LocalMachine,
        [Microsoft.Win32.RegistryView]::Registry64
    )
    $path = "Software\YunPin\IME"
    try {
        $key = $base.OpenSubKey($path)
        try {
            if (-not $key) {
                return
            }
            $registeredRoot = $key.GetValue("WeaselRoot")
            if ($registeredRoot -and $registeredRoot -ne $ExpectedRuntimeRoot) {
                throw "Refusing to remove a different 64-bit YunPin runtime: $registeredRoot"
            }
        } finally {
            if ($key) {
                $key.Dispose()
            }
        }
        $parent = $base.OpenSubKey("Software\YunPin", $true)
        try {
            if ($parent) {
                $parent.DeleteSubKeyTree("IME", $false)
            }
        } finally {
            if ($parent) {
                $parent.Dispose()
            }
        }
    } finally {
        $base.Dispose()
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
Remove-YunPinMachineRegistry64 -ExpectedRuntimeRoot $current
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
