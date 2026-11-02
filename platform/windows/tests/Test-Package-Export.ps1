# SPDX-License-Identifier: GPL-3.0-only
param(
    [string]$PackageScript = (Join-Path $PSScriptRoot '..\scripts\Package-Preview.ps1')
)

$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object Text.UTF8Encoding($false)
Add-Type -AssemblyName System.IO.Compression.FileSystem
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile(
    [IO.Path]::GetFullPath($PackageScript), [ref]$tokens, [ref]$errors
)
if ($errors.Count) { throw 'Package script did not parse.' }
$required = @('Invoke-Checked', 'Reset-GeneratedDirectory', 'Copy-TreeContent', 'Export-GitTree', 'Export-GitSubtree')
$definitions = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
        $required -ccontains $node.Name
}, $true))
if ($definitions.Count -ne $required.Count) { throw 'Missing package export functions.' }
foreach ($definition in $definitions) { Invoke-Expression $definition.Extent.Text }

$root = Join-Path ([IO.Path]::GetTempPath()) ('yunpin-package-export-' + [guid]::NewGuid().ToString('N'))
$checkout = Join-Path $root 'checkout'
$scratch = Join-Path $root 'scratch'
$utf8 = New-Object Text.UTF8Encoding($false, $true)
# Include a supplementary-plane character that cannot be represented by the
# Windows runner's legacy code page, as well as Chinese and spaces.
$unicodeName = ([char]0x6269).ToString() + [char]0x5c55 + '-' + [char]::ConvertFromUtf32(0x20bb7) + ' file.txt'
$nested = 'nested/source'
$source = Join-Path $checkout $nested
New-Item -ItemType Directory -Path $source, $scratch -Force | Out-Null
try {
    [IO.File]::WriteAllText((Join-Path $checkout '.gitattributes'), "*.patch text eol=lf`n*.ps1 text eol=crlf`nignored.txt export-ignore`n", $utf8)
    [IO.File]::WriteAllText((Join-Path $source '.hidden'), 'hidden', $utf8)
    [IO.File]::WriteAllText((Join-Path $source $unicodeName), 'committed Unicode data', $utf8)
    [IO.File]::WriteAllText((Join-Path $source 'change.patch'), "line one`nline two`n", $utf8)
    [IO.File]::WriteAllText((Join-Path $source 'script.ps1'), "line one`nline two`n", $utf8)
    [IO.File]::WriteAllText((Join-Path $checkout 'ignored.txt'), 'excluded', $utf8)
    Invoke-Checked git @('-C', $checkout, 'init', '--quiet')
    Invoke-Checked git @('-C', $checkout, 'config', 'core.autocrlf', 'true')
    Invoke-Checked git @('-C', $checkout, 'add', '.')
    Invoke-Checked git @('-C', $checkout, '-c', 'user.name=YunPin Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '--quiet', '-m', 'Synthetic package export fixture')
    # A package must export HEAD, never these modified/untracked bytes.
    [IO.File]::WriteAllText((Join-Path $source $unicodeName), 'uncommitted change', $utf8)
    [IO.File]::WriteAllText((Join-Path $source 'untracked.txt'), 'not committed', $utf8)
    $whole = Join-Path $root 'whole'
    $subtree = Join-Path $root 'subtree'
    Export-GitTree -Checkout $checkout -Destination $whole -ScratchRoot $scratch
    Export-GitSubtree -Checkout $checkout -Tree $nested -Destination $subtree -ScratchRoot $scratch
    foreach ($export in @((Join-Path $whole $nested), $subtree)) {
        if ([IO.File]::ReadAllText((Join-Path $export $unicodeName), $utf8) -cne 'committed Unicode data') { throw 'Unicode path/content was not preserved.' }
        if ([IO.File]::ReadAllText((Join-Path $export 'change.patch'), $utf8) -cne "line one`nline two`n") { throw 'Root LF attributes were lost.' }
        if ([IO.File]::ReadAllText((Join-Path $export 'script.ps1'), $utf8) -cne "line one`r`nline two`r`n") { throw 'Root CRLF attributes were lost.' }
        if (-not (Test-Path -LiteralPath (Join-Path $export '.hidden') -PathType Leaf)) { throw 'Dotfile was omitted.' }
        if (Test-Path -LiteralPath (Join-Path $export 'untracked.txt')) { throw 'Untracked data leaked into source export.' }
    }
    if (Test-Path -LiteralPath (Join-Path $whole 'ignored.txt')) { throw 'export-ignore was not honored.' }
    if (@(Get-ChildItem -LiteralPath $scratch -Force).Count) { throw 'Successful exports left scratch files.' }
    Write-Output 'PACKAGE_EXPORT_OK: Unicode, HEAD-only content, root attributes, dotfiles, export-ignore'
} finally {
    Remove-Item -LiteralPath $root -Recurse -Force
}
