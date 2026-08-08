# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$OutputRoot,
    [Parameter(Mandatory = $true)][string]$WeaselSource
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $false)][string[]]$ArgumentList = @()
    )
    & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed with exit code ${LASTEXITCODE}: $FilePath $($ArgumentList -join ' ')"
    }
}

function Reset-GeneratedDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$AllowedParent
    )
    $fullPath = [IO.Path]::GetFullPath($Path)
    $fullParent = [IO.Path]::GetFullPath($AllowedParent).TrimEnd(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    $prefix = $fullParent + [IO.Path]::DirectorySeparatorChar
    if (-not $fullPath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to reset path outside generated root: $fullPath"
    }
    if (Test-Path $fullPath) {
        Remove-Item -LiteralPath $fullPath -Recurse -Force
    }
    New-Item -ItemType Directory -Path $fullPath -Force | Out-Null
}

function Copy-TreeContent {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )
    New-Item -ItemType Directory -Path $Destination -Force | Out-Null
    Get-ChildItem -LiteralPath $Source -Force | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $Destination -Recurse -Force
    }
}

function Export-GitTree {
    param(
        [Parameter(Mandatory = $true)][string]$Checkout,
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][string]$ScratchRoot
    )
    Reset-GeneratedDirectory -Path $Destination -AllowedParent (Split-Path $Destination -Parent)
    $sourceMarker = Join-Path $Checkout ".yunpin-source-commit"
    if (-not (Test-Path (Join-Path $Checkout ".git")) -and (Test-Path $sourceMarker -PathType Leaf)) {
        Get-ChildItem -LiteralPath $Checkout -Force | Where-Object {
            $_.Name -ne ".yunpin-source-commit"
        } | ForEach-Object {
            Copy-Item -LiteralPath $_.FullName -Destination $Destination -Recurse -Force
        }
        return
    }
    $archive = Join-Path $ScratchRoot (([IO.Path]::GetFileName($Destination)) + "-" + [guid]::NewGuid().ToString("N") + ".tar")
    Invoke-Checked -FilePath "git" -ArgumentList @(
        "-C", $Checkout, "archive", "--format=tar", "--output=$archive", "HEAD"
    )
    Invoke-Checked -FilePath "tar.exe" -ArgumentList @("-xf", $archive, "-C", $Destination)
    Remove-Item -LiteralPath $archive -Force
}

function Export-GitSubtree {
    param(
        [Parameter(Mandatory = $true)][string]$Checkout,
        [Parameter(Mandatory = $true)][string]$Tree,
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][string]$ScratchRoot
    )
    Reset-GeneratedDirectory -Path $Destination -AllowedParent (Split-Path $Destination -Parent)
    $archive = Join-Path $ScratchRoot (([IO.Path]::GetFileName($Destination)) + "-" + [guid]::NewGuid().ToString("N") + ".tar")
    Invoke-Checked -FilePath "git" -ArgumentList @(
        "-C", $Checkout, "archive", "--format=tar", "--output=$archive", ("HEAD:" + $Tree)
    )
    Invoke-Checked -FilePath "tar.exe" -ArgumentList @("-xf", $archive, "-C", $Destination)
    Remove-Item -LiteralPath $archive -Force
}

function Write-ZipArchive {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )
    if (Test-Path $Destination) {
        Remove-Item -LiteralPath $Destination -Force
    }
    Invoke-Checked -FilePath "tar.exe" -ArgumentList @(
        "-a", "-c", "-f", $Destination, "-C", $Source, "."
    )
}

function Write-SourceCommitMarker {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Commit
    )
    [IO.File]::WriteAllText(
        (Join-Path $Path ".yunpin-source-commit"),
        ($Commit + "`n"),
        ([Text.UTF8Encoding]::new($false))
    )
}

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = [IO.Path]::GetFullPath((Join-Path $scriptRoot "..\..\.."))
$OutputRoot = [IO.Path]::GetFullPath($OutputRoot)
$WeaselSource = [IO.Path]::GetFullPath($WeaselSource)
$lockPath = Join-Path $repoRoot "platform\windows\dependencies.lock.json"
$lock = Get-Content -LiteralPath $lockPath -Raw | ConvertFrom-Json

$packageRoot = Join-Path $OutputRoot "package-staging"
$artifactsRoot = Join-Path $OutputRoot "artifacts"
$scratchRoot = Join-Path $OutputRoot "scratch"
New-Item -ItemType Directory -Path $packageRoot, $artifactsRoot, $scratchRoot -Force | Out-Null

$bundleRoot = Join-Path $packageRoot "YunPin-IME-Windows-development-preview"
Reset-GeneratedDirectory -Path $bundleRoot -AllowedParent $packageRoot
$runtimeRoot = Join-Path $bundleRoot "runtime"
$rimeDataRoot = Join-Path $bundleRoot "rime-data"
$licenseRoot = Join-Path $bundleRoot "licenses"
New-Item -ItemType Directory -Path $runtimeRoot, $rimeDataRoot, $licenseRoot -Force | Out-Null

foreach ($mapping in $lock.package.runtimeFiles.PSObject.Properties) {
    $source = Join-Path (Join-Path $WeaselSource "output") $mapping.Name
    $destination = Join-Path $runtimeRoot ([string]$mapping.Value)
    if (-not (Test-Path $source -PathType Leaf)) {
        throw "Required runtime file is missing: $source"
    }
    Copy-Item -LiteralPath $source -Destination $destination -Force
}

$systemData = Join-Path $WeaselSource "output\data"
if (Test-Path $systemData -PathType Container) {
    Copy-TreeContent -Source $systemData -Destination (Join-Path $runtimeRoot "data")
}

$rimeIceExport = Join-Path $scratchRoot "rime-ice-runtime-export"
Export-GitTree -Checkout (Join-Path $repoRoot "third_party\rime-ice") -Destination $rimeIceExport -ScratchRoot $scratchRoot
Get-ChildItem -LiteralPath $rimeIceExport -File | Where-Object {
    $_.Name -like "*.yaml" -or $_.Name -eq "custom_phrase.txt"
} | ForEach-Object {
    Copy-Item -LiteralPath $_.FullName -Destination $rimeDataRoot -Force
}
foreach ($directory in @("cn_dicts", "en_dicts", "lua", "opencc")) {
    Copy-TreeContent -Source (Join-Path $rimeIceExport $directory) -Destination (Join-Path $rimeDataRoot $directory)
}
Copy-Item -LiteralPath (Join-Path $repoRoot "platform\rime\weasel\default.custom.yaml") -Destination $rimeDataRoot -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "platform\rime\weasel\weasel.custom.yaml") -Destination $rimeDataRoot -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "platform\windows\rime\rime_ice.custom.yaml") -Destination $rimeDataRoot -Force
$privateExampleRoot = Join-Path $rimeDataRoot "yunpin"
New-Item -ItemType Directory -Path $privateExampleRoot -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot "platform\windows\rime\yunpin-private.tsv.example") -Destination (Join-Path $privateExampleRoot "private.tsv.example") -Force

Copy-TreeContent -Source (Join-Path $repoRoot "platform\windows\package") -Destination $bundleRoot
Copy-Item -LiteralPath (Join-Path $repoRoot "LICENSE") -Destination (Join-Path $licenseRoot "YunPin-Apache-2.0.txt") -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "NOTICE") -Destination (Join-Path $licenseRoot "YunPin-NOTICE.txt") -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "THIRD_PARTY_NOTICES.md") -Destination $licenseRoot -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "docs\LICENSE_MATRIX.md") -Destination $licenseRoot -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "third_party\weasel\LICENSE.txt") -Destination (Join-Path $licenseRoot "Weasel-GPL-3.0.txt") -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "third_party\librime\LICENSE") -Destination (Join-Path $licenseRoot "librime-BSD-3-Clause.txt") -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "third_party\rime-ice\LICENSE") -Destination (Join-Path $licenseRoot "Rime-Ice-GPL-3.0.txt") -Force
$boostLicense = Join-Path $OutputRoot "deps\boost_1_84_0\LICENSE_1_0.txt"
if (-not (Test-Path $boostLicense -PathType Leaf)) {
    throw "Boost license is missing from the verified source tree: $boostLicense"
}
Copy-Item -LiteralPath $boostLicense -Destination (Join-Path $licenseRoot "Boost-BSL-1.0.txt") -Force

foreach ($dependency in $lock.librime.dependencies.PSObject.Properties) {
    $dependencyRoot = Join-Path (Join-Path $repoRoot "third_party\librime") $dependency.Name
    $license = Get-ChildItem -LiteralPath $dependencyRoot -File -Recurse | Where-Object {
        $_.Name -match '^(LICENSE|COPYING)(\..*)?$'
    } | Sort-Object FullName | Select-Object -First 1
    if ($null -eq $license) {
        throw "No license file found for librime/$($dependency.Name)"
    }
    $safeName = $dependency.Name.Replace("/", "-").Replace("\", "-")
    Copy-Item -LiteralPath $license.FullName -Destination (Join-Path $licenseRoot ("librime-" + $safeName + "-" + $license.Name)) -Force
}

$repoCommit = ""
if (Test-Path (Join-Path $repoRoot ".git")) {
    $repoCommit = (& git -C $repoRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to resolve repository commit"
    }
} elseif (Test-Path (Join-Path $repoRoot "BUILD-SOURCE-METADATA.json") -PathType Leaf) {
    $existingSourceMetadata = Get-Content -LiteralPath (Join-Path $repoRoot "BUILD-SOURCE-METADATA.json") -Raw | ConvertFrom-Json
    $repoCommit = [string]$existingSourceMetadata.repositoryCommit
} else {
    throw "Unable to resolve repository commit or source-export metadata"
}
$metadata = [ordered]@{
    schemaVersion = 1
    product = "YunPin IME Windows development preview"
    generatedAtUtc = [DateTime]::UtcNow.ToString("o")
    repositoryCommit = $repoCommit
    signed = $false
    productionReady = $false
    architectures = @("x86-tsf", "x64-tsf", "x64-service")
    mergedPlugin = "librime-yunpin"
    privateCandidateSnapshotEnabled = $false
    upstreams = [ordered]@{
        weasel = $lock.weasel.commit
        librime = $lock.librime.commit
        rimeIce = $lock.rimeIce.commit
        boost = $lock.boost.sha256
    }
}
$metadata | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $bundleRoot "BUILD-METADATA.json") -Encoding UTF8

$manifestRows = @()
$bundlePrefix = $bundleRoot.TrimEnd("\") + "\"
Get-ChildItem -LiteralPath $bundleRoot -File -Recurse | Sort-Object FullName | ForEach-Object {
    $relative = $_.FullName.Substring($bundlePrefix.Length).Replace("\", "/")
    if ($relative -ne "MANIFEST.sha256") {
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant()
        $manifestRows += "$hash  $relative"
    }
}
[IO.File]::WriteAllLines((Join-Path $bundleRoot "MANIFEST.sha256"), $manifestRows, ([Text.UTF8Encoding]::new($false)))

& (Join-Path $scriptRoot "Test-Package.ps1") -BundleRoot $bundleRoot

$runtimeArchive = Join-Path $artifactsRoot "YunPin-IME-Windows-development-preview.zip"
Write-ZipArchive -Source $bundleRoot -Destination $runtimeArchive

$sourceRoot = Join-Path $packageRoot "YunPin-IME-Windows-development-preview-source"
Reset-GeneratedDirectory -Path $sourceRoot -AllowedParent $packageRoot
Export-GitTree -Checkout (Join-Path $repoRoot "third_party\weasel") -Destination (Join-Path $sourceRoot "third_party\weasel") -ScratchRoot $scratchRoot
Write-SourceCommitMarker -Path (Join-Path $sourceRoot "third_party\weasel") -Commit $lock.weasel.commit
Export-GitTree -Checkout (Join-Path $repoRoot "third_party\librime") -Destination (Join-Path $sourceRoot "third_party\librime") -ScratchRoot $scratchRoot
Write-SourceCommitMarker -Path (Join-Path $sourceRoot "third_party\librime") -Commit $lock.librime.commit
foreach ($dependency in $lock.librime.dependencies.PSObject.Properties) {
    $sourceDependency = Join-Path (Join-Path $sourceRoot "third_party\librime") $dependency.Name
    Export-GitTree -Checkout (Join-Path (Join-Path $repoRoot "third_party\librime") $dependency.Name) -Destination $sourceDependency -ScratchRoot $scratchRoot
    Write-SourceCommitMarker -Path $sourceDependency -Commit ([string]$dependency.Value)
}
Export-GitTree -Checkout (Join-Path $repoRoot "third_party\rime-ice") -Destination (Join-Path $sourceRoot "third_party\rime-ice") -ScratchRoot $scratchRoot
Write-SourceCommitMarker -Path (Join-Path $sourceRoot "third_party\rime-ice") -Commit $lock.rimeIce.commit
Export-GitSubtree -Checkout $repoRoot -Tree "librime-yunpin" -Destination (Join-Path $sourceRoot "librime-yunpin") -ScratchRoot $scratchRoot
$sourceEngine = Join-Path $sourceRoot "engine"
New-Item -ItemType Directory -Path $sourceEngine -Force | Out-Null
foreach ($directory in @("include", "src", "tests")) {
    Copy-TreeContent -Source (Join-Path (Join-Path $repoRoot "engine") $directory) -Destination (Join-Path $sourceEngine $directory)
}
foreach ($file in @(".gitignore", "CMakeLists.txt", "Makefile", "README.md")) {
    Copy-Item -LiteralPath (Join-Path (Join-Path $repoRoot "engine") $file) -Destination $sourceEngine -Force
}
Export-GitSubtree -Checkout $repoRoot -Tree "platform/windows" -Destination (Join-Path $sourceRoot "platform\windows") -ScratchRoot $scratchRoot
Export-GitSubtree -Checkout $repoRoot -Tree "platform/rime" -Destination (Join-Path $sourceRoot "platform\rime") -ScratchRoot $scratchRoot
Export-GitSubtree -Checkout $repoRoot -Tree "platform/patches/weasel" -Destination (Join-Path $sourceRoot "platform\patches\weasel") -ScratchRoot $scratchRoot
foreach ($file in @("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md")) {
    Copy-Item -LiteralPath (Join-Path $repoRoot $file) -Destination $sourceRoot -Force
}
New-Item -ItemType Directory -Path (Join-Path $sourceRoot "docs") -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot "docs\LICENSE_MATRIX.md") -Destination (Join-Path $sourceRoot "docs\LICENSE_MATRIX.md") -Force
$sourceBoost = Join-Path $OutputRoot "cache\boost_1_84_0.7z"
if (-not (Test-Path $sourceBoost -PathType Leaf)) {
    throw "Verified Boost source archive is missing: $sourceBoost"
}
New-Item -ItemType Directory -Path (Join-Path $sourceRoot "sources") -Force | Out-Null
Copy-Item -LiteralPath $sourceBoost -Destination (Join-Path $sourceRoot "sources\boost_1_84_0.7z") -Force
$sourceMetadata = [ordered]@{
    schemaVersion = 1
    repositoryCommit = $repoCommit
    buildEntryPoint = "platform/windows/scripts/Build-Preview.ps1"
    weaselBase = $lock.weasel.commit
    patches = $lock.weasel.patches
    librime = $lock.librime.commit
    librimeDependencies = $lock.librime.dependencies
    rimeIce = $lock.rimeIce.commit
    boostSourceSha256 = $lock.boost.sha256
}
$sourceMetadata | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $sourceRoot "BUILD-SOURCE-METADATA.json") -Encoding UTF8
$sourceReadme = @'
YunPin IME Windows development-preview corresponding source
============================================================

This archive contains the exact exported source trees and commit markers for
Weasel, librime, librime's nested dependencies, and Rime Ice, plus YunPin's
engine, merged plugin, Windows scripts, ordered GPL patches, licenses, and the
verified Boost source archive. It contains no private phrase data.

Verify SOURCE-MANIFEST.sha256, then run from a Visual Studio 2022 developer
PowerShell:

  .\platform\windows\scripts\Build-Preview.ps1

The build accepts these manifest-covered source exports without Git metadata;
each .yunpin-source-commit must match platform/windows/dependencies.lock.json.
'@
[IO.File]::WriteAllText((Join-Path $sourceRoot "README-SOURCE.txt"), $sourceReadme, ([Text.UTF8Encoding]::new($false)))
$sourceManifestRows = @()
$sourcePrefix = $sourceRoot.TrimEnd("\") + "\"
Get-ChildItem -LiteralPath $sourceRoot -File -Recurse | Sort-Object FullName | ForEach-Object {
    $relative = $_.FullName.Substring($sourcePrefix.Length).Replace("\", "/")
    if ($relative -ne "SOURCE-MANIFEST.sha256") {
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant()
        $sourceManifestRows += "$hash  $relative"
    }
}
[IO.File]::WriteAllLines((Join-Path $sourceRoot "SOURCE-MANIFEST.sha256"), $sourceManifestRows, ([Text.UTF8Encoding]::new($false)))

$sourceArchive = Join-Path $artifactsRoot "YunPin-IME-Windows-development-preview-source.zip"
Write-ZipArchive -Source $sourceRoot -Destination $sourceArchive
$runtimeHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $runtimeArchive).Hash.ToLowerInvariant()
$sourceHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourceArchive).Hash.ToLowerInvariant()
$artifactHashes = @(
    "$runtimeHash  $([IO.Path]::GetFileName($runtimeArchive))",
    "$sourceHash  $([IO.Path]::GetFileName($sourceArchive))"
)
[IO.File]::WriteAllLines((Join-Path $artifactsRoot "SHA256SUMS"), $artifactHashes, ([Text.UTF8Encoding]::new($false)))

Write-Host "Windows development preview package: $runtimeArchive"
Write-Host "Corresponding source package: $sourceArchive"
