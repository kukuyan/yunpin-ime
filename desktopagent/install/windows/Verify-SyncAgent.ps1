# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Verify-SyncAgent.ps1 must run as the signed-in Windows user."
}

$localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
if ([string]::IsNullOrWhiteSpace($localAppData)) { throw "Known Folder LOCALAPPDATA is unavailable." }
$state = Join-Path $localAppData "YunPinIME\sync"
$destination = Join-Path $state "bin\yunpin-sync-agent.exe"
$taskName = "YunPinSyncAgent"

if (-not (Test-Path -LiteralPath $destination -PathType Leaf)) { throw "Resident agent is absent." }
$registered = Get-ScheduledTask -TaskName $taskName -ErrorAction Stop
if ($registered.State.ToString() -cne "Disabled" -or
    $registered.Actions.Count -ne 1 -or $registered.Actions[0].Execute -cne $destination -or
    $registered.Actions[0].Arguments -cne "run --interval 1m") {
    throw "Disabled scheduled-task registration differs."
}
$process = Get-CimInstance Win32_Process -Filter "Name = 'yunpin-sync-agent.exe'" -ErrorAction Stop |
    Where-Object { $_.ExecutablePath -eq $destination } |
    Select-Object -First 1
if ($null -ne $process) { throw "Disabled YunPin sync process is unexpectedly running." }
& $destination install-probe | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Resident agent local readiness check failed." }
Write-Host "YunPin sync installation and disabled scheduled-task registration verified."
