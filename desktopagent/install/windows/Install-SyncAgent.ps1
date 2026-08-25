# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$AgentPath,
    [Parameter(Mandatory = $true)][ValidatePattern('^[0-9a-fA-F]{64}$')][string]$ExpectedSha256,
    # The windowless background process. Go links console-subsystem binaries by
    # default, so a scheduled task running the interactive agent in the user's
    # session gets a console window that stays for the life of the process. The
    # task therefore runs this GUI-subsystem binary instead; the interactive one
    # is still installed for status, configuration and pairing.
    [Parameter(Mandatory = $true)][string]$ResidentPath,
    [Parameter(Mandatory = $true)][ValidatePattern('^[0-9a-fA-F]{64}$')][string]$ResidentExpectedSha256
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Install-SyncAgent.ps1 must run as the signed-in Windows user."
}
$localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
if ([string]::IsNullOrWhiteSpace($localAppData)) { throw "Known Folder LOCALAPPDATA is unavailable." }
function Resolve-VerifiedBinary {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Sha256,
        [Parameter(Mandatory = $true)][string]$Label
    )
    $resolved = [IO.Path]::GetFullPath($Path)
    if (-not [IO.Path]::IsPathRooted($Path) -or -not (Test-Path -LiteralPath $resolved -PathType Leaf)) {
        throw "$Label must name an absolute existing file."
    }
    if ([IO.Path]::GetExtension($resolved) -ne ".exe") {
        throw "$Label must be a Windows executable."
    }
    $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $resolved).Hash.ToLowerInvariant()
    if ($observed -cne $Sha256.ToLowerInvariant()) {
        throw "$Label SHA-256 does not match the manifest-bound expected value."
    }
    return $resolved
}

$expectedHash = $ExpectedSha256.ToLowerInvariant()
$residentExpectedHash = $ResidentExpectedSha256.ToLowerInvariant()
$source = Resolve-VerifiedBinary -Path $AgentPath -Sha256 $expectedHash -Label "AgentPath"
$residentSource = Resolve-VerifiedBinary -Path $ResidentPath -Sha256 $residentExpectedHash -Label "ResidentPath"

$state = Join-Path $localAppData "YunPinIME\sync"
$bin = Join-Path $state "bin"
$destination = Join-Path $bin "yunpin-sync-agent.exe"
$residentDestination = Join-Path $bin "yunpin-sync-resident.exe"
$temporary = Join-Path $bin (".yunpin-sync-agent-" + [guid]::NewGuid().ToString("N") + ".tmp")
$residentTemporary = Join-Path $bin (".yunpin-sync-resident-" + [guid]::NewGuid().ToString("N") + ".tmp")
$backup = Join-Path $bin (".yunpin-sync-agent-" + [guid]::NewGuid().ToString("N") + ".rollback.exe")
$residentBackup = Join-Path $bin (".yunpin-sync-resident-" + [guid]::NewGuid().ToString("N") + ".rollback.exe")
$replaceBackup = Join-Path $bin (".yunpin-sync-agent-" + [guid]::NewGuid().ToString("N") + ".replace-backup.exe")
$residentReplaceBackup = Join-Path $bin (".yunpin-sync-resident-" + [guid]::NewGuid().ToString("N") + ".replace-backup.exe")
New-Item -ItemType Directory -Path $state, $bin -Force | Out-Null

$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
& "$env:SystemRoot\System32\icacls.exe" $state "/inheritance:r" "/grant:r" ("*" + $sid + ":(OI)(CI)F") "/grant:r" "*S-1-5-18:(OI)(CI)F" | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Failed to restrict the YunPin sync state ACL."
}

$taskName = "YunPinSyncAgent"
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$runName = "YunPinSyncAgent"
$previousRun = $null
$previousRunItem = Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
if ($null -ne $previousRunItem -and $previousRunItem.PSObject.Properties.Name -contains $runName) {
    $previousRun = $previousRunItem.$runName
}
$previousTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
$previousTaskXml = $null
$previousTaskWasRunning = $false
if ($null -ne $previousTask) {
    # Only a task this installer created may be replaced; anything else is
    # someone else's registration and stays untouched. Two shapes qualify:
    # the current one, and the one earlier versions registered before the
    # background loop moved to its own windowless binary. Without the legacy
    # shape, upgrading over an existing install would refuse to proceed and
    # require a manual uninstall first.
    $knownActions = @(
        @{ Execute = $residentDestination; Arguments = "--interval 1m" },
        @{ Execute = $destination; Arguments = "run --interval 1m" }
    )
    $recognized = $false
    if ($previousTask.Actions.Count -eq 1) {
        foreach ($known in $knownActions) {
            if ($previousTask.Actions[0].Execute -ceq $known.Execute -and
                $previousTask.Actions[0].Arguments -ceq $known.Arguments) {
                $recognized = $true
                break
            }
        }
    }
    if (-not $recognized) {
        throw "Refusing to replace a different YunPinSyncAgent scheduled task."
    }
    $previousTaskXml = Export-ScheduledTask -TaskName $taskName
    $previousTaskWasRunning = $previousTask.State.ToString() -ceq "Running"
}
$hadBinary = Test-Path -LiteralPath $destination -PathType Leaf
if ($hadBinary) {
    Copy-Item -LiteralPath $destination -Destination $backup
}
$hadResident = Test-Path -LiteralPath $residentDestination -PathType Leaf
if ($hadResident) {
    Copy-Item -LiteralPath $residentDestination -Destination $residentBackup
}

function Stop-InstalledAgent {
    foreach ($installed in @($destination, $residentDestination)) {
        Get-CimInstance Win32_Process -Filter ("Name = '" + [IO.Path]::GetFileName($installed) + "'") -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -eq $installed } |
            ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    }
}

try {
    Stop-InstalledAgent
    Copy-Item -LiteralPath $source -Destination $temporary
    if (Test-Path -LiteralPath $destination -PathType Leaf) {
        # Windows PowerShell 5.1 on supported hosts rejects a null backup path
        # for File.Replace.  Use a private same-directory metadata backup for
        # the atomic replacement; the separately verified $backup remains the
        # rollback source if a later registration step fails.
        [IO.File]::Replace($temporary, $destination, $replaceBackup, $true)
    } else {
        [IO.File]::Move($temporary, $destination)
    }
    $installedHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $destination).Hash.ToLowerInvariant()
    if ($installedHash -cne $expectedHash) {
        throw "Installed YunPin sync agent SHA-256 differs from the manifest-bound value."
    }
    Copy-Item -LiteralPath $residentSource -Destination $residentTemporary
    if (Test-Path -LiteralPath $residentDestination -PathType Leaf) {
        [IO.File]::Replace($residentTemporary, $residentDestination, $residentReplaceBackup, $true)
    } else {
        [IO.File]::Move($residentTemporary, $residentDestination)
    }
    $installedResidentHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $residentDestination).Hash.ToLowerInvariant()
    if ($installedResidentHash -cne $residentExpectedHash) {
        throw "Installed YunPin sync resident SHA-256 differs from the manifest-bound value."
    }
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    $action = New-ScheduledTaskAction -Execute $residentDestination -Argument "--interval 1m"
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity
    $principal = New-ScheduledTaskPrincipal -UserId $identity -LogonType Interactive -RunLevel Limited
    # The default Task Scheduler execution limit is 72 hours. A resident sync
    # agent must not silently stop after that limit or when a workstation
    # briefly reports battery/UPS power.
    $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
    Disable-ScheduledTask -TaskName $taskName | Out-Null
    Remove-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
    $registered = Get-ScheduledTask -TaskName $taskName -ErrorAction Stop
    if ($registered.State.ToString() -cne "Disabled" -or
        $registered.Actions.Count -ne 1 -or $registered.Actions[0].Execute -cne $residentDestination -or
        $registered.Actions[0].Arguments -cne "--interval 1m" -or
        [string]$registered.Settings.ExecutionTimeLimit -cne "PT0S" -or
        $registered.Settings.DisallowStartIfOnBatteries -or
        $registered.Settings.StopIfGoingOnBatteries) {
        throw "YunPin sync disabled background registration did not persist."
    }
    & $destination install-probe | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "YunPin sync background local readiness check failed."
    }
    $unexpected = Get-CimInstance Win32_Process -Filter "Name = 'yunpin-sync-resident.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -eq $residentDestination } |
        Select-Object -First 1
    if ($null -ne $unexpected) {
        throw "Disabled YunPin sync agent unexpectedly remained running."
    }
} catch {
    Stop-InstalledAgent
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    if ($hadBinary -and (Test-Path -LiteralPath $backup -PathType Leaf)) {
        Copy-Item -LiteralPath $backup -Destination $destination -Force
    } elseif (-not $hadBinary) {
        Remove-Item -LiteralPath $destination -Force -ErrorAction SilentlyContinue
    }
    if ($hadResident -and (Test-Path -LiteralPath $residentBackup -PathType Leaf)) {
        Copy-Item -LiteralPath $residentBackup -Destination $residentDestination -Force
    } elseif (-not $hadResident) {
        Remove-Item -LiteralPath $residentDestination -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $previousTaskXml) {
        Register-ScheduledTask -TaskName $taskName -Xml $previousTaskXml -Force | Out-Null
        if ($previousTaskWasRunning) {
            Start-ScheduledTask -TaskName $taskName
        }
    }
    if ($null -ne $previousRun) {
        New-Item -Path $runKey -Force | Out-Null
        New-ItemProperty -Path $runKey -Name $runName -PropertyType String -Value $previousRun -Force | Out-Null
    } else {
        Remove-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
    }
    throw
} finally {
    Remove-Item -LiteralPath $temporary, $backup, $replaceBackup,
        $residentTemporary, $residentBackup, $residentReplaceBackup -Force -ErrorAction SilentlyContinue
}
Write-Host "Installed and locally verified YunPinSyncAgent; its scheduled task remains disabled until Enable-SyncAgent.ps1 is run after setup."
