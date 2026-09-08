# SPDX-License-Identifier: Apache-2.0
param([string]$InstallerPath = (Join-Path $PSScriptRoot '../package/Install-Preview.ps1'))
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile([IO.Path]::GetFullPath($InstallerPath), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'Package transaction does not parse.' }
$names = @('Write-YunPinInstallJournal', 'Write-YunPinPriorState', 'Invoke-YunPinInstallTransaction', 'Assert-YunPinSafeTree',
    'Save-YunPinPathBackups', 'Restore-YunPinPathBackups', 'Assert-YunPinReplaceableDlls',
    'Get-YunPinRegistryTree', 'Set-YunPinRegistryTree', 'Save-YunPinRegistryBackups', 'Restore-YunPinRegistryBackups',
    'Restore-YunPinPackage')
$definitions = @($ast.FindAll({ param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $names -ccontains $node.Name
}, $true))
if ($definitions.Count -ne $names.Count) { throw 'Missing production package transaction functions.' }
foreach ($definition in $definitions) { Invoke-Expression $definition.Extent.Text }

# Native registry coverage is confined to this newly-created, exact HKCU key.
if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
    $testKey = 'Software\YunPinInstallerTests\' + [guid]::NewGuid().ToString('N')
    $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::CurrentUser, [Microsoft.Win32.RegistryView]::Registry64)
    try {
        $key = $base.CreateSubKey($testKey)
        $key.SetValue('Text', 'public-fixture', [Microsoft.Win32.RegistryValueKind]::String)
        $key.SetValue('Expand', '%PUBLIC%', [Microsoft.Win32.RegistryValueKind]::ExpandString)
        $key.SetValue('Bytes', [byte[]](1, 2, 3), [Microsoft.Win32.RegistryValueKind]::Binary)
        $key.SetValue('Number', [int]42, [Microsoft.Win32.RegistryValueKind]::DWord)
        $key.SetValue('Long', [long]4294967297, [Microsoft.Win32.RegistryValueKind]::QWord)
        $key.SetValue('List', [string[]]@('alpha', 'beta'), [Microsoft.Win32.RegistryValueKind]::MultiString)
        $child = $key.CreateSubKey('Child'); $child.SetValue('', 'default-value'); $child.Dispose(); $key.Dispose()
        $target = @([pscustomobject]@{ hive = 'CurrentUser'; view = 'Registry64'; path = $testKey })
        $savedRegistry = @(Save-YunPinRegistryBackups -Targets $target)
        $before = $savedRegistry | ConvertTo-Json -Depth 30 -Compress
        $savedRegistry = @([Management.Automation.PSSerializer]::Deserialize([Management.Automation.PSSerializer]::Serialize($savedRegistry, 30)))
        $base.DeleteSubKeyTree($testKey)
        Restore-YunPinRegistryBackups -Entries $savedRegistry
        $after = @(Save-YunPinRegistryBackups -Targets $target) | ConvertTo-Json -Depth 30 -Compress
        if ($before -cne $after) { throw 'Native temporary registry restoration changed values or kinds.' }
        Write-Host 'Native temporary HKCU subtree rollback passed, including all supported value kinds.'
    } finally { $base.DeleteSubKeyTree($testKey, $false); $base.Dispose() }
} else {
    Write-Host 'Native registry test SKIPPED outside Windows; transaction/file fixtures still execute.'
}

# Only native side effects are replaced. Production journal/file restore and
# complete rollback orchestration below are executed, not reimplemented.
function Stop-YunPinPackageWriters { param($Prior) $script:writerStopped = $true }
function Restore-YunPinPackageWriters { param($Prior) $script:writerRestored = $true }
function Assert-YunPinNoPendingDllRenames { }
function Invoke-CheckedExecutable { param($FilePath, $Arguments) $script:registrationCalls++ }
function Restore-YunPinRegistryBackups { param($Entries) $script:registryValue = $Entries[0] }
function New-ItemProperty { param($LiteralPath, $Name, $Value, $PropertyType, [switch]$Force) $script:runValue = $Value }
function Remove-ItemProperty { [CmdletBinding()]param($LiteralPath, $Name) $script:runValue = $null }

$root = Join-Path ([IO.Path]::GetTempPath()) ('yunpin-package-transaction-' + [guid]::NewGuid().ToString('N'))
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT -and $root.StartsWith('/var/')) { $root = '/private' + $root }
New-Item -ItemType Directory -Path $root | Out-Null
try {
    $dll = Join-Path $root 'synthetic-only.dll'
    [IO.File]::WriteAllText($dll, 'public synthetic DLL stand-in')
    Assert-YunPinReplaceableDlls -Paths @($dll)
    $locked = [IO.File]::Open($dll, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    try {
        $refused = $false
        try { Assert-YunPinReplaceableDlls -Paths @($dll) } catch { $refused = $true }
        if (-not $refused) { throw 'Locked DLL was not refused before mutation.' }
    } finally { $locked.Dispose() }
    if ([IO.File]::ReadAllText($dll) -cne 'public synthetic DLL stand-in') { throw 'DLL lock preflight changed a file.' }

    $phases = @('runtime', 'overlay', 'registration', 'deploy', 'agent', 'state', 'resume')
    foreach ($fresh in @($false, $true)) {
        foreach ($fault in $phases) {
            $fixtureRoot = Join-Path $root ([guid]::NewGuid().ToString('N'))
            $transactionRoot = Join-Path $fixtureRoot 'transaction'
            New-Item -ItemType Directory -Path $transactionRoot | Out-Null
            $fixtureFiles = @{}
            foreach ($phase in $phases) {
                $fixtureFiles[$phase] = Join-Path $fixtureRoot ($phase + '.txt')
                if (-not $fresh) { [IO.File]::WriteAllText($fixtureFiles[$phase], 'before-' + $phase) }
            }
            $entries = @(Save-YunPinPathBackups -Paths @($fixtureFiles.Values) -BackupRoot (Join-Path $transactionRoot 'backups'))
            $prior = [pscustomobject]@{
                pathBackups = $entries; systemDlls = @($dll); hadRuntime = (-not $fresh)
                registrations = @([pscustomobject]@{ regsvr = 'synthetic-only'; dll = $dll })
                registryBackups = @('before-registry'); runKey = 'synthetic-only'
                runValues = @([pscustomobject]@{ name = 'synthetic-only'; existed = $true; value = 'before-run'; kind = 'String' })
            }
            Write-YunPinPriorState -Path (Join-Path $transactionRoot 'prior.clixml') -Prior $prior
            Write-YunPinPriorState -Path (Join-Path $transactionRoot 'prior.clixml') -Prior $prior
            $script:writerStopped = $false; $script:writerRestored = $false
            $script:registryValue = 'before-registry'; $script:runValue = 'before-run'; $script:registrationCalls = 0
            $stages = @()
            foreach ($phase in $phases) {
                $stages += @{ Name = $phase; Action = {
                    [IO.File]::WriteAllText($fixtureFiles[$stage.Name], 'after-' + $stage.Name)
                    $script:registryValue = 'after-registry'; $script:runValue = 'after-run'
                    if ($stage.Name -ceq $fault) { throw 'injected-stage-failure' }
                } }
            }
            $journalPath = Join-Path $transactionRoot 'journal.json'
            $failed = $false
            try {
                Invoke-YunPinInstallTransaction -JournalPath $journalPath -Stages $stages -Rollback {
                    param($journal)
                    Restore-YunPinPackage -TransactionRoot $transactionRoot -Journal $journal
                }
            } catch { $failed = $true }
            if (-not $failed) { throw 'Fault injection did not fail.' }
            $receipt = Get-Content -LiteralPath $journalPath -Raw | ConvertFrom-Json
            if ($receipt.state -cne 'ROLLED_BACK' -or $receipt.stage -cne $fault) { throw 'Wrong durable failure stage or terminal result.' }
            foreach ($phase in $phases) {
                if ($fresh) {
                    if (Test-Path -LiteralPath $fixtureFiles[$phase]) { throw 'Rollback left a fresh-install target active.' }
                } elseif ([IO.File]::ReadAllText($fixtureFiles[$phase]) -cne ('before-' + $phase)) { throw 'Rollback did not restore the previous bytes.' }
            }
            if (-not $script:writerStopped -or -not $script:writerRestored -or $script:registryValue -cne 'before-registry' -or $script:runValue -cne 'before-run') {
                throw 'Package rollback did not restore its state/writer boundaries.'
            }
        }
    }
    $recoveryRoot = Join-Path $root 'recovery-required'
    New-Item -ItemType Directory -Path $recoveryRoot | Out-Null
    $journalPath = Join-Path $recoveryRoot 'journal.json'
    try { Invoke-YunPinInstallTransaction -JournalPath $journalPath -Stages @(@{ Name = 'runtime'; Action = { throw 'injected' } }) -Rollback { throw 'injected recovery failure' } } catch { }
    if ((Get-Content -LiteralPath $journalPath -Raw | ConvertFrom-Json).state -cne 'RECOVERY_REQUIRED') { throw 'Recovery failure was hidden.' }
    $script:retried = $false
    try { Invoke-YunPinInstallTransaction -JournalPath $journalPath -Stages @(@{ Name = 'runtime'; Action = { $script:retried = $true } }) -Rollback { } } catch { }
    if ($script:retried) { throw 'An unfinished transaction allowed another business attempt.' }
    Write-Host '14 fresh/upgrade stage faults, exact file/state rollback, locked-DLL refusal, and recovery-only no-rerun passed.'
} finally {
    Remove-Item -LiteralPath $root -Recurse -Force
}
