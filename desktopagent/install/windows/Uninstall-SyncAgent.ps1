# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Uninstall-SyncAgent.ps1 must run as the signed-in Windows user."
}

$localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
if ([string]::IsNullOrWhiteSpace($localAppData)) { throw "Known Folder LOCALAPPDATA is unavailable." }
$state = Join-Path $localAppData "YunPinIME\sync"
$destination = Join-Path $state "bin\yunpin-sync-agent.exe"
# The scheduled task runs the windowless resident; the interactive agent
# stays installed for status, configuration and pairing.
$residentDestination = Join-Path $state "bin\yunpin-sync-resident.exe"
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$runName = "YunPinSyncAgent"
$taskName = "YunPinSyncAgent"
$registered = $null
$runItem = Get-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
if ($null -ne $runItem -and $runItem.PSObject.Properties.Name -contains $runName) {
    $registered = $runItem.$runName
}
if ($null -ne $registered -and -not $registered.StartsWith(('"' + $destination + '" '), [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to remove a different YunPinSyncAgent registration."
}
$task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($null -ne $task -and ($task.Actions.Count -ne 1 -or $task.Actions[0].Execute -cne $destination)) {
    throw "Refusing to remove a different YunPinSyncAgent scheduled task."
}
Get-CimInstance Win32_Process -Filter "Name = 'yunpin-sync-resident.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -eq $residentDestination } |
    ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Remove-ItemProperty -Path $runKey -Name $runName -ErrorAction SilentlyContinue
if ($null -ne $task) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}

$installedBinaries = @($destination, $residentDestination) | Where-Object {
    Test-Path -LiteralPath $_ -PathType Leaf
}
if ($installedBinaries.Count -gt 0) {
    $retired = Join-Path $state ("retired\" + [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ"))
    New-Item -ItemType Directory -Path $retired -Force | Out-Null
    foreach ($binary in $installedBinaries) {
        Move-Item -LiteralPath $binary -Destination (Join-Path $retired ([IO.Path]::GetFileName($binary)))
    }
    Write-Host "Background agent retired to $retired."
}
Write-Host "Endpoint, encrypted DB, DPAPI records, and dictionaries were retained."
