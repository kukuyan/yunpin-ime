# SPDX-License-Identifier: Apache-2.0
param([string]$InstallerPath = (Join-Path $PSScriptRoot 'Install-SyncAgent.ps1'))
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile([IO.Path]::GetFullPath($InstallerPath), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { throw 'Sync installer parse failed.' }
$definitions = @($ast.FindAll({ param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq 'Restore-YunPinExistingTaskState'
}, $true))
if ($definitions.Count -ne 1) { throw 'Missing production state restoration helper.' }
Invoke-Expression $definitions[0].Extent.Text

# Every task operation is a fixture stub. The installer body is not executed.
function Enable-ScheduledTask { param($TaskName) $script:enabled++; $script:observed = 'Ready' }
function Start-ScheduledTask { param($TaskName) $script:started++; $script:observed = 'Running' }
function Get-ScheduledTask { [CmdletBinding()]param($TaskName) [pscustomobject]@{ State = $script:observed } }
foreach ($wasEnabled in @($false, $true)) {
    foreach ($wasRunning in @($false, $true)) {
        foreach ($leaveDisabled in @($false, $true)) {
            $script:enabled = 0; $script:started = 0; $script:observed = 'Disabled'
            Restore-YunPinExistingTaskState -TaskName 'synthetic-task-only' -WasEnabled $wasEnabled `
                -WasRunning $wasRunning -LeaveDisabled:$leaveDisabled
            $expectedEnabled = [int]($wasEnabled -and -not $leaveDisabled)
            $expectedStarted = [int]($wasEnabled -and $wasRunning -and -not $leaveDisabled)
            if ($script:enabled -ne $expectedEnabled -or $script:started -ne $expectedStarted) {
                throw 'Task state restoration changed the original opt-in or running state.'
            }
        }
    }
}
Write-Host 'Eight task state/explicit staging combinations passed without native task operations.'
