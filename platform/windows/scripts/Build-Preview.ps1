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
    # The generated tree normally lives below the repository in an ignored
    # build directory.  Without a ceiling, git apply discovers the parent
    # repository and can return success while silently ignoring these files.
    # Force standalone patch mode so the generated Weasel tree is the target.
    $env:GIT_CEILING_DIRECTORIES = $repoRoot
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
    $env:GIT_CEILING_DIRECTORIES = $repoRoot
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
