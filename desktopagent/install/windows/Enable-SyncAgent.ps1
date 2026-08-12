# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Enable-SyncAgent.ps1 must run as the signed-in Windows user."
}

$localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
if ([string]::IsNullOrWhiteSpace($localAppData)) { throw "Known Folder LOCALAPPDATA is unavailable." }
$state = Join-Path $localAppData "YunPinIME\sync"
$destination = Join-Path $state "bin\yunpin-sync-agent.exe"
$taskName = "YunPinSyncAgent"

if (-not (Test-Path -LiteralPath $destination -PathType Leaf)) { throw "Resident agent is absent." }
$registered = Get-ScheduledTask -TaskName $taskName -ErrorAction Stop
if ($registered.Actions.Count -ne 1 -or $registered.Actions[0].Execute -cne $destination -or
    $registered.Actions[0].Arguments -cne "run --interval 1m") {
    throw "Scheduled-task registration differs."
}

# The local redacted gate requires finalized two-device trust, no pending
# protected setup journal, and complete private Rime bridge state. Suppress its
# complete stream so no identifier or state path can enter an installer log.
& $destination resident-ready *> $null
if ($LASTEXITCODE -ne 0) {
    throw "YunPin sync setup or pairing is incomplete; the resident task remains disabled."
}

try {
    Enable-ScheduledTask -TaskName $taskName | Out-Null
    Start-ScheduledTask -TaskName $taskName
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    do {
        Start-Sleep -Milliseconds 100
        $process = Get-CimInstance Win32_Process -Filter "Name = 'yunpin-sync-agent.exe'" -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -eq $destination } |
            Select-Object -First 1
    } while ($null -eq $process -and [DateTime]::UtcNow -lt $deadline)
    if ($null -eq $process) { throw "YunPin sync resident process did not start." }
} catch {
    Get-CimInstance Win32_Process -Filter "Name = 'yunpin-sync-agent.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -eq $destination } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Disable-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue | Out-Null
    throw
}
Write-Host "YunPin sync scheduled task enabled and started after local setup validation."
