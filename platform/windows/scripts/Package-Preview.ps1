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
    Invoke-Checked -FilePath "tar.exe" -ArgumentList @(
        "-xf", $archive, "-C", $Destination,
        "--options", "hdrcharset=UTF-8"
    )
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
    $treeComponents = @($Tree -split '[/\\]' | Where-Object { $_ -ne "" })
    if ($treeComponents.Count -eq 0 -or
        @($treeComponents | Where-Object { $_ -eq "." -or $_ -eq ".." }).Count -ne 0) {
        throw "Refusing to export an invalid Git subtree: $Tree"
    }
    $sourceMetadata = Join-Path $Checkout "BUILD-SOURCE-METADATA.json"
    if (-not (Test-Path (Join-Path $Checkout ".git")) -and
        (Test-Path $sourceMetadata -PathType Leaf)) {
        $sourceTree = Join-Path $Checkout $Tree
        if (-not (Test-Path -LiteralPath $sourceTree -PathType Container)) {
            throw "Source export is missing required subtree: $Tree"
        }
        Copy-TreeContent -Source $sourceTree -Destination $Destination
        return
    }
    $archive = Join-Path $ScratchRoot (([IO.Path]::GetFileName($Destination)) + "-" + [guid]::NewGuid().ToString("N") + ".tar")
    # Archive from the repository root so the root .gitattributes remains in
    # scope. Archiving HEAD:$Tree drops those attributes; with core.autocrlf on
    # Windows, that silently rewrites LF-locked patch files to CRLF and makes
    # the corresponding-source archive fail its own dependency hash gate.
    Invoke-Checked -FilePath "git" -ArgumentList @(
        "-C", $Checkout, "archive", "--format=tar", "--output=$archive", "HEAD", "--", $Tree
    )
    Invoke-Checked -FilePath "tar.exe" -ArgumentList @(
        "-xf", $archive, "-C", $Destination,
        ("--strip-components=" + $treeComponents.Count),
        "--options", "hdrcharset=UTF-8"
    )
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
    [IO.Compression.ZipFile]::CreateFromDirectory(
        $Source,
        $Destination,
        [IO.Compression.CompressionLevel]::Optimal,
        $false,
        [Text.UTF8Encoding]::new($false, $true)
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

function Assert-LockedFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Sha256,
        [Parameter(Mandatory = $true)][long]$Size,
        [Parameter(Mandatory = $true)][string]$Label
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Label is missing or not a regular file: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label must not be a reparse point: $Path"
    }
    if ($item.Length -ne $Size) {
        throw "$Label size mismatch: expected $Size, observed $($item.Length)"
    }
    $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
    if ($observed -cne $Sha256.ToLowerInvariant()) {
        throw "$Label SHA-256 mismatch"
    }
}

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = [IO.Path]::GetFullPath((Join-Path $scriptRoot "..\..\.."))
$OutputRoot = [IO.Path]::GetFullPath($OutputRoot)
$WeaselSource = [IO.Path]::GetFullPath($WeaselSource)
$lockPath = Join-Path $repoRoot "platform\windows\dependencies.lock.json"
$lock = Get-Content -LiteralPath $lockPath -Raw | ConvertFrom-Json

if (Test-Path (Join-Path $repoRoot ".git")) {
    $repositoryState = @(& git -C $repoRoot status --porcelain --untracked-files=normal)
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to inspect repository state before packaging"
    }
    if ($repositoryState.Count -ne 0) {
        throw "Packaging requires a clean YunPin repository so binaries and corresponding source use the same commit"
    }
}

$packageRoot = Join-Path $OutputRoot "package-staging"
$artifactsRoot = Join-Path $OutputRoot "artifacts"
$scratchRoot = Join-Path $OutputRoot "scratch"
New-Item -ItemType Directory -Path $packageRoot, $artifactsRoot, $scratchRoot -Force | Out-Null

$bundleRoot = Join-Path $packageRoot "YunPin-IME-Windows-development-preview"
Reset-GeneratedDirectory -Path $bundleRoot -AllowedParent $packageRoot
$runtimeRoot = Join-Path $bundleRoot "runtime"
$rimeDataRoot = Join-Path $bundleRoot "rime-data"
$licenseRoot = Join-Path $bundleRoot "licenses"
$syncAgentRoot = Join-Path $bundleRoot "sync-agent"
New-Item -ItemType Directory -Path $runtimeRoot, $rimeDataRoot, $licenseRoot, $syncAgentRoot -Force | Out-Null

foreach ($mapping in $lock.package.runtimeFiles.PSObject.Properties) {
    $source = Join-Path (Join-Path $WeaselSource "output") $mapping.Name
    $destination = Join-Path $runtimeRoot ([string]$mapping.Value)
    if (-not (Test-Path $source -PathType Leaf)) {
        throw "Required runtime file is missing: $source"
    }
    Copy-Item -LiteralPath $source -Destination $destination -Force
}

$publicSyncAgent = Join-Path $OutputRoot "desktopagent\public\yunpin-sync-agent.exe"
$publicSyncResident = Join-Path $OutputRoot "desktopagent\public\yunpin-sync-resident.exe"
$publicSettings = Join-Path $OutputRoot "desktopagent\public\yunpin-settings.exe"
$publicReplayLab = Join-Path $OutputRoot "desktopagent\public\yunpin-replay-lab.exe"
if (-not (Test-Path $publicSyncAgent -PathType Leaf)) {
    throw "Public default-tag sync agent is missing: $publicSyncAgent"
}
Copy-Item -LiteralPath $publicSyncAgent -Destination (Join-Path $syncAgentRoot "yunpin-sync-agent.exe") -Force
if (-not (Test-Path -LiteralPath $publicSyncResident -PathType Leaf)) {
    throw "Windowless sync resident is missing; run Build-SyncAgents.ps1 first"
}
Copy-Item -LiteralPath $publicSyncResident -Destination (Join-Path $syncAgentRoot "yunpin-sync-resident.exe") -Force
if (-not (Test-Path -LiteralPath $publicSettings -PathType Leaf)) {
    throw "Windowless settings launcher is missing; run Build-SyncAgents.ps1 first"
}
Copy-Item -LiteralPath $publicSettings -Destination (Join-Path $syncAgentRoot "yunpin-settings.exe") -Force
if (-not (Test-Path -LiteralPath $publicReplayLab -PathType Leaf)) {
    throw "Replay Lab CLI is missing; run Build-SyncAgents.ps1 first"
}
Copy-Item -LiteralPath $publicReplayLab -Destination (Join-Path $syncAgentRoot "yunpin-replay-lab.exe") -Force
$syncAgentLicenses = Join-Path $OutputRoot "desktopagent\licenses"
if (-not (Test-Path (Join-Path $syncAgentLicenses "LICENSES.json") -PathType Leaf)) {
    throw "Public sync-agent license-text bundle is missing"
}
Copy-TreeContent -Source $syncAgentLicenses -Destination (Join-Path $licenseRoot "YunPin-Sync-Agent-Go")
$replayLicenses = Join-Path $OutputRoot "replaylab\licenses"
if (-not (Test-Path (Join-Path $replayLicenses "LICENSES.json") -PathType Leaf)) {
    throw "Replay Lab license-text bundle is missing"
}
Copy-TreeContent -Source $replayLicenses -Destination (Join-Path $licenseRoot "YunPin-Replay-Lab-Go")
foreach ($supportScript in @(
    "Install-SyncAgent.ps1", "Verify-SyncAgent.ps1",
    "Enable-SyncAgent.ps1", "Uninstall-SyncAgent.ps1"
)) {
    Copy-Item -LiteralPath (Join-Path $repoRoot ("desktopagent\install\windows\" + $supportScript)) `
        -Destination (Join-Path $syncAgentRoot $supportScript) -Force
}
Copy-Item -LiteralPath (Join-Path $repoRoot "desktopagent\install\README.md") `
    -Destination (Join-Path $syncAgentRoot "README.md") -Force

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
$grammarModelCache = Join-Path $OutputRoot ("cache\" + [string]$lock.grammarModel.filename)
Assert-LockedFile -Path $grammarModelCache -Sha256 $lock.grammarModel.sha256 `
    -Size $lock.grammarModel.size -Label "Verified grammar model cache"
$packagedGrammarModel = Join-Path $rimeDataRoot ([string]$lock.grammarModel.filename)
Copy-Item -LiteralPath $grammarModelCache -Destination $packagedGrammarModel -Force
Assert-LockedFile -Path $packagedGrammarModel -Sha256 $lock.grammarModel.sha256 `
    -Size $lock.grammarModel.size -Label "Packaged grammar model"
$packagedGrammarModels = @(Get-ChildItem -LiteralPath $rimeDataRoot -File -Recurse -Filter "*.gram")
if ($packagedGrammarModels.Count -ne 1 -or
    $packagedGrammarModels[0].FullName -cne $packagedGrammarModel) {
    throw "Windows runtime must contain exactly one locked grammar model"
}
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
$octagramLicense = Join-Path $WeaselSource "librime\plugins\octagram\LICENSE"
if (-not (Test-Path $octagramLicense -PathType Leaf)) {
    throw "Verified librime-octagram license is missing from the staged source"
}
$octagramLicenseHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $octagramLicense).Hash.ToLowerInvariant()
if ($octagramLicenseHash -ne ([string]$lock.librimeOctagram.licenseSha256).ToLowerInvariant()) {
    throw "Staged librime-octagram license does not match the dependency lock"
}
Copy-Item -LiteralPath $octagramLicense `
    -Destination (Join-Path $licenseRoot "librime-octagram-BSD-3-Clause.txt") -Force
$grammarLicenseCache = Join-Path $OutputRoot ("cache\" + [string]$lock.grammarModel.licenseFilename)
Assert-LockedFile -Path $grammarLicenseCache -Sha256 $lock.grammarModel.licenseSha256 `
    -Size $lock.grammarModel.licenseSize -Label "Verified grammar model license cache"
$packagedGrammarLicense = Join-Path $licenseRoot `
    ([string]$lock.grammarModel.licenseFilename)
Copy-Item -LiteralPath $grammarLicenseCache -Destination $packagedGrammarLicense -Force
Assert-LockedFile -Path $packagedGrammarLicense -Sha256 $lock.grammarModel.licenseSha256 `
    -Size $lock.grammarModel.licenseSize -Label "Packaged grammar model license"
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

# Exercise the exact packaged Rime Ice data/model and exact runtime DLL in two
# fresh processes. The baseline differs only by removing grammar settings and
# disabling contextual suggestions. Both runs enumerate the same frozen
# 20-case matrix under a hard 20 ms final-key P95 gate.
$qualityProbe = Join-Path $scratchRoot `
    "rime-grammar-quality-probe-x64\Release\yunpin-rime-grammar-quality-probe.exe"
if (-not (Test-Path -LiteralPath $qualityProbe -PathType Leaf)) {
    throw "Built Rime grammar quality probe is missing: $qualityProbe"
}
$modelQualityUser = Join-Path $scratchRoot "rime-grammar-model-user"
$baselineQualityUser = Join-Path $scratchRoot "rime-no-grammar-baseline-user"
foreach ($qualityUser in @($modelQualityUser, $baselineQualityUser)) {
    Reset-GeneratedDirectory -Path $qualityUser -AllowedParent $scratchRoot
    New-Item -ItemType Directory -Path (Join-Path $qualityUser "lua") -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $qualityUser "yunpin") -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $rimeDataRoot "lua\lunar.db") `
        -Destination (Join-Path $qualityUser "lua\lunar.db") -Force
    Copy-Item -LiteralPath (Join-Path $repoRoot "platform\windows\tests\fixtures\synthetic-public-ranking.tsv") `
        -Destination (Join-Path $qualityUser "yunpin\private.tsv") -Force
}
$qualityOverlaySource = Join-Path $rimeDataRoot "rime_ice.custom.yaml"
$qualityOverlayText = [IO.File]::ReadAllText($qualityOverlaySource)
$disabledPrivateGate = '"yunpin/enabled": false'
if ([regex]::Matches(
        $qualityOverlayText, [regex]::Escape($disabledPrivateGate)).Count -ne 1) {
    throw "Packaged Windows overlay does not contain one disabled private-fixture gate"
}
$qualityOverlayText = $qualityOverlayText.Replace(
    $disabledPrivateGate, '"yunpin/enabled": true')
[IO.File]::WriteAllText(
    (Join-Path $modelQualityUser "rime_ice.custom.yaml"),
    $qualityOverlayText,
    (New-Object Text.UTF8Encoding($false)))
$grammarOverlayPattern = '(?m)^[ \t]+"grammar\/[^"\r\n]+":[^\r\n]*(?:\r?\n|$)'
if ([regex]::Matches($qualityOverlayText, $grammarOverlayPattern).Count -ne 7) {
    throw "Packaged Windows overlay grammar key count changed"
}
$contextualEnabled = '  "translator/contextual_suggestions": true'
if ([regex]::Matches(
        $qualityOverlayText, [regex]::Escape($contextualEnabled)).Count -ne 1) {
    throw "Packaged Windows overlay contextual setting changed"
}
$baselineOverlayText = [regex]::Replace(
    $qualityOverlayText, $grammarOverlayPattern, "")
$baselineOverlayText = $baselineOverlayText.Replace(
    $contextualEnabled, '  "translator/contextual_suggestions": false')
if ($baselineOverlayText -match '"grammar/' -or
    [regex]::Matches(
        $baselineOverlayText,
        [regex]::Escape('"translator/contextual_suggestions": false')
    ).Count -ne 1) {
    throw "Failed to create exact Windows no-grammar baseline overlay"
}
[IO.File]::WriteAllText(
    (Join-Path $baselineQualityUser "rime_ice.custom.yaml"),
    $baselineOverlayText,
    (New-Object Text.UTF8Encoding($false)))

$deploymentOutputs = @{}
$deploymentExitCodes = @{}
$qualityOutputs = @{}
$qualityExitCodes = @{}
$privateOffOutput = @()
$privateOffExitCode = -1
$previousPath = $env:PATH
try {
    $env:PATH = $runtimeRoot + ";" + $previousPath
    foreach ($mode in @("baseline", "model")) {
        $qualityUser = if ($mode -eq "model") {
            $modelQualityUser
        } else {
            $baselineQualityUser
        }
        $phase = "prepare-$mode"
        $deploymentOutputs[$mode] = @(
            & $qualityProbe $rimeDataRoot $qualityUser `
                (Join-Path $runtimeRoot "rime.dll") $phase 2>&1
        )
        $deploymentExitCodes[$mode] = $LASTEXITCODE
    }

    # Measurement is a fresh process over already deployed user data. It must
    # not invoke maintenance, so compilation allocations cannot contaminate
    # resident-runtime RSS or cold-stage latency.
    foreach ($mode in @("baseline", "model")) {
        $qualityUser = if ($mode -eq "model") {
            $modelQualityUser
        } else {
            $baselineQualityUser
        }
        $qualityOutputs[$mode] = @(
            & $qualityProbe $rimeDataRoot $qualityUser `
                (Join-Path $runtimeRoot "rime.dll") $mode 2>&1
        )
        $qualityExitCodes[$mode] = $LASTEXITCODE
    }

    # Prove the private result was caused by the reviewed synthetic fixture,
    # not by the public dictionary. Remove only that exact regular file, then
    # enumerate a fresh process's complete visible candidate page.
    $privateFixture = Join-Path $modelQualityUser "yunpin\private.tsv"
    $expectedPrivateFixture = [IO.Path]::GetFullPath(
        (Join-Path $modelQualityUser "yunpin\private.tsv"))
    if (-not [string]::Equals(
            [IO.Path]::GetFullPath($privateFixture),
            $expectedPrivateFixture,
            [StringComparison]::OrdinalIgnoreCase) -or
        -not (Test-Path -LiteralPath $privateFixture -PathType Leaf) -or
        ((Get-Item -LiteralPath $privateFixture -Force).Attributes -band
            [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Synthetic private fixture is missing or unsafe before counterfactual"
    }
    Remove-Item -LiteralPath $privateFixture -Force
    $privateOffOutput = @(
        & $qualityProbe $rimeDataRoot $modelQualityUser `
            (Join-Path $runtimeRoot "rime.dll") "private-off" 2>&1
    )
    $privateOffExitCode = $LASTEXITCODE
} finally {
    $env:PATH = $previousPath
}

foreach ($mode in @("baseline", "model")) {
    $deploymentOutput = @($deploymentOutputs[$mode])
    $phase = "prepare-$mode"
    if ($deploymentExitCodes[$mode] -ne 0 -or
        $deploymentOutput -notcontains "mode=$phase" -or
        $deploymentOutput -notcontains "deployment_pass=true" -or
        $deploymentOutput -notcontains `
            "cache_condition=isolated-deployment-process-os-warm") {
        throw "Packaged Rime $phase probe failed: $($deploymentOutput -join '; ')"
    }

    $qualityOutput = @($qualityOutputs[$mode])
    $expectedAccepted = if ($mode -eq "model") { 18 } else { 17 }
    $passedHoldout = @(
        $qualityOutput |
            Where-Object { "$_" -match '^holdout_case=[a-z0-9_]+:pass$' }
    )
    if ($qualityExitCodes[$mode] -ne 0 -or
        $qualityOutput -notcontains "mode=$mode" -or
        $qualityOutput -notcontains "grammar_quality_pass=true" -or
        $qualityOutput -notcontains "accepted_quality_cases=$expectedAccepted" -or
        $qualityOutput -notcontains "holdout_case_count=20" -or
        $qualityOutput -notcontains `
            "cache_condition=process-cold-deployed-user-data-os-warm" -or
        $passedHoldout.Count -ne 20) {
        throw "Packaged Rime $mode probe failed: $($qualityOutput -join '; ')"
    }
}

$modelQualityOutput = @($qualityOutputs["model"])
$baselineQualityOutput = @($qualityOutputs["baseline"])
if ($modelQualityOutput -notcontains "synthetic_private_fixture=pass") {
    throw "Packaged model probe did not preserve the synthetic private fixture"
}
if ($privateOffExitCode -ne 0 -or
    $privateOffOutput -notcontains "mode=private-off" -or
    $privateOffOutput -notcontains "synthetic_private_counterfactual=pass") {
    throw "Packaged private-off counterfactual failed: $($privateOffOutput -join '; ')"
}
$expectedModelLoadLog = "loading gram db: " + (
    Join-Path $rimeDataRoot ([string]$lock.grammarModel.filename))
$modelLoadRows = @(
    $modelQualityOutput | Where-Object {
        "$_" -like ("*" + $expectedModelLoadLog)
    }
)
$allModelLoadRows = @(
    $modelQualityOutput | Where-Object { "$_" -like "*loading gram db:*" }
)
$baselineLoadRows = @(
    $baselineQualityOutput | Where-Object { "$_" -like "*loading gram db:*" }
)
$modelLines = @($modelQualityOutput | ForEach-Object { "$_" })
$schemaBeginIndex = [Array]::IndexOf($modelLines, "schema_select_begin")
$schemaEndIndex = [Array]::IndexOf($modelLines, "schema_select_end")
$modelLoadIndex = -1
for ($index = 0; $index -lt $modelLines.Count; $index++) {
    if ($modelLines[$index] -like
        ("*" + $expectedModelLoadLog)) {
        $modelLoadIndex = $index
    }
}
if ($modelLoadRows.Count -ne 1 -or $allModelLoadRows.Count -ne 1 -or
    $baselineLoadRows.Count -ne 0 -or $schemaBeginIndex -lt 0 -or
    $schemaEndIndex -le $schemaBeginIndex -or
    $modelLoadIndex -le $schemaBeginIndex -or
    $modelLoadIndex -ge $schemaEndIndex) {
    throw "Grammar model file-open log did not occur exactly once inside model schema selection"
}
foreach ($expected in @(
    "grammar_quality=youyuantuma:有原图吗",
    "grammar_quality=youceshizhanghaoma:右侧是账号吗",
    "grammar_quality=shujukushiyongdeshinagebanben:数据库使用的是哪个版本",
    "grammar_quality=qingzaishiyici:请再试一次",
    "grammar_quality=woyijingshoudaole:我已经收到了"
)) {
    if ($modelQualityOutput -notcontains $expected) {
        throw "Packaged model probe omitted expected candidate: $expected"
    }
}

$metricFields = [ordered]@{
    initializeMicroseconds = "initialize_us"
    schemaSelectMicroseconds = "schema_select_us"
    firstCompleteInputMicroseconds = "first_complete_input_us"
    rssAfterInitializeBytes = "rss_after_initialize_bytes"
    rssAfterSchemaSelectBytes = "rss_after_schema_select_bytes"
    rssAfterFirstInputBytes = "rss_after_first_input_bytes"
    rssAfterHoldoutBytes = "rss_after_holdout_bytes"
    measurementMaxRssBytes = "measurement_max_rss_bytes"
    finalKeyCandidateP95Microseconds = "final_key_candidate_p95_us"
    measurementProcessElapsedMicroseconds = "measurement_process_elapsed_us"
}
$deploymentMetricFields = [ordered]@{
    elapsedMicroseconds = "deployment_phase_elapsed_us"
    peakRssBytes = "deployment_phase_peak_rss_bytes"
}
function Read-QualityMetrics {
    param(
        [Parameter(Mandatory = $true)][object[]]$Output,
        [Parameter(Mandatory = $true)][string]$Mode,
        [Parameter(Mandatory = $true)][Collections.IDictionary]$Fields
    )
    $result = [ordered]@{}
    foreach ($property in $Fields.GetEnumerator()) {
        $pattern = '^' + [regex]::Escape([string]$property.Value) + '=([0-9]+)$'
        $rows = @($Output | Where-Object { "$_" -match $pattern })
        if ($rows.Count -ne 1) {
            throw "$Mode probe omitted numeric metric: $($property.Value)"
        }
        $match = [regex]::Match("$($rows[0])", $pattern)
        $value = [long]$match.Groups[1].Value
        if ($value -le 0) {
            throw "$Mode probe emitted zero metric: $($property.Value)"
        }
        $result[$property.Key] = $value
    }
    if ($Fields.Contains("finalKeyCandidateP95Microseconds") -and
        $result.finalKeyCandidateP95Microseconds -gt 20000) {
        throw "$Mode final-key plus candidate-enumeration P95 exceeded 20 ms"
    }
    return $result
}
$baselineDeploymentMetrics = Read-QualityMetrics `
    -Output @($deploymentOutputs["baseline"]) -Mode "prepare-baseline" `
    -Fields $deploymentMetricFields
$modelDeploymentMetrics = Read-QualityMetrics `
    -Output @($deploymentOutputs["model"]) -Mode "prepare-model" `
    -Fields $deploymentMetricFields
$baselineMetrics = Read-QualityMetrics -Output $baselineQualityOutput `
    -Mode "baseline" -Fields $metricFields
$modelMetrics = Read-QualityMetrics -Output $modelQualityOutput `
    -Mode "model" -Fields $metricFields
$modelMinusBaseline = [ordered]@{}
foreach ($property in $metricFields.GetEnumerator()) {
    $modelMinusBaseline[$property.Key] =
        [long]$modelMetrics[$property.Key] - [long]$baselineMetrics[$property.Key]
}
$stageDeltas = [ordered]@{
    initialize = [long]$modelMinusBaseline.rssAfterInitializeBytes
    "schema-select" =
        [long]$modelMinusBaseline.rssAfterSchemaSelectBytes -
        [long]$modelMinusBaseline.rssAfterInitializeBytes
    "first-input" =
        [long]$modelMinusBaseline.rssAfterFirstInputBytes -
        [long]$modelMinusBaseline.rssAfterSchemaSelectBytes
    holdout =
        [long]$modelMinusBaseline.rssAfterHoldoutBytes -
        [long]$modelMinusBaseline.rssAfterFirstInputBytes
}
$largestResidentGrowthStage = ""
$largestStageDelta = [long]::MinValue
foreach ($stage in $stageDeltas.GetEnumerator()) {
    if ([long]$stage.Value -gt $largestStageDelta) {
        $largestResidentGrowthStage = [string]$stage.Key
        $largestStageDelta = [long]$stage.Value
    }
}
if ($largestStageDelta -le 0) {
    throw "A/B RSS evidence has no positive model resident growth"
}
$loadStageEvidence = [ordered]@{
    modelFileOpenObservedStage = "schema-select-before-first-input"
    largestResidentGrowthStage = $largestResidentGrowthStage
    modelMinusBaselineRssAfterInitializeBytes = [long]$stageDeltas.initialize
    modelMinusBaselineRssIncreaseAtSchemaSelectBytes =
        [long]$stageDeltas["schema-select"]
    modelMinusBaselineRssIncreaseAtFirstInputBytes =
        [long]$stageDeltas["first-input"]
    modelMinusBaselineRssIncreaseAtHoldoutBytes = [long]$stageDeltas.holdout
    modelMinusBaselineSchemaSelectMicroseconds =
        [long]$modelMinusBaseline.schemaSelectMicroseconds
    firstInputLatencyDeltaMicroseconds =
        [long]$modelMinusBaseline.firstCompleteInputMicroseconds
    modelFirstInputExceeds20ms =
        ([long]$modelMetrics.firstCompleteInputMicroseconds -gt 20000)
}

$builtSchemaPath = Join-Path $modelQualityUser "build\rime_ice.schema.yaml"
$baselineSchemaPath = Join-Path $baselineQualityUser "build\rime_ice.schema.yaml"
if (-not (Test-Path -LiteralPath $builtSchemaPath -PathType Leaf) -or
    -not (Test-Path -LiteralPath $baselineSchemaPath -PathType Leaf)) {
    throw "Packaged Rime quality probe did not build rime_ice.schema.yaml"
}
$builtSchema = Get-Content -LiteralPath $builtSchemaPath -Raw
$baselineSchema = Get-Content -LiteralPath $baselineSchemaPath -Raw
$grammarModelNamePattern = [regex]::Escape([string]$lock.grammarModel.name)
foreach ($pattern in @(
    '(?m)^grammar:\s*$',
    ('(?m)^\s+language:\s*"?' + $grammarModelNamePattern + '"?\s*$'),
    '(?m)^\s+collocation_max_length:\s*6\s*$',
    '(?m)^\s+collocation_min_length:\s*3\s*$',
    '(?m)^\s+collocation_penalty:\s*(?:-14|"-14")\s*$',
    '(?m)^\s+non_collocation_penalty:\s*(?:-6|"-6")\s*$',
    '(?m)^\s+weak_collocation_penalty:\s*(?:-100|"-100")\s*$',
    '(?m)^\s+rear_penalty:\s*(?:-20|"-20")\s*$',
    '(?m)^\s+contextual_suggestions:\s*true\s*$',
    '(?m)^\s+enable_correction:\s*false\s*$',
    '(?m)^\s+max_homophones:\s*8\s*$',
    '(?m)^\s+max_sentences:\s*1\s*$',
    '(?m)^\s+long_correction_guard:\s*true\s*$',
    '(?m)^\s+short_input_guard:\s*true\s*$',
    '(?m)^\s+typo_correction:\s*false\s*$'
)) {
    if ($builtSchema -notmatch $pattern) {
        throw "Built packaged Rime Ice schema omitted grammar/safety setting: $pattern"
    }
}
if ($builtSchema -match '(?m)^\s+max_homographs:') {
    throw "Built packaged Rime Ice schema contains ineffective max_homographs"
}
if ($baselineSchema -notmatch '(?m)^\s+contextual_suggestions:\s*false\s*$' -or
    $baselineSchema -match '(?m)^grammar:\s*$' -or
    $baselineSchema -match '(?m)^\s+language:\s*wanxiang-') {
    throw "Built no-grammar baseline schema retained model configuration"
}
foreach ($pattern in @(
    '(?m)^\s+enable_correction:\s*false\s*$',
    '(?m)^\s+max_sentences:\s*1\s*$',
    '(?m)^\s+long_correction_guard:\s*true\s*$',
    '(?m)^\s+short_input_guard:\s*true\s*$',
    '(?m)^\s+typo_correction:\s*false\s*$'
)) {
    if ($baselineSchema -notmatch $pattern) {
        throw "Built no-grammar baseline changed a non-grammar safety setting"
    }
}

Write-Host "--- no-grammar baseline (process-cold, OS-warm) ---"
$baselineQualityOutput | ForEach-Object { Write-Host "$_" }
Write-Host "--- Wanxiang model (process-cold, OS-warm) ---"
$modelQualityOutput | ForEach-Object { Write-Host "$_" }

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
    mergedPlugins = @("librime-yunpin", "librime-octagram")
    mergedModules = @("yunpin", "octagram", "grammar")
    privateCandidateSnapshotEnabled = $false
    grammarModel = $lock.grammarModel
    grammarQuality = [ordered]@{
        headlessRimeIce = $true
        cacheCondition = "process-cold-deployed-user-data-os-warm"
        comparisonOrder = @("baseline", "model")
        deploymentPhase = [ordered]@{
            cacheCondition = "isolated-deployment-process-os-warm"
            processIsolation = "separate-prepare-process"
            baseline = $baselineDeploymentMetrics
            model = $modelDeploymentMetrics
        }
        measurementPhase = [ordered]@{
            processIsolation = "fresh-process-after-deployment"
            maintenanceInvoked = $false
        }
        holdoutCaseCount = 20
        acceptedQualityCases = [ordered]@{
            baseline = 17
            model = 18
        }
        finalKeyCandidateP95Microseconds = $modelMetrics.finalKeyCandidateP95Microseconds
        gateMicroseconds = 20000
        syntheticPrivateCounterfactual = $true
        baseline = $baselineMetrics
        model = $modelMetrics
        modelMinusBaseline = $modelMinusBaseline
        loadStageEvidence = $loadStageEvidence
        publicCases = @(
            "youyuantuma", "youceshizhanghaoma",
            "shujukushiyongdeshinagebanben", "qingzaishiyici",
            "woyijingshoudaole"
        )
    }
    syncAgent = [ordered]@{
        bundled = $true
        target = "windows-amd64"
        build = "public-default-tag"
        privatePairingCommands = $false
        residentDefault = "disabled"
    }
    upstreams = [ordered]@{
        weasel = $lock.weasel.commit
        librime = $lock.librime.commit
        librimeOctagram = $lock.librimeOctagram.commit
        librimeOctagramSourceSha256 = $lock.librimeOctagram.sha256
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
$unicodeSourceRelative = "third_party\rime-ice\others\asserts\扩展-Unicode_compressed.webp"
$mojibakeSourceRelative = "third_party\rime-ice\others\asserts\µë⌐σ▒ò-Unicode_compressed.webp"
$unicodeSourcePath = Join-Path $sourceRoot $unicodeSourceRelative
$mojibakeSourcePath = Join-Path $sourceRoot $mojibakeSourceRelative
if (-not (Test-Path -LiteralPath $unicodeSourcePath -PathType Leaf) -or
    (Test-Path -LiteralPath $mojibakeSourcePath)) {
    throw "Windows corresponding source export did not preserve the locked UTF-8 path"
}
Export-GitSubtree -Checkout $repoRoot -Tree "librime-yunpin" -Destination (Join-Path $sourceRoot "librime-yunpin") -ScratchRoot $scratchRoot
# Every other subtree in this archive comes from the git tree, so its contents
# correspond to $repoCommit, which BUILD-SOURCE-METADATA.json records. The
# engine was copied from the working tree instead, which meant the recorded
# commit and the shipped engine sources could disagree -- and any untracked file
# under engine/ would have been packaged. Use the same git export as the rest.
Export-GitSubtree -Checkout $repoRoot -Tree "engine" -Destination (Join-Path $sourceRoot "engine") -ScratchRoot $scratchRoot
Export-GitSubtree -Checkout $repoRoot -Tree "platform/windows" -Destination (Join-Path $sourceRoot "platform\windows") -ScratchRoot $scratchRoot
$privateE2ESource = [IO.Path]::GetFullPath((Join-Path $sourceRoot "platform\windows\e2e"))
$sourcePrefixForExclusion = [IO.Path]::GetFullPath($sourceRoot).TrimEnd("\") + "\"
if (-not $privateE2ESource.StartsWith($sourcePrefixForExclusion, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to exclude a private E2E source path outside the generated source root"
}
if (Test-Path -LiteralPath $privateE2ESource) {
    $privateE2EItem = Get-Item -LiteralPath $privateE2ESource -Force
    if (-not $privateE2EItem.PSIsContainer -or
        ($privateE2EItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing to remove a non-directory or reparse-point private E2E source export"
    }
    Remove-Item -LiteralPath $privateE2ESource -Recurse -Force
}
Export-GitSubtree -Checkout $repoRoot -Tree "platform/rime" -Destination (Join-Path $sourceRoot "platform\rime") -ScratchRoot $scratchRoot
Export-GitSubtree -Checkout $repoRoot -Tree "platform/patches/weasel" -Destination (Join-Path $sourceRoot "platform\patches\weasel") -ScratchRoot $scratchRoot
Export-GitSubtree -Checkout $repoRoot -Tree "platform/patches/librime-1.17" -Destination (Join-Path $sourceRoot "platform\patches\librime-1.17") -ScratchRoot $scratchRoot
foreach ($tree in @("desktopagent", "localstore", "protocol", "replaylab", "syncclient")) {
    Export-GitSubtree -Checkout $repoRoot -Tree $tree -Destination (Join-Path $sourceRoot $tree) -ScratchRoot $scratchRoot
}
foreach ($file in @("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md")) {
    Copy-Item -LiteralPath (Join-Path $repoRoot $file) -Destination $sourceRoot -Force
}
New-Item -ItemType Directory -Path (Join-Path $sourceRoot "third_party"), (Join-Path $sourceRoot "scripts") -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot "third_party\go-modules.lock.json") -Destination (Join-Path $sourceRoot "third_party\go-modules.lock.json") -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "third_party\upstreams.lock.json") -Destination (Join-Path $sourceRoot "third_party\upstreams.lock.json") -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "scripts\package_go_licenses.py") -Destination (Join-Path $sourceRoot "scripts\package_go_licenses.py") -Force
Copy-Item -LiteralPath (Join-Path $repoRoot "scripts\verify_grammar_asset_metadata.py") -Destination (Join-Path $sourceRoot "scripts\verify_grammar_asset_metadata.py") -Force
New-Item -ItemType Directory -Path (Join-Path $sourceRoot "docs") -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot "docs\LICENSE_MATRIX.md") -Destination (Join-Path $sourceRoot "docs\LICENSE_MATRIX.md") -Force
New-Item -ItemType Directory -Path (Join-Path $sourceRoot "platform") -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot "platform\upstream-lock.json") -Destination (Join-Path $sourceRoot "platform\upstream-lock.json") -Force
$sourceBoost = Join-Path $OutputRoot "cache\boost_1_84_0.7z"
if (-not (Test-Path $sourceBoost -PathType Leaf)) {
    throw "Verified Boost source archive is missing: $sourceBoost"
}
New-Item -ItemType Directory -Path (Join-Path $sourceRoot "sources") -Force | Out-Null
Copy-Item -LiteralPath $sourceBoost -Destination (Join-Path $sourceRoot "sources\boost_1_84_0.7z") -Force
$sourceOctagram = Join-Path $OutputRoot ("cache\" + [string]$lock.librimeOctagram.archiveName)
if (-not (Test-Path $sourceOctagram -PathType Leaf)) {
    throw "Verified librime-octagram source archive is missing: $sourceOctagram"
}
$sourceOctagramHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourceOctagram).Hash.ToLowerInvariant()
if ($sourceOctagramHash -ne ([string]$lock.librimeOctagram.sha256).ToLowerInvariant()) {
    throw "Verified librime-octagram source archive no longer matches the dependency lock"
}
Copy-Item -LiteralPath $sourceOctagram `
    -Destination (Join-Path $sourceRoot ("sources\" + [string]$lock.librimeOctagram.archiveName)) -Force
Copy-Item -LiteralPath $grammarModelCache `
    -Destination (Join-Path $sourceRoot ("sources\" + [string]$lock.grammarModel.filename)) -Force
Copy-Item -LiteralPath $grammarLicenseCache `
    -Destination (Join-Path $sourceRoot ("sources\" + [string]$lock.grammarModel.licenseFilename)) -Force
$sourceGrammarModel = Join-Path $sourceRoot ("sources\" + [string]$lock.grammarModel.filename)
$sourceGrammarLicense = Join-Path $sourceRoot ("sources\" + [string]$lock.grammarModel.licenseFilename)
Assert-LockedFile -Path $sourceGrammarModel -Sha256 $lock.grammarModel.sha256 `
    -Size $lock.grammarModel.size -Label "Source-bundled grammar model"
Assert-LockedFile -Path $sourceGrammarLicense -Sha256 $lock.grammarModel.licenseSha256 `
    -Size $lock.grammarModel.licenseSize -Label "Source-bundled grammar model license"
$sourceGrammarModels = @(Get-ChildItem -LiteralPath $sourceRoot -File -Recurse -Filter "*.gram")
if ($sourceGrammarModels.Count -ne 1 -or
    $sourceGrammarModels[0].FullName -cne $sourceGrammarModel) {
    throw "Windows corresponding source must contain exactly one locked grammar model"
}
$sourceMetadata = [ordered]@{
    schemaVersion = 1
    repositoryCommit = $repoCommit
    buildEntryPoint = "platform/windows/scripts/Build-Preview.ps1"
    weaselBase = $lock.weasel.commit
    patches = $lock.weasel.patches
    librime = $lock.librime.commit
    librimePatches = $lock.librime.patches
    librimeDependencies = $lock.librime.dependencies
    librimeOctagram = $lock.librimeOctagram
    rimeIce = $lock.rimeIce.commit
    boostSourceSha256 = $lock.boost.sha256
    grammarModel = $lock.grammarModel
}
$sourceMetadata | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $sourceRoot "BUILD-SOURCE-METADATA.json") -Encoding UTF8
$sourceReadme = @'
YunPin IME Windows development-preview corresponding source
============================================================

This archive contains the exact exported source trees and commit markers for
Weasel, librime, librime's nested dependencies, and Rime Ice, plus YunPin's
engine, merged plugin, public default-tag sync agent source and its local Go
modules, Windows scripts, ordered GPL patches, licenses, and the verified Boost
and librime-octagram source archives, and one exact full grammar-model release
asset plus its license and overlay. It contains no private phrase data, E2E
binary artifact, or private E2E activation script.

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
$unicodeSourceEntry = $unicodeSourceRelative.Replace("\", "/")
$mojibakeSourceEntry = $mojibakeSourceRelative.Replace("\", "/")
$sourceZip = [IO.Compression.ZipFile]::OpenRead($sourceArchive)
try {
    $sourceArchiveEntries = @($sourceZip.Entries | ForEach-Object { $_.FullName })
    if (@($sourceArchiveEntries | Where-Object { $_ -ceq $unicodeSourceEntry }).Count -ne 1 -or
        @($sourceArchiveEntries | Where-Object { $_ -ceq $mojibakeSourceEntry }).Count -ne 0) {
        throw "Windows corresponding source ZIP did not preserve the locked UTF-8 entry name"
    }
} finally {
    $sourceZip.Dispose()
}
$runtimeHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $runtimeArchive).Hash.ToLowerInvariant()
$sourceHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourceArchive).Hash.ToLowerInvariant()
$artifactHashes = @(
    "$runtimeHash  $([IO.Path]::GetFileName($runtimeArchive))",
    "$sourceHash  $([IO.Path]::GetFileName($sourceArchive))"
)
[IO.File]::WriteAllLines((Join-Path $artifactsRoot "SHA256SUMS"), $artifactHashes, ([Text.UTF8Encoding]::new($false)))

Write-Host "Windows development preview package: $runtimeArchive"
Write-Host "Corresponding source package: $sourceArchive"
