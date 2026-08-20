param(
    [string]$InstallerPath = (Join-Path $PSScriptRoot '..\package\Install-Preview.ps1')
)

$ErrorActionPreference = 'Stop'
$InstallerPath = [IO.Path]::GetFullPath($InstallerPath)
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile(
    $InstallerPath,
    [ref]$tokens,
    [ref]$parseErrors
)
if ($parseErrors.Count -ne 0) {
    throw 'Install-Preview.ps1 does not parse as PowerShell.'
}

$requiredFunctions = @(
    'Read-YunPinStrictUtf8File',
    'Get-YunPinBooleanOptIn',
    'Preserve-YunPinBooleanOptIns'
)
$definitions = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
        $requiredFunctions -ccontains $node.Name
}, $true))
if ($definitions.Count -ne $requiredFunctions.Count) {
    throw 'Installer UTF-8 helper functions are missing or duplicated.'
}
foreach ($definition in $definitions) {
    Invoke-Expression $definition.Extent.Text
}

$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ('yunpin-installer-utf8-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporaryRoot -ErrorAction Stop | Out-Null
try {
    $overlay = Join-Path $temporaryRoot 'rime_ice.custom.yaml'
    $fixture = @'
patch:
  "engine/filters/@before 0": yunpin_filter@yunpin
  "switches/@after last":
    name: yunpin_show_candidate_pinyin
    states: [拼音关, 拼音开]
  "corrector": "［{comment}］"
  "yunpin/enabled": false
  "yunpin/session_learning": false
'@
    $strictUtf8 = New-Object Text.UTF8Encoding($false, $true)
    [IO.File]::WriteAllText($overlay, $fixture, $strictUtf8)
    if (Get-YunPinBooleanOptIn -Path $overlay -Name 'yunpin/enabled') {
        throw 'Disabled private candidates were detected as enabled.'
    }

    Preserve-YunPinBooleanOptIns -Path $overlay -PrivateCandidates $true -SessionLearning $true
    $actual = Read-YunPinStrictUtf8File -Path $overlay
    $expected = $fixture.Replace('"yunpin/enabled": false', '"yunpin/enabled": true').Replace(
        '"yunpin/session_learning": false',
        '"yunpin/session_learning": true'
    )
    if ($actual -cne $expected -or
        -not $actual.Contains('states: [拼音关, 拼音开]') -or
        -not $actual.Contains('"corrector": "［{comment}］"') -or
        $actual.Contains([char]0xfffd)) {
        throw 'Installer opt-in preservation changed non-ASCII overlay content.'
    }
    if (-not (Get-YunPinBooleanOptIn -Path $overlay -Name 'yunpin/enabled') -or
        -not (Get-YunPinBooleanOptIn -Path $overlay -Name 'yunpin/session_learning')) {
        throw 'Installer opt-in preservation did not retain both explicit choices.'
    }
    $bytes = [IO.File]::ReadAllBytes($overlay)
    if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xef -and $bytes[1] -eq 0xbb -and $bytes[2] -eq 0xbf) {
        throw 'Installer unexpectedly added a UTF-8 BOM.'
    }

    $invalid = Join-Path $temporaryRoot 'invalid-utf8.yaml'
    [IO.File]::WriteAllBytes($invalid, [byte[]](0xc3, 0x28))
    $invalidRejected = $false
    try {
        [void](Read-YunPinStrictUtf8File -Path $invalid)
    } catch {
        $invalidRejected = $true
    }
    if (-not $invalidRejected) {
        throw 'Installer accepted malformed UTF-8.'
    }

    $replacement = Join-Path $temporaryRoot 'replacement-character.yaml'
    [IO.File]::WriteAllText($replacement, ('patch: ' + [char]0xfffd), $strictUtf8)
    $replacementRejected = $false
    try {
        [void](Read-YunPinStrictUtf8File -Path $replacement)
    } catch {
        $replacementRejected = $true
    }
    if (-not $replacementRejected) {
        throw 'Installer accepted an already-corrupted replacement character.'
    }

    Write-Host 'Windows installer UTF-8 opt-in preservation verified.'
} finally {
    Remove-Item -LiteralPath $temporaryRoot -Recurse -Force -ErrorAction SilentlyContinue
}
