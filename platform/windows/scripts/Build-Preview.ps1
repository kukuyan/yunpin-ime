# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [string]$OutputRoot = "",
    [switch]$SkipPackage,
    [switch]$Offline,
    [switch]$TestGrammarCacheSafety
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$script:YunPinOfflineBuild = $Offline.IsPresent

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

function Assert-CheckoutCommit {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Expected,
        [Parameter(Mandatory = $true)][string]$Name
    )
    $gitMetadata = Join-Path $Path ".git"
    $sourceMarker = Join-Path $Path ".yunpin-source-commit"
    if (Test-Path $gitMetadata) {
        $observed = (& git -C $Path rev-parse HEAD).Trim()
        if ($LASTEXITCODE -ne 0 -or $observed -ne $Expected) {
            throw "$Name checkout $observed does not match lock $Expected"
        }
        return
    }
    if (Test-Path $sourceMarker -PathType Leaf) {
        $observed = (Get-Content -LiteralPath $sourceMarker -Raw).Trim()
        if ($observed -ne $Expected) {
            throw "$Name source marker $observed does not match lock $Expected"
        }
        return
    }
    throw "$Name is neither an initialized checkout nor a YunPin source export at $Path"
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
    $archiveName = ([IO.Path]::GetFileName($Destination) + "-" + [guid]::NewGuid().ToString("N") + ".tar")
    $archive = Join-Path $ScratchRoot $archiveName
    Invoke-Checked -FilePath "git" -ArgumentList @(
        "-C", $Checkout, "archive", "--format=tar", "--output=$archive", "HEAD"
    )
    Invoke-Checked -FilePath "tar.exe" -ArgumentList @("-xf", $archive, "-C", $Destination)
    Remove-Item -LiteralPath $archive -Force
}

function Get-VerifiedDownload {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [Parameter(Mandatory = $true)][string]$Sha256,
        [Parameter(Mandatory = $true)][string]$Destination
    )
    if (Test-Path $Destination) {
        $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $Destination).Hash.ToLowerInvariant()
        if ($observed -eq $Sha256.ToLowerInvariant()) {
            return
        }
        Remove-Item -LiteralPath $Destination -Force
    }
    if ($script:YunPinOfflineBuild) {
        throw "Offline build is missing a locked local dependency: $Destination"
    }
    Invoke-WebRequest -Uri $Uri -OutFile $Destination -UseBasicParsing
    $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $Destination).Hash.ToLowerInvariant()
    if ($observed -ne $Sha256.ToLowerInvariant()) {
        Remove-Item -LiteralPath $Destination -Force
        throw "SHA-256 mismatch for $Uri"
    }
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

function Assert-OnlineGrammarAssetMetadata {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$DependencyLock,
        [Parameter(Mandatory = $true)][string]$ScratchRoot
    )
    if ($script:YunPinOfflineBuild) {
        throw "Offline build attempted mutable grammar release metadata access"
    }
    $metadataRoot = Join-Path $ScratchRoot "grammar-release-metadata"
    Reset-GeneratedDirectory -Path $metadataRoot -AllowedParent $ScratchRoot
    $headers = @{
        Accept = "application/vnd.github+json"
        "X-GitHub-Api-Version" = "2022-11-28"
    }
    if (-not [string]::IsNullOrWhiteSpace($env:GITHUB_TOKEN)) {
        $headers.Authorization = "Bearer $($env:GITHUB_TOKEN)"
    }
    try {
        $releaseJson = Join-Path $metadataRoot "release.json"
        $tagJson = Join-Path $metadataRoot "tag.json"
        Invoke-WebRequest `
            -Uri "https://api.github.com/repos/amzxyz/RIME-LMDG/releases/tags/LTS" `
            -Headers $headers -OutFile $releaseJson -UseBasicParsing
        Invoke-WebRequest `
            -Uri "https://api.github.com/repos/amzxyz/RIME-LMDG/git/ref/tags/LTS" `
            -Headers $headers -OutFile $tagJson -UseBasicParsing
        Invoke-Checked -FilePath "python" -ArgumentList @(
            (Join-Path $RepositoryRoot "scripts\verify_grammar_asset_metadata.py"),
            "--lock", $DependencyLock,
            "--release-json", $releaseJson,
            "--tag-json", $tagJson
        )
    } finally {
        if (Test-Path -LiteralPath $metadataRoot) {
            Remove-Item -LiteralPath $metadataRoot -Recurse -Force
        }
    }
}

function Assert-SafeCacheTemporaryFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$ExpectedDirectory,
        [switch]$RequireEmpty
    )
    $fullPath = [IO.Path]::GetFullPath($Path)
    $fullDirectory = [IO.Path]::GetFullPath($ExpectedDirectory).TrimEnd("\")
    if (-not [string]::Equals(
            [IO.Path]::GetDirectoryName($fullPath),
            $fullDirectory,
            [StringComparison]::OrdinalIgnoreCase)) {
        throw "Locked cache temporary file escaped its cache directory"
    }
    if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) {
        throw "Locked cache temporary file is missing or not a regular file"
    }
    $item = Get-Item -LiteralPath $fullPath -Force
    if ($item.PSIsContainer -or
        ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Locked cache temporary file must not be a reparse point"
    }
    if ($RequireEmpty -and $item.Length -ne 0) {
        throw "New locked cache temporary file is unexpectedly non-empty"
    }
}

function New-ExclusiveCacheTemporaryFile {
    param(
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][string]$Label
    )
    $fullDestination = [IO.Path]::GetFullPath($Destination)
    $directory = [IO.Path]::GetDirectoryName($fullDestination)
    $directoryItem = Get-Item -LiteralPath $directory -Force
    if (-not $directoryItem.PSIsContainer -or
        ($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label cache directory must be an ordinary directory"
    }
    $leaf = [IO.Path]::GetFileName($fullDestination)
    foreach ($attempt in 1..16) {
        $stream = $null
        $candidate = Join-Path $directory (
            "." + $leaf + "." + [guid]::NewGuid().ToString("N") + ".part"
        )
        try {
            $stream = [IO.File]::Open(
                $candidate,
                [IO.FileMode]::CreateNew,
                [IO.FileAccess]::Write,
                [IO.FileShare]::None
            )
            Assert-SafeCacheTemporaryFile -Path $candidate `
                -ExpectedDirectory $directory -RequireEmpty
            return [pscustomobject]@{
                Path = $candidate
                Stream = $stream
            }
        } catch [IO.IOException] {
            if ($null -ne $stream) {
                $stream.Dispose()
            }
            continue
        } catch {
            if ($null -ne $stream) {
                $stream.Dispose()
            }
            throw
        }
    }
    throw "Could not create an exclusive temporary file for $Label"
}

function Install-LockedCacheResource {
    param(
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][string]$Bundled,
        [Parameter(Mandatory = $true)][string]$Uri,
        [Parameter(Mandatory = $true)][string]$Sha256,
        [Parameter(Mandatory = $true)][long]$Size,
        [Parameter(Mandatory = $true)][string]$Label,
        [switch]$VerifyOnlineGrammarMetadata,
        [string]$RepositoryRoot = "",
        [string]$DependencyLock = "",
        [string]$ScratchRoot = ""
    )
    if (Test-Path -LiteralPath $Destination) {
        Assert-LockedFile -Path $Destination -Sha256 $Sha256 `
            -Size $Size -Label "$Label cache"
        return
    }

    $temporaryHandle = New-ExclusiveCacheTemporaryFile `
        -Destination $Destination -Label $Label
    $temporary = [string]$temporaryHandle.Path
    $temporaryStream = [IO.FileStream]$temporaryHandle.Stream
    $temporaryDirectory = [IO.Path]::GetDirectoryName($temporary)
    try {
        Assert-SafeCacheTemporaryFile -Path $temporary `
            -ExpectedDirectory $temporaryDirectory -RequireEmpty
        if (Test-Path -LiteralPath $Bundled) {
            Assert-LockedFile -Path $Bundled -Sha256 $Sha256 `
                -Size $Size -Label "Source-bundled $Label"
            $sourceStream = [IO.File]::Open(
                $Bundled,
                [IO.FileMode]::Open,
                [IO.FileAccess]::Read,
                [IO.FileShare]::Read
            )
            try {
                $sourceStream.CopyTo($temporaryStream)
            } finally {
                $sourceStream.Dispose()
            }
        } else {
            if ($VerifyOnlineGrammarMetadata) {
                Assert-OnlineGrammarAssetMetadata `
                    -RepositoryRoot $RepositoryRoot `
                    -DependencyLock $DependencyLock `
                    -ScratchRoot $ScratchRoot
            }
            if ($script:YunPinOfflineBuild) {
                throw "Offline build is missing a locked local dependency: $Bundled"
            }
            Assert-SafeCacheTemporaryFile -Path $temporary `
                -ExpectedDirectory $temporaryDirectory -RequireEmpty
            $httpClient = [Net.Http.HttpClient]::new()
            $httpResponse = $null
            $httpStream = $null
            try {
                $httpClient.DefaultRequestHeaders.UserAgent.ParseAdd(
                    "YunPin-locked-dependency-fetch/1")
                $httpResponse = $httpClient.GetAsync(
                    $Uri,
                    [Net.Http.HttpCompletionOption]::ResponseHeadersRead
                ).GetAwaiter().GetResult()
                $null = $httpResponse.EnsureSuccessStatusCode()
                $httpStream = $httpResponse.Content.ReadAsStreamAsync(
                ).GetAwaiter().GetResult()
                $httpStream.CopyTo($temporaryStream)
            } finally {
                if ($null -ne $httpStream) { $httpStream.Dispose() }
                if ($null -ne $httpResponse) { $httpResponse.Dispose() }
                $httpClient.Dispose()
            }
        }
        $temporaryStream.Flush($true)
        $temporaryStream.Dispose()
        $temporaryStream = $null
        Assert-LockedFile -Path $temporary -Sha256 $Sha256 `
            -Size $Size -Label "Staged $Label"
        Assert-SafeCacheTemporaryFile -Path $temporary `
            -ExpectedDirectory $temporaryDirectory

        # File.Move(source, destination) is a same-directory atomic rename and
        # the two-argument overload refuses to replace a destination that
        # appeared while the bytes were being staged.
        [IO.File]::Move($temporary, $Destination)
        $temporary = ""
        try {
            Assert-LockedFile -Path $Destination -Sha256 $Sha256 `
                -Size $Size -Label "$Label cache"
        } catch {
            # The non-clobbering move proves this destination was the file
            # published by this invocation. Never leave it behind if the
            # post-publication identity check fails.
            if (Test-Path -LiteralPath $Destination) {
                [IO.File]::Delete($Destination)
            }
            throw
        }
    } finally {
        if ($null -ne $temporaryStream) {
            $temporaryStream.Dispose()
        }
        if (-not [string]::IsNullOrEmpty($temporary) -and
            (Test-Path -LiteralPath $temporary)) {
            Remove-Item -LiteralPath $temporary -Force
        }
    }
}

function Invoke-GrammarCacheSafetySelfTest {
    $root = Join-Path ([IO.Path]::GetTempPath()) (
        "yunpin-grammar-cache-safety-" + [guid]::NewGuid().ToString("N")
    )
    $cache = Join-Path $root "cache"
    $sources = Join-Path $root "sources"
    $outside = Join-Path $root "outside"
    New-Item -ItemType Directory -Path $cache, $sources, $outside | Out-Null
    try {
        $source = Join-Path $sources "synthetic.gram"
        [IO.File]::WriteAllText(
            $source,
            "fixed public grammar cache safety fixture`n",
            [Text.Encoding]::UTF8
        )
        $sourceSize = (Get-Item -LiteralPath $source).Length
        $sourceSha = (Get-FileHash -Algorithm SHA256 -LiteralPath $source).Hash
        $outsideSentinel = Join-Path $outside "sentinel.txt"
        [IO.File]::WriteAllText($outsideSentinel, "unchanged`n", [Text.Encoding]::UTF8)

        $destination = Join-Path $cache "synthetic.gram"
        $predictablePartial = $destination + ".part"
        New-Item -ItemType Junction -Path $predictablePartial `
            -Target $outside | Out-Null
        Install-LockedCacheResource -Destination $destination `
            -Bundled $source -Uri "https://example.invalid/not-used" `
            -Sha256 $sourceSha -Size $sourceSize -Label "Synthetic grammar model"
        Assert-LockedFile -Path $destination -Sha256 $sourceSha `
            -Size $sourceSize -Label "Synthetic grammar model cache"
        $outsideNames = @(Get-ChildItem -LiteralPath $outside -Force).Name
        if ((@($outsideNames) -join ",") -cne "sentinel.txt") {
            throw "Predictable partial reparse point received an out-of-cache write"
        }

        $reparseDestination = Join-Path $cache "destination-reparse.gram"
        New-Item -ItemType Junction -Path $reparseDestination `
            -Target $outside | Out-Null
        $reparseRejected = $false
        try {
            Install-LockedCacheResource -Destination $reparseDestination `
                -Bundled $source -Uri "https://example.invalid/not-used" `
                -Sha256 $sourceSha -Size $sourceSize -Label "Reparse fixture"
        } catch {
            $reparseRejected = $true
        }
        if (-not $reparseRejected) {
            throw "Locked cache destination reparse point was accepted"
        }

        $tampered = Join-Path $sources "tampered.gram"
        [IO.File]::WriteAllText($tampered, "tampered`n", [Text.Encoding]::UTF8)
        $tamperedDestination = Join-Path $cache "tampered.gram"
        $tamperRejected = $false
        try {
            Install-LockedCacheResource -Destination $tamperedDestination `
                -Bundled $tampered -Uri "https://example.invalid/not-used" `
                -Sha256 $sourceSha -Size $sourceSize -Label "Tampered fixture"
        } catch {
            $tamperRejected = $true
        }
        if (-not $tamperRejected -or (Test-Path -LiteralPath $tamperedDestination)) {
            throw "Tampered locked cache resource was published"
        }
        if (@(Get-ChildItem -LiteralPath $cache -Force -File |
                Where-Object { $_.Name -like ".tampered.gram.*.part" }).Count -ne 0) {
            throw "Failed locked cache staging left a temporary file"
        }
        Write-Host "Windows grammar cache exclusive-temp and reparse tests passed"
    } finally {
        if (Test-Path -LiteralPath $root) {
            Remove-Item -LiteralPath $root -Recurse -Force
        }
    }
}

function Assert-SourceManifest {
    param([Parameter(Mandatory = $true)][string]$Root)

    $rootFull = [IO.Path]::GetFullPath($Root).TrimEnd("\")
    $rootPrefix = $rootFull + "\"
    $manifestPath = Join-Path $rootFull "SOURCE-MANIFEST.sha256"
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        throw "Offline source export is missing SOURCE-MANIFEST.sha256"
    }
    $manifestItem = Get-Item -LiteralPath $manifestPath -Force
    if (($manifestItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Offline source manifest must not be a reparse point"
    }

    $expected = New-Object 'Collections.Generic.Dictionary[string,string]' `
        ([StringComparer]::Ordinal)
    foreach ($line in [IO.File]::ReadAllLines($manifestPath)) {
        if ($line -notmatch '^([0-9a-f]{64})  (.+)$') {
            throw "Offline source manifest contains a malformed row"
        }
        $digest = $Matches[1]
        $relative = $Matches[2]
        if ($relative.Contains("\") -or [IO.Path]::IsPathRooted($relative) -or
            @($relative.Split('/')) -contains ".." -or
            $relative -ceq "SOURCE-MANIFEST.sha256" -or
            $expected.ContainsKey($relative)) {
            throw "Offline source manifest contains an unsafe or duplicate path"
        }
        $path = [IO.Path]::GetFullPath((Join-Path $rootFull $relative.Replace('/', '\')))
        if (-not $path.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase) -or
            -not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Offline source manifest path is missing or escapes its root: $relative"
        }
        $item = Get-Item -LiteralPath $path -Force
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Offline source manifest path is a reparse point: $relative"
        }
        $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        if ($observed -cne $digest) {
            throw "Offline source manifest digest mismatch: $relative"
        }
        $expected.Add($relative, $digest)
    }

    $actualCount = 0
    Get-ChildItem -LiteralPath $rootFull -File -Recurse -Force | ForEach-Object {
        if ($_.FullName -cne $manifestPath) {
            if (($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Offline source export contains a reparse-point file"
            }
            $relative = $_.FullName.Substring($rootPrefix.Length).Replace('\', '/')
            if (-not $expected.ContainsKey($relative)) {
                throw "Offline source export contains an unmanifested file: $relative"
            }
            $actualCount++
        }
    }
    if ($actualCount -ne $expected.Count) {
        throw "Offline source manifest does not cover every source file"
    }
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Build-Preview.ps1 must run on Windows"
}
if (-not [Environment]::Is64BitOperatingSystem) {
    throw "The preview build requires a 64-bit Windows host"
}
if ($TestGrammarCacheSafety) {
    Invoke-GrammarCacheSafetySelfTest
    return
}

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = [IO.Path]::GetFullPath((Join-Path $scriptRoot "..\..\.."))
$lockPath = Join-Path $repoRoot "platform\windows\dependencies.lock.json"
$lock = Get-Content -LiteralPath $lockPath -Raw | ConvertFrom-Json

if ($script:YunPinOfflineBuild -and
    (Test-Path (Join-Path $repoRoot ".git"))) {
    throw "Offline source acceptance must run from an extracted Git-free archive"
}

if (-not (Test-Path (Join-Path $repoRoot ".git"))) {
    $sourceMetadataPath = Join-Path $repoRoot "BUILD-SOURCE-METADATA.json"
    if (-not (Test-Path -LiteralPath $sourceMetadataPath -PathType Leaf)) {
        throw "Git-free build requires BUILD-SOURCE-METADATA.json"
    }
    Assert-SourceManifest -Root $repoRoot
    $sourceMetadata = Get-Content -LiteralPath $sourceMetadataPath -Raw | ConvertFrom-Json
    $lockedGrammarJson = $lock.grammarModel | ConvertTo-Json -Depth 8 -Compress
    $sourceGrammarJson = $sourceMetadata.grammarModel | ConvertTo-Json -Depth 8 -Compress
    if ($sourceGrammarJson -cne $lockedGrammarJson) {
        throw "Offline source grammar model metadata differs from its dependency lock"
    }
}

if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $OutputRoot = Join-Path $repoRoot "build\windows"
}
$OutputRoot = [IO.Path]::GetFullPath($OutputRoot)
New-Item -ItemType Directory -Path $OutputRoot -Force | Out-Null

$cacheRoot = Join-Path $OutputRoot "cache"
$depsRoot = Join-Path $OutputRoot "deps"
$scratchRoot = Join-Path $OutputRoot "scratch"
$weaselSource = Join-Path $OutputRoot "weasel-src"
foreach ($directory in @($cacheRoot, $depsRoot, $scratchRoot)) {
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
}

$grammarModelPath = Join-Path $cacheRoot ([string]$lock.grammarModel.filename)
$bundledGrammarModelPath = Join-Path $repoRoot ("sources\" + [string]$lock.grammarModel.filename)
Install-LockedCacheResource -Destination $grammarModelPath `
    -Bundled $bundledGrammarModelPath -Uri $lock.grammarModel.url `
    -Sha256 $lock.grammarModel.sha256 -Size $lock.grammarModel.size `
    -Label "Grammar model" -VerifyOnlineGrammarMetadata `
    -RepositoryRoot $repoRoot -DependencyLock $lockPath -ScratchRoot $scratchRoot

$grammarLicensePath = Join-Path $cacheRoot ([string]$lock.grammarModel.licenseFilename)
$bundledGrammarLicensePath = Join-Path $repoRoot ("sources\" + [string]$lock.grammarModel.licenseFilename)
Install-LockedCacheResource -Destination $grammarLicensePath `
    -Bundled $bundledGrammarLicensePath -Uri $lock.grammarModel.licenseUrl `
    -Sha256 $lock.grammarModel.licenseSha256 `
    -Size $lock.grammarModel.licenseSize -Label "Grammar model license"

$weaselCheckout = Join-Path $repoRoot "third_party\weasel"
$librimeCheckout = Join-Path $repoRoot "third_party\librime"
$rimeIceCheckout = Join-Path $repoRoot "third_party\rime-ice"
Assert-CheckoutCommit $weaselCheckout $lock.weasel.commit "Weasel"
Assert-CheckoutCommit $librimeCheckout $lock.librime.commit "librime"
Assert-CheckoutCommit $rimeIceCheckout $lock.rimeIce.commit "Rime Ice"

foreach ($dependency in $lock.librime.dependencies.PSObject.Properties) {
    $dependencyCheckout = Join-Path $librimeCheckout $dependency.Name
    Assert-CheckoutCommit $dependencyCheckout ([string]$dependency.Value) "librime/$($dependency.Name)"
}

$pluginRoot = Join-Path $repoRoot "librime-yunpin"
$engineRoot = Join-Path $repoRoot "engine"
foreach ($required in @(
    (Join-Path $pluginRoot "CMakeLists.txt"),
    (Join-Path $pluginRoot "src\yunpin_module.cpp"),
    (Join-Path $engineRoot "include"),
    (Join-Path $engineRoot "src\phrase_engine.cpp")
)) {
    if (-not (Test-Path $required)) {
        throw "Required merged-plugin source is missing: $required"
    }
}

foreach ($patchEntry in @($lock.weasel.patches) + @($lock.librime.patches)) {
    $patchPath = Join-Path $repoRoot $patchEntry.path
    $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $patchPath).Hash.ToLowerInvariant()
    if ($observed -ne ([string]$patchEntry.sha256).ToLowerInvariant()) {
        throw "Source patch hash mismatch: $($patchEntry.path)"
    }
}

# Hashing the locked entries proves every locked patch is intact, but says
# nothing about a patch sitting in the directory that the lock does not mention.
# macOS compares the directory listing against the lock for exactly that reason,
# and it is what caught a directory full of file-sync conflict copies. Windows
# enumerated the lock only, so an unlocked patch stayed invisible here.
foreach ($patchSet in @(
    @{ Directory = "platform\patches\weasel"; Entries = @($lock.weasel.patches) },
    @{ Directory = "platform\patches\librime-1.17"; Entries = @($lock.librime.patches) }
)) {
    $patchDirectory = Join-Path $repoRoot $patchSet.Directory
    if (-not (Test-Path -LiteralPath $patchDirectory -PathType Container)) {
        throw "Locked patch directory is missing: $($patchSet.Directory)"
    }
    $onDisk = @(Get-ChildItem -LiteralPath $patchDirectory -File -Filter "*.patch" |
        Sort-Object Name | ForEach-Object { $_.Name })
    $locked = @($patchSet.Entries | ForEach-Object { [IO.Path]::GetFileName($_.path) } | Sort-Object)
    if ($onDisk.Count -ne $locked.Count -or
        @(Compare-Object -ReferenceObject $locked -DifferenceObject $onDisk -CaseSensitive).Count -ne 0) {
        throw ("Patch directory $($patchSet.Directory) does not match the lock. " +
            "On disk: $($onDisk -join ', '). Locked: $($locked -join ', ').")
    }
}

Reset-GeneratedDirectory -Path $weaselSource -AllowedParent $OutputRoot
Export-GitTree -Checkout $weaselCheckout -Destination $weaselSource -ScratchRoot $scratchRoot

$previousGitCeilingDirectories = $env:GIT_CEILING_DIRECTORIES
try {
    # The generated trees live below OutputRoot, which may be a sibling of an
    # extracted offline source root.  Without this ceiling, git apply can find
    # an unrelated parent repository and return success without touching the
    # generated tree.  Keep patch application standalone in both layouts.
    $env:GIT_CEILING_DIRECTORIES = $OutputRoot
    foreach ($patchEntry in $lock.weasel.patches) {
        $patchPath = Join-Path $repoRoot $patchEntry.path
        Push-Location $weaselSource
        try {
            Invoke-Checked -FilePath "git" -ArgumentList @(
                "-c", "core.whitespace=cr-at-eol", "apply", "--check",
                "--ignore-space-change", "--whitespace=error-all", $patchPath
            )
            Invoke-Checked -FilePath "git" -ArgumentList @(
                "-c", "core.whitespace=cr-at-eol", "apply",
                "--ignore-space-change", "--whitespace=error-all", $patchPath
            )
        } finally {
            Pop-Location
        }
    }
} finally {
    if ($null -eq $previousGitCeilingDirectories) {
        Remove-Item Env:GIT_CEILING_DIRECTORIES -ErrorAction SilentlyContinue
    } else {
        $env:GIT_CEILING_DIRECTORIES = $previousGitCeilingDirectories
    }
}

$stagedConstants = Get-Content -LiteralPath (Join-Path $weaselSource "include\WeaselConstants.h") -Raw
$stagedSetup = Get-Content -LiteralPath (Join-Path $weaselSource "WeaselSetup\imesetup.cpp") -Raw
$stagedGlobals = Get-Content -LiteralPath (Join-Path $weaselSource "WeaselTSF\Globals.cpp") -Raw
$stagedServer = Get-Content -LiteralPath (Join-Path $weaselSource "WeaselServer\WeaselServerApp.cpp") -Raw
$requiredIdentities = @(
    [pscustomobject]@{ Text = $stagedConstants; Marker = '#define WEASEL_CODE_NAME "YunPin"' }
    [pscustomobject]@{ Text = $stagedConstants; Marker = 'Software\\YunPin\\IME' }
    [pscustomobject]@{ Text = $stagedSetup; Marker = 'std::wstring srcFileName = L"yunpin"' }
    [pscustomobject]@{ Text = $stagedSetup; Marker = 'sysPath + L"\\yunpin.dll"' }
    [pscustomobject]@{ Text = $stagedGlobals; Marker = '0x1c4fbfe5' }
    [pscustomobject]@{ Text = $stagedServer; Marker = 'YunPinDeployer.exe' }
)
foreach ($requiredIdentity in $requiredIdentities) {
    if (-not ([string]$requiredIdentity.Text).Contains([string]$requiredIdentity.Marker)) {
        throw "Generated Weasel source is missing YunPin patch marker: $($requiredIdentity.Marker)"
    }
}
if ($stagedServer.Contains('win_sparkle_init')) {
    throw "Generated Weasel source still enables WinSparkle"
}

$generatedIcon = Join-Path $weaselSource "resource\yunpin.ico"
& (Join-Path $scriptRoot "New-PreviewIcon.ps1") -Destination $generatedIcon
Copy-Item -LiteralPath $generatedIcon -Destination (Join-Path $weaselSource "resource\weasel.ico") -Force
Copy-Item -LiteralPath $generatedIcon -Destination (Join-Path $weaselSource "WeaselSetup\WeaselSetup.ico") -Force

$stagedLibrime = Join-Path $weaselSource "librime"
Export-GitTree -Checkout $librimeCheckout -Destination $stagedLibrime -ScratchRoot $scratchRoot
foreach ($dependency in $lock.librime.dependencies.PSObject.Properties) {
    $dependencyCheckout = Join-Path $librimeCheckout $dependency.Name
    $dependencyDestination = Join-Path $stagedLibrime $dependency.Name
    Export-GitTree -Checkout $dependencyCheckout -Destination $dependencyDestination -ScratchRoot $scratchRoot
}

$previousGitCeilingDirectories = $env:GIT_CEILING_DIRECTORIES
try {
    $env:GIT_CEILING_DIRECTORIES = $OutputRoot
    foreach ($patchEntry in $lock.librime.patches) {
        $patchPath = Join-Path $repoRoot $patchEntry.path
        Push-Location $stagedLibrime
        try {
            Invoke-Checked -FilePath "git" -ArgumentList @(
                "apply", "--check", "--whitespace=error-all", $patchPath
            )
            Invoke-Checked -FilePath "git" -ArgumentList @(
                "apply", "--whitespace=error-all", $patchPath
            )
        } finally {
            Pop-Location
        }
    }
} finally {
    if ($null -eq $previousGitCeilingDirectories) {
        Remove-Item Env:GIT_CEILING_DIRECTORIES -ErrorAction SilentlyContinue
    } else {
        $env:GIT_CEILING_DIRECTORIES = $previousGitCeilingDirectories
    }
}
$patchedTranslator = Get-Content -LiteralPath (Join-Path $stagedLibrime "src\rime\gear\script_translator.cc") -Raw
if (-not $patchedTranslator.Contains("corrector_component")) {
    throw "Staged librime does not expose the locked corrector component selector"
}

$stagedPlugin = Join-Path $stagedLibrime "plugins\librime-yunpin"
Reset-GeneratedDirectory -Path $stagedPlugin -AllowedParent (Join-Path $stagedLibrime "plugins")
Get-ChildItem -LiteralPath $pluginRoot -Force | ForEach-Object {
    Copy-Item -LiteralPath $_.FullName -Destination $stagedPlugin -Recurse -Force
}
$stagedEngine = Join-Path $stagedPlugin "engine"
New-Item -ItemType Directory -Path $stagedEngine -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $engineRoot "include") -Destination $stagedEngine -Recurse -Force
Copy-Item -LiteralPath (Join-Path $engineRoot "src") -Destination $stagedEngine -Recurse -Force

# Windows has no production external-plugin loader. Fetch the same immutable
# octagram source used by macOS, verify both source and license identities, and
# stage it as a second merged librime plugin for both generated architectures.
$octagramArchive = Join-Path $cacheRoot ([string]$lock.librimeOctagram.archiveName)
$bundledOctagramArchive = Join-Path $repoRoot ("sources\" + [string]$lock.librimeOctagram.archiveName)
if (-not (Test-Path $octagramArchive -PathType Leaf) -and
    (Test-Path $bundledOctagramArchive -PathType Leaf)) {
    $bundledOctagramHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $bundledOctagramArchive).Hash.ToLowerInvariant()
    if ($bundledOctagramHash -ne ([string]$lock.librimeOctagram.sha256).ToLowerInvariant()) {
        throw "Bundled librime-octagram source archive does not match the Windows lock"
    }
    Copy-Item -LiteralPath $bundledOctagramArchive -Destination $octagramArchive -Force
}
Get-VerifiedDownload -Uri $lock.librimeOctagram.url `
    -Sha256 $lock.librimeOctagram.sha256 -Destination $octagramArchive
$stagedOctagram = Join-Path $stagedLibrime "plugins\octagram"
Reset-GeneratedDirectory -Path $stagedOctagram -AllowedParent (Join-Path $stagedLibrime "plugins")
Invoke-Checked -FilePath "tar.exe" -ArgumentList @(
    "-xzf", $octagramArchive, "-C", $stagedOctagram, "--strip-components=1"
)
foreach ($requiredOctagramSource in @(
    "CMakeLists.txt", "LICENSE", "src\gram_encoding.cc", "src\grammar_module.cc"
)) {
    if (-not (Test-Path (Join-Path $stagedOctagram $requiredOctagramSource) -PathType Leaf)) {
        throw "librime-octagram source archive is incomplete: $requiredOctagramSource"
    }
}
$octagramLicenseHash = (Get-FileHash -Algorithm SHA256 `
    -LiteralPath (Join-Path $stagedOctagram "LICENSE")).Hash.ToLowerInvariant()
if ($octagramLicenseHash -ne ([string]$lock.librimeOctagram.licenseSha256).ToLowerInvariant()) {
    throw "librime-octagram license does not match the Windows lock"
}
$octagramEncoding = Get-Content -LiteralPath (Join-Path $stagedOctagram "src\gram_encoding.cc") -Raw
if (-not $octagramEncoding.Contains("u <<= 7;")) {
    throw "librime-octagram source lacks the locked multi-byte encoder fix"
}
[IO.File]::WriteAllText(
    (Join-Path $stagedOctagram ".yunpin-source-commit"),
    ([string]$lock.librimeOctagram.commit + "`r`n"),
    [Text.Encoding]::ASCII
)

$sevenZip = (Get-Command "7z.exe" -ErrorAction Stop).Source
$boostArchive = Join-Path $cacheRoot "boost_1_84_0.7z"
$bundledBoostArchive = Join-Path $repoRoot "sources\boost_1_84_0.7z"
if (-not (Test-Path $boostArchive -PathType Leaf) -and (Test-Path $bundledBoostArchive -PathType Leaf)) {
    $bundledBoostHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $bundledBoostArchive).Hash.ToLowerInvariant()
    if ($bundledBoostHash -ne ([string]$lock.boost.sha256).ToLowerInvariant()) {
        throw "Bundled Boost source archive does not match the Windows lock"
    }
    Copy-Item -LiteralPath $bundledBoostArchive -Destination $boostArchive -Force
}
Get-VerifiedDownload -Uri $lock.boost.url -Sha256 $lock.boost.sha256 -Destination $boostArchive
$boostRoot = Join-Path $depsRoot "boost_1_84_0"
if (-not (Test-Path (Join-Path $boostRoot "boost"))) {
    Invoke-Checked -FilePath $sevenZip -ArgumentList @(
        "x", $boostArchive, "-o$depsRoot", "-y"
    )
}
if (-not (Test-Path (Join-Path $boostRoot "boost"))) {
    throw "Boost extraction did not create $boostRoot"
}

$vcInstallDir = [string]$env:VCINSTALLDIR
if ([string]::IsNullOrWhiteSpace($vcInstallDir)) {
    throw "VCINSTALLDIR is unavailable; run from a Visual Studio 2022 developer environment"
}
$vcvarsAll = Join-Path $vcInstallDir "Auxiliary\Build\vcvarsall.bat"
if (-not (Test-Path $vcvarsAll -PathType Leaf)) {
    throw "Visual Studio setup script is missing: $vcvarsAll"
}
$boostUserConfig = Join-Path $scratchRoot "boost-msvc-user-config.jam"
$vcvarsAllForJam = $vcvarsAll.Replace("\", "/")
$boostUserConfigText = "using msvc : 14.3 : : <setup>`"$vcvarsAllForJam`" ;`r`n"
[IO.File]::WriteAllText($boostUserConfig, $boostUserConfigText, [Text.Encoding]::ASCII)
$boostBuildOptions = "--user-config=`"$boostUserConfig`""

$envFile = Join-Path $weaselSource "env.bat"
$envLines = @(
    "@echo off",
    "set BOOST_ROOT=$boostRoot",
    "set BOOST_COMPILED_LIBS=$boostBuildOptions",
    "set BJAM_TOOLSET=msvc-14.3",
    'set CMAKE_GENERATOR="Visual Studio 17 2022"',
    "set PLATFORM_TOOLSET=v143"
)
[IO.File]::WriteAllLines($envFile, $envLines, [Text.Encoding]::ASCII)

$env:BOOST_ROOT = $boostRoot
$env:BOOST_COMPILED_LIBS = $boostBuildOptions
$env:BJAM_TOOLSET = "msvc-14.3"
$env:CMAKE_GENERATOR = '"Visual Studio 17 2022"'
$env:PLATFORM_TOOLSET = "v143"
$env:RIME_PLUGINS = "librime-yunpin octagram"
$env:VERSION_MAJOR = "0"
$env:VERSION_MINOR = "17"
$env:VERSION_PATCH = "4"
$env:WEASEL_VERSION = "0.17.4"
$env:WEASEL_BUILD = "0"
$env:PRODUCT_VERSION = "0.17.4.0-yunpin-dev"
$env:FILE_VERSION = "0.17.4.0"
$env:RELEASE_BUILD = "1"

$boostMarker = Join-Path $boostRoot ".yunpin-vc143-x86-x64.complete"
$boostArtifacts = @(
    (Join-Path $boostRoot "stage\lib\libboost_wserialization-vc143-mt-s-x32-1_84.lib"),
    (Join-Path $boostRoot "stage\lib\libboost_wserialization-vc143-mt-s-x64-1_84.lib")
)
$missingBoostArtifacts = @($boostArtifacts | Where-Object { -not (Test-Path $_ -PathType Leaf) })
if ((Test-Path $boostMarker -PathType Leaf) -and $missingBoostArtifacts.Count -ne 0) {
    Remove-Item -LiteralPath $boostMarker -Force
}
Push-Location $weaselSource
try {
    if (-not (Test-Path $boostMarker)) {
        # Boost caches failed compiler feature probes in bin.v2.  A corrected
        # VS setup must not reuse those negative results from an interrupted
        # build, or the x86 wide-serialization library remains silently absent.
        Reset-GeneratedDirectory -Path (Join-Path $boostRoot "bin.v2") -AllowedParent $boostRoot
        Reset-GeneratedDirectory -Path (Join-Path $boostRoot "stage") -AllowedParent $boostRoot
        Invoke-Checked -FilePath "cmd.exe" -ArgumentList @("/d", "/c", "call build.bat boost")
        $missingBoostArtifacts = @($boostArtifacts | Where-Object { -not (Test-Path $_ -PathType Leaf) })
        if ($missingBoostArtifacts.Count -ne 0) {
            throw "Boost build did not produce required x86/x64 libraries: $($missingBoostArtifacts -join ', ')"
        }
        [IO.File]::WriteAllText($boostMarker, "boost 1.84.0 vc143 x86+x64`r`n", [Text.Encoding]::ASCII)
    }

    Invoke-Checked -FilePath "cmd.exe" -ArgumentList @("/d", "/c", "call build.bat rime")
    foreach ($requiredRimeOutput in @(
        "include\rime_api.h",
        "include\rime_levers_api.h",
        "lib64\rime.lib",
        "lib\rime.lib",
        "output\rime.dll",
        "output\Win32\rime.dll"
    )) {
        if (-not (Test-Path (Join-Path $weaselSource $requiredRimeOutput) -PathType Leaf)) {
            throw "Merged librime build did not produce $requiredRimeOutput; inspect the preceding compiler error"
        }
    }
    foreach ($architecture in @("x64", "Win32")) {
        $generatedProject = Join-Path $stagedLibrime ("build_" + $architecture + "\src\rime.vcxproj")
        if (-not (Test-Path $generatedProject -PathType Leaf)) {
            throw "Merged librime project is missing for $architecture"
        }
        $projectText = Get-Content -LiteralPath $generatedProject -Raw
        foreach ($module in @("yunpin", "octagram")) {
            if ($projectText -notmatch ('Q\(' + $module + '\)')) {
                throw "Merged $module module is absent from the $architecture librime project"
            }
        }
        $octagramProject = Join-Path $stagedLibrime `
            ("build_" + $architecture + "\plugins\octagram\rime-octagram-objs.vcxproj")
        if (-not (Test-Path $octagramProject -PathType Leaf)) {
            throw "Merged octagram object project is missing for $architecture"
        }
        $octagramProjectText = Get-Content -LiteralPath $octagramProject -Raw
        foreach ($octagramSource in @("gram_encoding.cc", "grammar_module.cc")) {
            if (-not $octagramProjectText.Contains($octagramSource)) {
                throw "Merged octagram $architecture project omits $octagramSource"
            }
        }
    }

    # Execute a C-API registration probe against each exact rime.dll. This is
    # stronger than process survival or project generation: the default module
    # set must load octagram and its grammar module in both x64 and x86 images.
    $moduleProbeSource = Join-Path $repoRoot "platform\windows\tests\rime-module-probe"
    foreach ($probeTarget in @(
        [pscustomobject]@{
            Name = "x64"
            Platform = "x64"
            ImportLibrary = (Join-Path $weaselSource "lib64\rime.lib")
            RuntimeDirectory = (Join-Path $weaselSource "output")
        }
        [pscustomobject]@{
            Name = "x86"
            Platform = "Win32"
            ImportLibrary = (Join-Path $weaselSource "lib\rime.lib")
            RuntimeDirectory = (Join-Path $weaselSource "output\Win32")
        }
    )) {
        $probeBuild = Join-Path $scratchRoot ("rime-module-probe-" + $probeTarget.Name)
        Reset-GeneratedDirectory -Path $probeBuild -AllowedParent $scratchRoot
        Invoke-Checked -FilePath "cmake.exe" -ArgumentList @(
            "-S", $moduleProbeSource,
            "-B", $probeBuild,
            "-G", "Visual Studio 17 2022",
            "-A", $probeTarget.Platform,
            "-DRIME_INCLUDE_DIR=$(Join-Path $weaselSource 'include')",
            "-DRIME_IMPORT_LIBRARY=$($probeTarget.ImportLibrary)"
        )
        Invoke-Checked -FilePath "cmake.exe" -ArgumentList @(
            "--build", $probeBuild, "--config", "Release", "--parallel"
        )
        $probeExecutable = Join-Path $probeBuild "Release\yunpin-rime-module-probe.exe"
        if (-not (Test-Path $probeExecutable -PathType Leaf)) {
            throw "Rime module probe executable is missing for $($probeTarget.Name)"
        }
        $probeShared = Join-Path $probeBuild "shared"
        $probeUser = Join-Path $probeBuild "user"
        New-Item -ItemType Directory -Path $probeShared, $probeUser -Force | Out-Null
        $previousPath = $env:PATH
        try {
            $env:PATH = $probeTarget.RuntimeDirectory + ";" + $previousPath
            $expectedRimeDll = Join-Path $probeTarget.RuntimeDirectory "rime.dll"
            $probeOutput = @(& $probeExecutable $probeShared $probeUser $expectedRimeDll 2>&1)
            $probeExitCode = $LASTEXITCODE
        } finally {
            $env:PATH = $previousPath
        }
        if ($probeExitCode -ne 0) {
            throw "Rime module probe failed for $($probeTarget.Name): $($probeOutput -join '; ')"
        }
        foreach ($expectedProbeRow in @(
            "octagram_module_registered=true",
            "grammar_module_registered=true",
            "yunpin_module_registered=true",
            "rime_runtime_identity_exact=true"
        )) {
            if ($probeOutput -notcontains $expectedProbeRow) {
                throw "Rime module probe omitted $expectedProbeRow for $($probeTarget.Name)"
            }
        }
    }

    # Compile the quality probe against the exact x64 import library. Packaging
    # executes it later against the completed rime-data directory and the
    # runtime rime.dll, after the locked model is copied into place.
    $qualityProbeSource = Join-Path $repoRoot "platform\windows\tests\rime-grammar-quality-probe"
    $qualityProbeBuild = Join-Path $scratchRoot "rime-grammar-quality-probe-x64"
    Reset-GeneratedDirectory -Path $qualityProbeBuild -AllowedParent $scratchRoot
    Invoke-Checked -FilePath "cmake.exe" -ArgumentList @(
        "-S", $qualityProbeSource,
        "-B", $qualityProbeBuild,
        "-G", "Visual Studio 17 2022",
        "-A", "x64",
        "-DRIME_INCLUDE_DIR=$(Join-Path $weaselSource 'include')",
        "-DRIME_IMPORT_LIBRARY=$(Join-Path $weaselSource 'lib64\rime.lib')"
    )
    Invoke-Checked -FilePath "cmake.exe" -ArgumentList @(
        "--build", $qualityProbeBuild, "--config", "Release", "--parallel"
    )
    if (-not (Test-Path `
        (Join-Path $qualityProbeBuild "Release\yunpin-rime-grammar-quality-probe.exe") `
        -PathType Leaf)) {
        throw "Rime grammar quality probe executable is missing"
    }
    Invoke-Checked -FilePath "cmd.exe" -ArgumentList @("/d", "/c", "call build.bat opencc")

    $propertyNames = @(
        "BOOST_ROOT", "PLATFORM_TOOLSET", "VERSION_MAJOR", "VERSION_MINOR",
        "VERSION_PATCH", "PRODUCT_VERSION", "FILE_VERSION"
    )
    Invoke-Checked -FilePath "cscript.exe" -ArgumentList (@(
        "//nologo", "render.js", "weasel.props"
    ) + $propertyNames)
    # WeaselIME and WeaselTSF both emit weaselx64.lib/.exp while linking.
    # Keep project scheduling serial so those shared outputs cannot race; the
    # projects still retain their compiler-level /MP parallelism.
    Invoke-Checked -FilePath "msbuild.exe" -ArgumentList @(
        "weasel.sln", "/m:1", "/t:Build", "/p:Configuration=Release",
        "/p:Platform=x64", "/verbosity:minimal"
    )
    Invoke-Checked -FilePath "msbuild.exe" -ArgumentList @(
        "weasel.sln", "/m:1", "/t:Build", "/p:Configuration=Release",
        "/p:Platform=Win32", "/verbosity:minimal"
    )
} finally {
    Pop-Location
}

foreach ($requiredOutput in @(
    "output\weasel.dll",
    "output\weaselx64.dll",
    "output\WeaselServer.exe",
    "output\WeaselDeployer.exe",
    "output\WeaselSetup.exe",
    "output\rime.dll",
    "output\Win32\rime.dll"
)) {
    $path = Join-Path $weaselSource $requiredOutput
    if (-not (Test-Path $path)) {
        throw "Expected Windows build output is missing: $requiredOutput"
    }
}

foreach ($rimeOutput in @("output\rime.dll", "output\Win32\rime.dll")) {
    $rimePath = Join-Path $weaselSource $rimeOutput
    $exports = & "dumpbin.exe" /nologo /exports $rimePath 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "dumpbin failed while checking native spooler export in ${rimeOutput}"
    }
    if (($exports -join "`n") -notmatch 'YunPinStartNativeSelectionSpoolerV1') {
        throw "Merged librime does not export the YunPin native spooler: ${rimeOutput}"
    }
}

$setupBinaryText = [Text.Encoding]::Unicode.GetString(
    [IO.File]::ReadAllBytes((Join-Path $weaselSource "output\WeaselSetup.exe"))
)
if (-not $setupBinaryText.Contains("yunpin.dll") -or $setupBinaryText.Contains("weasel.dll")) {
    throw "Built setup binary does not carry the isolated YunPin runtime identity"
}
$serverBinaryText = [Text.Encoding]::Unicode.GetString(
    [IO.File]::ReadAllBytes((Join-Path $weaselSource "output\WeaselServer.exe"))
)
if (-not $serverBinaryText.Contains("YunPinDeployer.exe") -or $serverBinaryText.Contains("WeaselDeployer.exe")) {
    throw "Built server binary does not carry the isolated YunPin runtime identity"
}

& (Join-Path $scriptRoot "Build-SyncAgents.ps1") -OutputRoot $OutputRoot

if (-not $SkipPackage) {
    & (Join-Path $scriptRoot "Package-Preview.ps1") -OutputRoot $OutputRoot -WeaselSource $weaselSource
}

Write-Host "YunPin Windows development preview build completed: $OutputRoot"
