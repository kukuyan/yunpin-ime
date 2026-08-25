# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$OutputRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-GoBuild {
    param(
        [Parameter(Mandatory = $true)][string]$AgentRoot,
        [Parameter(Mandatory = $true)][string]$Output,
        [string]$BuildTag = "",
        [string]$Package = "./cmd/yunpin-sync-agent",
        # Go links console-subsystem binaries by default. A scheduled task that
        # starts a long-running console binary in the user's interactive session
        # gets a console window allocated for it, and it stays for the life of
        # the process rather than flashing. The resident is therefore linked for
        # the GUI subsystem; the interactive binary writes JSON to stdout and
        # must stay console-subsystem.
        [switch]$WindowsGui
    )
    $arguments = @("build", "-trimpath", "-buildvcs=false")
    if (-not [string]::IsNullOrWhiteSpace($BuildTag)) {
        $arguments += ("-tags=" + $BuildTag)
    }
    if ($WindowsGui) {
        $arguments += @("-ldflags", "-H=windowsgui")
    }
    $arguments += @("-o", $Output, $Package)
    Push-Location $AgentRoot
    try {
        & go @arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Go sync-agent build failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

function Invoke-AgentCapture {
    param(
        [Parameter(Mandatory = $true)][string]$Executable,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $Executable
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.Arguments = $Arguments -join " "
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) { throw "Failed to start sync agent: $Executable" }
        $stdout = $process.StandardOutput.ReadToEnd()
        $stderr = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        return [pscustomobject]@{
            Output = (($stdout + $stderr).Trim())
            ExitCode = $process.ExitCode
        }
    } finally {
        $process.Dispose()
    }
}

function Reset-PrivateE2EOutput {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$AllowedParent,
        [Parameter(Mandatory = $true)][bool]$Create
    )

    $fullPath = [IO.Path]::GetFullPath($Path)
    $fullParent = [IO.Path]::GetFullPath($AllowedParent).TrimEnd("\")
    $prefix = $fullParent + "\"
    if (-not $fullPath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Private E2E output path escapes its generated parent"
    }
    if (-not (Test-Path -LiteralPath $fullParent -PathType Container)) {
        throw "Private E2E generated parent is missing"
    }
    $parentItem = Get-Item -LiteralPath $fullParent -Force
    if (($parentItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Private E2E generated parent is a reparse point"
    }
    $current = $fullParent
    $relative = $fullPath.Substring($prefix.Length)
    $components = @($relative.Split([char]'\') | Where-Object {
        -not [string]::IsNullOrWhiteSpace($_)
    })
    for ($index = 0; $index -lt $components.Count; $index++) {
        $current = Join-Path $current $components[$index]
        if (-not (Test-Path -LiteralPath $current)) {
            break
        }
        $componentItem = Get-Item -LiteralPath $current -Force
        if (($componentItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Private E2E output path contains a reparse point"
        }
        if ($index -lt ($components.Count - 1) -and -not $componentItem.PSIsContainer) {
            throw "Private E2E output path contains a non-directory component"
        }
    }
    $markerName = ".yunpin-private-e2e-generated"
    if (Test-Path -LiteralPath $fullPath) {
        $root = Get-Item -LiteralPath $fullPath -Force
        if (-not $root.PSIsContainer -or
            ($root.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Private E2E output root is not a normal directory"
        }
        $marker = Join-Path $fullPath $markerName
        if (-not (Test-Path -LiteralPath $marker -PathType Leaf) -or
            (Get-Content -LiteralPath $marker -Raw) -cne "YunPin private E2E generated output`n") {
            throw "Refusing to reset an unmarked private E2E output root"
        }
        foreach ($item in @(Get-ChildItem -LiteralPath $fullPath -Force -Recurse)) {
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing a nested reparse point in private E2E output"
            }
            $relative = $item.FullName.Substring($fullPath.TrimEnd("\").Length + 1).Replace("\", "/")
            if ($item.PSIsContainer) {
                if ($relative -cne "licenses") {
                    throw "Unknown directory in private E2E generated output: $relative"
                }
            } elseif ($relative -cne $markerName -and
                $relative -cne "yunpin-sync-agent.exe" -and
                $relative -cne "BUILD-METADATA.json" -and
                $relative -cne "SHA256SUMS" -and
                $relative -cne "Private-Snapshot-E2E.Common.ps1" -and
                $relative -cne "Enable-Private-Snapshot-E2E.ps1" -and
                $relative -cne "Disable-Private-Snapshot-E2E.ps1" -and
                $relative -cne "README.md" -and
                $relative -notmatch '^licenses/[A-Za-z0-9_.@-]+$') {
                throw "Unknown file in private E2E generated output: $relative"
            }
        }
        Remove-Item -LiteralPath $fullPath -Recurse -Force
    }
    if ($Create) {
        New-Item -ItemType Directory -Path $fullPath -Force | Out-Null
        [IO.File]::WriteAllText(
            (Join-Path $fullPath $markerName),
            "YunPin private E2E generated output`n",
            [Text.UTF8Encoding]::new($false)
        )
    }
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Build-SyncAgents.ps1 must run on Windows"
}
if (-not [Environment]::Is64BitOperatingSystem) {
    throw "The YunPin sync agent requires a 64-bit Windows build host"
}
if ($null -eq (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is required to build the YunPin sync agent"
}

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = [IO.Path]::GetFullPath((Join-Path $scriptRoot "..\..\.."))
$agentRoot = Join-Path $repoRoot "desktopagent"
$replayRoot = Join-Path $repoRoot "replaylab"
$e2eSourceRoot = Join-Path $repoRoot "platform\windows\e2e"
$sourceMetadataPath = Join-Path $repoRoot "BUILD-SOURCE-METADATA.json"
$isPublicSourceExport = -not (Test-Path (Join-Path $repoRoot ".git")) -and
    (Test-Path -LiteralPath $sourceMetadataPath -PathType Leaf)
$hasPrivateE2ESupport = Test-Path -LiteralPath $e2eSourceRoot -PathType Container
if (-not $hasPrivateE2ESupport -and -not $isPublicSourceExport) {
    throw "Private Windows E2E support is missing from a repository checkout"
}
$publicOverlayPath = Join-Path $repoRoot "platform\windows\rime\rime_ice.custom.yaml"
$OutputRoot = [IO.Path]::GetFullPath($OutputRoot)
$publicRoot = Join-Path $OutputRoot "desktopagent\public"
$privateRoot = Join-Path $OutputRoot "e2e-private\windows"
New-Item -ItemType Directory -Path $OutputRoot -Force | Out-Null
Reset-PrivateE2EOutput -Path $privateRoot -AllowedParent $OutputRoot `
    -Create $hasPrivateE2ESupport
New-Item -ItemType Directory -Path $publicRoot -Force | Out-Null
$publicBinary = Join-Path $publicRoot "yunpin-sync-agent.exe"
# The windowless background process the scheduled task runs. Separate from the
# interactive binary only because of the subsystem it is linked for.
$residentBinary = Join-Path $publicRoot "yunpin-sync-resident.exe"
# The tray launches the same public command package through a GUI-subsystem
# image, so opening Settings never allocates a PowerShell/console window.
$settingsBinary = Join-Path $publicRoot "yunpin-settings.exe"
$replayBinary = Join-Path $publicRoot "yunpin-replay-lab.exe"
$privateBinary = Join-Path $privateRoot "yunpin-sync-agent.exe"
Remove-Item -LiteralPath $publicBinary, $residentBinary, $settingsBinary, $replayBinary -Force -ErrorAction SilentlyContinue
if (-not (Test-Path -LiteralPath $publicOverlayPath -PathType Leaf)) {
    throw "Same-run public Windows overlay is missing"
}

Push-Location $agentRoot
try {
    & go mod verify
    if ($LASTEXITCODE -ne 0) { throw "Go module verification failed" }
} finally {
    Pop-Location
}
Push-Location $replayRoot
try {
    & go mod verify
    if ($LASTEXITCODE -ne 0) { throw "Replay Lab Go module verification failed" }
} finally {
    Pop-Location
}

$savedEnvironment = @{
    CGO_ENABLED = $env:CGO_ENABLED
    GOOS = $env:GOOS
    GOARCH = $env:GOARCH
}
try {
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    Invoke-GoBuild -AgentRoot $agentRoot -Output $publicBinary
    Invoke-GoBuild -AgentRoot $agentRoot -Output $settingsBinary -WindowsGui
    Invoke-GoBuild -AgentRoot $agentRoot -Output $residentBinary `
        -Package "./cmd/yunpin-sync-resident" -WindowsGui
    Invoke-GoBuild -AgentRoot $replayRoot -Output $replayBinary `
        -Package "./cmd/yunpin-replay-lab"
    if ($hasPrivateE2ESupport) {
        Invoke-GoBuild -AgentRoot $agentRoot -Output $privateBinary -BuildTag "yunpin_pairing_private"
    }
} finally {
    foreach ($name in $savedEnvironment.Keys) {
        if ($null -eq $savedEnvironment[$name]) {
            Remove-Item ("Env:" + $name) -ErrorAction SilentlyContinue
        } else {
            Set-Item ("Env:" + $name) $savedEnvironment[$name]
        }
    }
}

$probe = Invoke-AgentCapture -Executable $publicBinary -Arguments @("install-probe")
if ($probe.ExitCode -ne 0) {
    throw "Public sync agent install-probe failed"
}
$publicPrivate = Invoke-AgentCapture -Executable $publicBinary -Arguments @("pairing-invite")
if ($publicPrivate.ExitCode -eq 0 -or $publicPrivate.Output -cne "yunpin-sync-agent: unknown command") {
    throw "Public sync agent exposes a private pairing command"
}
$publicBaseline = Invoke-AgentCapture -Executable $publicBinary -Arguments @("e2e-init-empty-baseline")
if ($publicBaseline.ExitCode -eq 0 -or
    $publicBaseline.Output -cne "yunpin-sync-agent: unknown command") {
    throw "Public sync agent exposes the private empty-baseline E2E command"
}
$replayUsage = Invoke-AgentCapture -Executable $replayBinary -Arguments @("help")
if ($replayUsage.ExitCode -eq 0 -or
    -not $replayUsage.Output.StartsWith("error: usage: yunpin-replay-lab")) {
    throw "Replay Lab CLI usage probe failed"
}

$licenseRoot = Join-Path $OutputRoot "desktopagent\licenses"
& python (Join-Path $repoRoot "scripts\package_go_licenses.py") `
    --go-module $agentRoot --output $licenseRoot
if ($LASTEXITCODE -ne 0) { throw "Sync-agent license bundle generation failed" }
$replayLicenseRoot = Join-Path $OutputRoot "replaylab\licenses"
& python (Join-Path $repoRoot "scripts\package_go_licenses.py") `
    --go-module $replayRoot --go-package ./cmd/yunpin-replay-lab `
    --artifact yunpin-replay-lab --output $replayLicenseRoot
if ($LASTEXITCODE -ne 0) { throw "Replay Lab license bundle generation failed" }
if ($hasPrivateE2ESupport) {
    $privateGate = Invoke-AgentCapture -Executable $privateBinary -Arguments @("pairing-invite")
    if ($privateGate.ExitCode -eq 0 -or
        -not $privateGate.Output.Contains("pairing-invite requires --confirm-display-invitation")) {
        throw "Private E2E sync agent does not expose the confirmation-gated pairing command"
    }
    $privateBaseline = Invoke-AgentCapture -Executable $privateBinary -Arguments @("e2e-init-empty-baseline")
    if ($privateBaseline.ExitCode -eq 0 -or
        -not $privateBaseline.Output.Contains("e2e-init-empty-baseline requires --confirm-create-empty-baseline")) {
        throw "Private E2E sync agent does not expose the confirmation-gated empty-baseline command"
    }
    Copy-Item -LiteralPath $licenseRoot -Destination (Join-Path $privateRoot "licenses") -Recurse -Force
    foreach ($privateSupportFile in @(
        "Private-Snapshot-E2E.Common.ps1",
        "Enable-Private-Snapshot-E2E.ps1",
        "Disable-Private-Snapshot-E2E.ps1",
        "README.md"
    )) {
        $source = Join-Path $e2eSourceRoot $privateSupportFile
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
            throw "Private Windows E2E support file is missing: $source"
        }
        Copy-Item -LiteralPath $source -Destination (Join-Path $privateRoot $privateSupportFile) -Force
    }
}

$repoCommit = ""
if (Test-Path (Join-Path $repoRoot ".git")) {
    $repoCommit = (& git -C $repoRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Unable to resolve repository commit" }
} elseif (Test-Path -LiteralPath $sourceMetadataPath -PathType Leaf) {
    $sourceMetadata = Get-Content -LiteralPath $sourceMetadataPath -Raw | ConvertFrom-Json
    $repoCommit = [string]$sourceMetadata.repositoryCommit
} else {
    $repoCommit = "source-export"
}
if ($hasPrivateE2ESupport) {
    $publicOverlaySha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $publicOverlayPath).Hash.ToLowerInvariant()
    $metadata = [ordered]@{
        schemaVersion = 2
        repositoryCommit = $repoCommit
        target = "windows-amd64"
        buildTag = "yunpin_pairing_private"
        purpose = "private Mac-R0W E2E acceptance only"
        activationGate = "private-snapshot-e2e-only"
        sameRunPublicOverlay = [ordered]@{
            relativePath = "platform/windows/rime/rime_ice.custom.yaml"
            sha256 = $publicOverlaySha256
        }
        overlayOnly = $true
        requiredHostCapability = "yunpin_learning_allowed"
        hostCapabilityProvided = $false
        realCandidateVisibilityClaimed = $false
        publicReleaseEligible = $false
    }
    $metadataPath = Join-Path $privateRoot "BUILD-METADATA.json"
    $metadata | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $metadataPath -Encoding UTF8
    $privatePrefix = $privateRoot.TrimEnd("\") + "\"
    $hashRows = @(Get-ChildItem -LiteralPath $privateRoot -File -Recurse | Where-Object {
        $_.Name -ne "SHA256SUMS"
    } | Sort-Object FullName | ForEach-Object {
        $relative = $_.FullName.Substring($privatePrefix.Length).Replace("\", "/")
        (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant() + "  " + $relative
    })
    [IO.File]::WriteAllLines((Join-Path $privateRoot "SHA256SUMS"), $hashRows, ([Text.UTF8Encoding]::new($false)))
}

# The subsystem is the whole point of building the resident separately, and it
# is invisible in the source, so verify the linked image rather than trusting
# the build flags.
$subsystemChecker = Join-Path $repoRoot "scripts\check_pe_subsystem.py"
if (Test-Path -LiteralPath $subsystemChecker -PathType Leaf) {
    & python $subsystemChecker gui $residentBinary
    if ($LASTEXITCODE -ne 0) { throw "Resident sync agent is not linked for the Windows GUI subsystem" }
    & python $subsystemChecker gui $settingsBinary
    if ($LASTEXITCODE -ne 0) { throw "YunPin settings launcher is not linked for the Windows GUI subsystem" }
    & python $subsystemChecker console $publicBinary
    if ($LASTEXITCODE -ne 0) { throw "Interactive sync agent must stay console-subsystem for its JSON output" }
    & python $subsystemChecker console $replayBinary
    if ($LASTEXITCODE -ne 0) { throw "Replay Lab CLI must stay console-subsystem for its JSON output" }
}

Write-Host "Built public Windows sync agent: $publicBinary"
Write-Host "Built windowless Windows sync resident: $residentBinary"
Write-Host "Built windowless Windows settings launcher: $settingsBinary"
Write-Host "Built Windows Replay Lab CLI: $replayBinary"
if ($hasPrivateE2ESupport) {
    Write-Host "Built private E2E-only Windows sync agent: $privateBinary"
} else {
    Write-Host "Private E2E support is intentionally absent from this public corresponding-source export."
}
