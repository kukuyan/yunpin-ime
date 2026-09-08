# SPDX-License-Identifier: Apache-2.0
param([string]$InstallerPath = (Join-Path $PSScriptRoot '../package/Install-Preview.ps1'))
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile([IO.Path]::GetFullPath($InstallerPath), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { throw 'Installer parse failed.' }
$names = @('Read-YunPinStrictUtf8File', 'Copy-OverlayWithBackup', 'Get-YunPinBooleanOptIn', 'Preserve-YunPinBooleanOptIns')
$definitions = @($ast.FindAll({ param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $names -ccontains $node.Name
}, $true))
if ($definitions.Count -ne $names.Count) { throw 'Missing production functions.' }
foreach ($definition in $definitions) { Invoke-Expression $definition.Extent.Text }

# Only public synthetic YAML, never the user's installed input method data.
$root = Join-Path ([IO.Path]::GetTempPath()) ('yunpin-overlay-state-' + [guid]::NewGuid().ToString('N'))
$source = Join-Path $root 'source'
$destination = Join-Path $root 'destination'
$backup = Join-Path $root 'backup'
New-Item -ItemType Directory -Path $source, $destination | Out-Null
$utf8 = New-Object Text.UTF8Encoding($false, $true)
$packaged = @'
patch:
  "yunpin/enabled": false
  "yunpin/session_learning": false
  "yunpin/short_input_guard": true
  "yunpin/long_correction_guard": true
  "yunpin/typo_correction": false
'@
$user = @'
patch:
  "yunpin/enabled": true
  "yunpin/session_learning": true
  "yunpin/short_input_guard": false
  "yunpin/long_correction_guard": false
  "yunpin/typo_correction": true
  "style/comment": "公开合成测试"
  "custom/unknown_setting": [1, 2, 3]
'@
try {
    $overlay = Join-Path $destination 'rime_ice.custom.yaml'
    [IO.File]::WriteAllText((Join-Path $source 'rime_ice.custom.yaml'), $packaged, $utf8)
    [IO.File]::WriteAllText($overlay, $user, $utf8)
    $hash = (Get-FileHash -LiteralPath $overlay -Algorithm SHA256).Hash
    Copy-OverlayWithBackup -SourceRoot $source -DestinationRoot $destination -BackupRoot $backup
    Preserve-YunPinBooleanOptIns -Path $overlay -PrivateCandidates $true -SessionLearning $true
    if ((Get-FileHash -LiteralPath $overlay -Algorithm SHA256).Hash -cne $hash) { throw 'Upgrade changed user-owned YAML.' }
    if ((Read-YunPinStrictUtf8File -Path (Join-Path $backup 'incoming-defaults/rime_ice.custom.yaml')) -cne $packaged) {
        throw 'Upgrade did not retain proposed defaults.'
    }
    for ($bits = 0; $bits -lt 32; $bits++) {
        foreach ($withBom in @($false, $true)) {
            $keys = @('enabled', 'session_learning', 'short_input_guard', 'long_correction_guard', 'typo_correction')
            $variant = $user
            for ($index = 0; $index -lt $keys.Count; $index++) {
                $value = ($bits -band (1 -shl $index)) -ne 0
                $variant = [regex]::Replace($variant, ('("yunpin/' + $keys[$index] + '": )(true|false)'), ('${1}' + $value.ToString().ToLowerInvariant()))
            }
            [IO.File]::WriteAllText($overlay, $variant, (New-Object Text.UTF8Encoding($withBom, $true)))
            $before = (Get-FileHash -LiteralPath $overlay -Algorithm SHA256).Hash
            Copy-OverlayWithBackup -SourceRoot $source -DestinationRoot $destination -BackupRoot $backup 6>$null
            Preserve-YunPinBooleanOptIns -Path $overlay -PrivateCandidates (($bits -band 1) -ne 0) -SessionLearning (($bits -band 2) -ne 0)
            if ((Get-FileHash -LiteralPath $overlay -Algorithm SHA256).Hash -cne $before) {
                throw "Upgrade changed a settings combination (bits=$bits, BOM=$withBom)."
            }
        }
    }
    Remove-Item -LiteralPath $overlay
    Copy-OverlayWithBackup -SourceRoot $source -DestinationRoot $destination -BackupRoot $backup
    if ((Read-YunPinStrictUtf8File -Path $overlay) -cne $packaged) { throw 'Fresh install did not retain disabled defaults.' }
    Write-Host 'All 64 YAML/encoding combinations preserve exact bytes; fresh install retains disabled defaults.'
} finally {
    Remove-Item -LiteralPath $root -Recurse -Force
}

$startup = @($ast.FindAll({ param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq 'Restore-YunPinServerStartup'
}, $true))
if ($startup.Count -ne 1) { throw 'Missing production startup preservation helper.' }
Invoke-Expression $startup[0].Extent.Text
# Native side effects are stubs, isolated from the file-fixture phase above.
function New-Item { param($Path, [switch]$Force) }
function New-ItemProperty { param($Path, $Name, $PropertyType, $Value, [switch]$Force) $script:runWrites++ }
function Start-Process { param($FilePath) $script:serverStarts++ }
foreach ($installed in @($false, $true)) {
    foreach ($running in @($false, $true)) {
        $script:runWrites = 0; $script:serverStarts = 0
        Restore-YunPinServerStartup -WasInstalled $installed -WasRunning $running -RunKey 'synthetic-only' -Server 'synthetic-only'
        if ($script:runWrites -ne [int](-not $installed) -or $script:serverStarts -ne [int](-not $installed -or $running)) {
            throw 'Upgrade changed existing server autostart or running state.'
        }
    }
}
Write-Host 'Four server startup combinations passed without registry/process operations.'
