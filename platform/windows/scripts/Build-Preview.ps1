# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [string]$OutputRoot = "",
    [switch]$SkipPackage
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

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
    Invoke-WebRequest -Uri $Uri -OutFile $Destination -UseBasicParsing
    $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $Destination).Hash.ToLowerInvariant()
    if ($observed -ne $Sha256.ToLowerInvariant()) {
        Remove-Item -LiteralPath $Destination -Force
        throw "SHA-256 mismatch for $Uri"
    }
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Build-Preview.ps1 must run on Windows"
}
if (-not [Environment]::Is64BitOperatingSystem) {
    throw "The preview build requires a 64-bit Windows host"
}

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = [IO.Path]::GetFullPath((Join-Path $scriptRoot "..\..\.."))
$lockPath = Join-Path $repoRoot "platform\windows\dependencies.lock.json"
$lock = Get-Content -LiteralPath $lockPath -Raw | ConvertFrom-Json

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

foreach ($patchEntry in $lock.weasel.patches) {
    $patchPath = Join-Path $repoRoot $patchEntry.path
    $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $patchPath).Hash.ToLowerInvariant()
    if ($observed -ne ([string]$patchEntry.sha256).ToLowerInvariant()) {
        throw "Weasel patch hash mismatch: $($patchEntry.path)"
    }
}

Reset-GeneratedDirectory -Path $weaselSource -AllowedParent $OutputRoot
Export-GitTree -Checkout $weaselCheckout -Destination $weaselSource -ScratchRoot $scratchRoot

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

$stagedPlugin = Join-Path $stagedLibrime "plugins\librime-yunpin"
Reset-GeneratedDirectory -Path $stagedPlugin -AllowedParent (Join-Path $stagedLibrime "plugins")
Get-ChildItem -LiteralPath $pluginRoot -Force | ForEach-Object {
    Copy-Item -LiteralPath $_.FullName -Destination $stagedPlugin -Recurse -Force
}
$stagedEngine = Join-Path $stagedPlugin "engine"
New-Item -ItemType Directory -Path $stagedEngine -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $engineRoot "include") -Destination $stagedEngine -Recurse -Force
Copy-Item -LiteralPath (Join-Path $engineRoot "src") -Destination $stagedEngine -Recurse -Force

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

$envFile = Join-Path $weaselSource "env.bat"
$envLines = @(
    "@echo off",
    "set BOOST_ROOT=$boostRoot",
    "set BJAM_TOOLSET=msvc-14.3",
    'set CMAKE_GENERATOR="Visual Studio 17 2022"',
    "set PLATFORM_TOOLSET=v143"
)
[IO.File]::WriteAllLines($envFile, $envLines, [Text.Encoding]::ASCII)

$env:BOOST_ROOT = $boostRoot
$env:BJAM_TOOLSET = "msvc-14.3"
$env:CMAKE_GENERATOR = '"Visual Studio 17 2022"'
$env:PLATFORM_TOOLSET = "v143"
$env:RIME_PLUGINS = "librime-yunpin"
$env:VERSION_MAJOR = "0"
$env:VERSION_MINOR = "17"
$env:VERSION_PATCH = "4"
$env:WEASEL_VERSION = "0.17.4"
$env:WEASEL_BUILD = "0"
$env:PRODUCT_VERSION = "0.17.4.0-yunpin-dev"
$env:FILE_VERSION = "0.17.4.0"
$env:RELEASE_BUILD = "1"

$boostMarker = Join-Path $boostRoot ".yunpin-vc143-x86-x64.complete"
Push-Location $weaselSource
try {
    if (-not (Test-Path $boostMarker)) {
        Invoke-Checked -FilePath "cmd.exe" -ArgumentList @("/d", "/c", "call build.bat boost")
        [IO.File]::WriteAllText($boostMarker, "boost 1.84.0 vc143 x86+x64`r`n", [Text.Encoding]::ASCII)
    }

    Invoke-Checked -FilePath "cmd.exe" -ArgumentList @("/d", "/c", "call build.bat rime")
    foreach ($architecture in @("x64", "Win32")) {
        $generatedProject = Join-Path $stagedLibrime ("build_" + $architecture + "\src\rime.vcxproj")
        if (-not (Test-Path $generatedProject -PathType Leaf)) {
            throw "Merged librime project is missing for $architecture"
        }
        $projectText = Get-Content -LiteralPath $generatedProject -Raw
        if ($projectText -notmatch 'Q\(yunpin\)') {
            throw "Merged yunpin module is absent from the $architecture librime project"
        }
    }
    Invoke-Checked -FilePath "cmd.exe" -ArgumentList @("/d", "/c", "call build.bat opencc")

    $propertyNames = @(
        "BOOST_ROOT", "PLATFORM_TOOLSET", "VERSION_MAJOR", "VERSION_MINOR",
        "VERSION_PATCH", "PRODUCT_VERSION", "FILE_VERSION"
    )
    Invoke-Checked -FilePath "cscript.exe" -ArgumentList (@(
        "//nologo", "render.js", "weasel.props"
    ) + $propertyNames)
    Invoke-Checked -FilePath "msbuild.exe" -ArgumentList @(
        "weasel.sln", "/m", "/t:Build", "/p:Configuration=Release",
        "/p:Platform=x64", "/verbosity:minimal"
    )
    Invoke-Checked -FilePath "msbuild.exe" -ArgumentList @(
        "weasel.sln", "/m", "/t:Build", "/p:Configuration=Release",
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

if (-not $SkipPackage) {
    & (Join-Path $scriptRoot "Package-Preview.ps1") -OutputRoot $OutputRoot -WeaselSource $weaselSource
}

Write-Host "YunPin Windows development preview build completed: $OutputRoot"
