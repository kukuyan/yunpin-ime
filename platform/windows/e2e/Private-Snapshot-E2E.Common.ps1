# SPDX-License-Identifier: Apache-2.0
Set-StrictMode -Version Latest

function ConvertTo-YunPinSha256 {
    param([Parameter(Mandatory = $true)][string]$Value)

    $normalized = $Value.Trim().ToLowerInvariant()
    if ($normalized -notmatch '^[0-9a-f]{64}$') {
        throw "Expected a 64-character SHA-256 value"
    }
    return $normalized
}

function Get-YunPinCurrentUserSid {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    if ($null -eq $identity.User) {
        throw "Unable to resolve the current Windows user SID"
    }
    return $identity.User
}

function Enter-YunPinPrivateSnapshotGateLock {
    $sid = (Get-YunPinCurrentUserSid).Value.Replace("-", "_")
    $name = "Local\YunPinIME.PrivateSnapshotE2E." + $sid
    $mutex = [Threading.Mutex]::new($false, $name)
    try {
        $held = $false
        try {
            $held = $mutex.WaitOne(0)
        } catch [Threading.AbandonedMutexException] {
            # An abandoned mutex is granted to this process. The durable journal
            # is the recovery authority for the interrupted previous owner.
            $held = $true
        }
        if (-not $held) {
            throw "Another private snapshot E2E gate process is active"
        }
        return [pscustomobject]@{
            Mutex = $mutex
            Held = $true
            Name = $name
        }
    } catch {
        $mutex.Dispose()
        throw
    }
}

function Exit-YunPinPrivateSnapshotGateLock {
    param([Parameter(Mandatory = $true)]$Lock)

    try {
        if ($Lock.Held) {
            $Lock.Mutex.ReleaseMutex()
            $Lock.Held = $false
        }
    } finally {
        $Lock.Mutex.Dispose()
    }
}

function Assert-YunPinOwnedNonReparsePath {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $false)]
        [Security.Principal.SecurityIdentifier]$ExpectedOwnerSid = $(Get-YunPinCurrentUserSid)
    )

    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing a reparse point in the private E2E path: $Path"
    }
    $owner = (Get-Acl -LiteralPath $Path -ErrorAction Stop).GetOwner(
        [Security.Principal.SecurityIdentifier]
    )
    if ($owner.Value -cne $ExpectedOwnerSid.Value) {
        throw "Private E2E path is not owned by the current user: $Path"
    }
}

function Set-YunPinOwnerForCreatedPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    # Elevated Windows tokens commonly use BUILTIN\Administrators as their
    # default object owner even though TokenUser is the signed-in user.  Keep
    # the production verifier strict: objects this gate creates are explicitly
    # rebound to TokenUser instead of teaching the verifier to trust the group.
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing to change ownership through a reparse point: $Path"
    }
    $sid = Get-YunPinCurrentUserSid
    $acl = Get-Acl -LiteralPath $Path -ErrorAction Stop
    $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier])
    if ($owner.Value -cne $sid.Value) {
        $acl.SetOwner($sid)
        Set-Acl -LiteralPath $Path -AclObject $acl -ErrorAction Stop
    }
    Assert-YunPinOwnedNonReparsePath -Path $Path
}

function Assert-YunPinOwnedPathChain {
    param(
        [Parameter(Mandatory = $true)][string]$BasePath,
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $false)]
        [Security.Principal.SecurityIdentifier]$ExpectedOwnerSid = $(Get-YunPinCurrentUserSid)
    )

    $base = [IO.Path]::GetFullPath($BasePath).TrimEnd('\')
    $target = [IO.Path]::GetFullPath($Path)
    $prefix = $base + "\"
    if ($target -cne $base -and
        -not $target.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Private E2E path escapes its fixed base: $target"
    }
    if (-not (Test-Path -LiteralPath $base)) {
        throw "Fixed private E2E base is missing: $base"
    }
    Assert-YunPinOwnedNonReparsePath -Path $base -ExpectedOwnerSid $ExpectedOwnerSid
    if ($target -ceq $base) {
        return
    }

    $relative = $target.Substring($prefix.Length)
    $current = $base
    foreach ($part in $relative.Split([char]'\')) {
        if ([string]::IsNullOrWhiteSpace($part)) {
            continue
        }
        $current = Join-Path $current $part
        if (-not (Test-Path -LiteralPath $current)) {
            break
        }
        Assert-YunPinOwnedNonReparsePath -Path $current -ExpectedOwnerSid $ExpectedOwnerSid
    }
}

function Ensure-YunPinOwnedDirectoryChain {
    param(
        [Parameter(Mandatory = $true)][string]$BasePath,
        [Parameter(Mandatory = $true)][string]$DirectoryPath
    )

    $base = [IO.Path]::GetFullPath($BasePath).TrimEnd('\')
    $target = [IO.Path]::GetFullPath($DirectoryPath).TrimEnd('\')
    $prefix = $base + "\"
    if ($target -cne $base -and
        -not $target.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Private E2E directory escapes its fixed base: $target"
    }
    Assert-YunPinOwnedNonReparsePath -Path $base
    if ($target -ceq $base) {
        return
    }

    $current = $base
    foreach ($part in $target.Substring($prefix.Length).Split('\')) {
        if ([string]::IsNullOrWhiteSpace($part)) {
            throw "Private E2E directory contains an empty path component"
        }
        $current = Join-Path $current $part
        if (Test-Path -LiteralPath $current) {
            if (-not (Test-Path -LiteralPath $current -PathType Container)) {
                throw "Private E2E directory component is not a directory: $current"
            }
            Assert-YunPinOwnedNonReparsePath -Path $current
            continue
        }
        New-Item -ItemType Directory -Path $current | Out-Null
        Set-YunPinOwnerForCreatedPath -Path $current
    }
}

function Get-YunPinPrivateSnapshotFixedPaths {
    $appDataKnownFolder = [Environment]::GetFolderPath(
        [Environment+SpecialFolder]::ApplicationData
    )
    $localAppDataKnownFolder = [Environment]::GetFolderPath(
        [Environment+SpecialFolder]::LocalApplicationData
    )
    if ([string]::IsNullOrWhiteSpace($appDataKnownFolder) -or
        [string]::IsNullOrWhiteSpace($localAppDataKnownFolder)) {
        throw "Windows known user-data folders are unavailable"
    }
    $appData = [IO.Path]::GetFullPath($appDataKnownFolder)
    $localAppData = [IO.Path]::GetFullPath($localAppDataKnownFolder)
    $userData = Join-Path $appData "YunPin\Rime"
    $installRoot = Join-Path $localAppData "Programs\YunPinIME\Preview"
    $stateRoot = Join-Path $localAppData "YunPinIME\E2E\private-snapshot-gate"
    return [pscustomobject]@{
        AppDataBase = $appData
        LocalAppDataBase = $localAppData
        UserDataRoot = $userData
        OverlayPath = Join-Path $userData "rime_ice.custom.yaml"
        PrivateSnapshotPath = Join-Path $userData "yunpin\private.tsv"
        InstallRoot = $installRoot
        RuntimeRoot = Join-Path $installRoot "current"
        DeployerPath = Join-Path (Join-Path $installRoot "current") "YunPinDeployer.exe"
        StateRoot = $stateRoot
        StatePath = Join-Path $stateRoot "state.json"
        BackupPath = Join-Path $stateRoot "rime_ice.custom.yaml.public-disabled.backup"
    }
}

function Assert-YunPinGateTargetPaths {
    param(
        [Parameter(Mandatory = $true)]$Paths,
        [switch]$RequirePrivateSnapshot
    )

    $sid = Get-YunPinCurrentUserSid
    Assert-YunPinOwnedPathChain -BasePath $Paths.AppDataBase -Path $Paths.OverlayPath -ExpectedOwnerSid $sid
    if (-not (Test-Path -LiteralPath $Paths.OverlayPath -PathType Leaf)) {
        throw "The fixed YunPin user overlay is missing: $($Paths.OverlayPath)"
    }
    if ($RequirePrivateSnapshot) {
        Assert-YunPinOwnedPathChain -BasePath $Paths.AppDataBase -Path $Paths.PrivateSnapshotPath -ExpectedOwnerSid $sid
        if (-not (Test-Path -LiteralPath $Paths.PrivateSnapshotPath -PathType Leaf)) {
            throw "The fixed YunPin private snapshot is missing: $($Paths.PrivateSnapshotPath)"
        }
        # Deliberately inspect only path type, owner and reparse attributes. Never
        # open, hash or parse private.tsv in this activation gate.
        Assert-YunPinOwnedNonReparsePath -Path $Paths.PrivateSnapshotPath -ExpectedOwnerSid $sid
    }
    Assert-YunPinOwnedPathChain -BasePath $Paths.LocalAppDataBase -Path $Paths.DeployerPath -ExpectedOwnerSid $sid
    if (-not (Test-Path -LiteralPath $Paths.DeployerPath -PathType Leaf)) {
        throw "The fixed YunPin deployer is missing: $($Paths.DeployerPath)"
    }
    Assert-YunPinOwnedPathChain -BasePath $Paths.LocalAppDataBase -Path $Paths.StateRoot -ExpectedOwnerSid $sid
}

function Remove-YunPinStaleGateTemporaryFiles {
    param([Parameter(Mandatory = $true)]$Paths)

    $locations = @(
        [pscustomobject]@{
            Root = $Paths.StateRoot
            Candidates = @(
                [pscustomobject]@{
                    Filter = ".backup-*.tmp"
                    Regex = '^\.backup-[0-9a-f]{32}\.tmp$'
                },
                [pscustomobject]@{
                    Filter = ".state-*.tmp"
                    Regex = '^\.state-[0-9a-f]{32}\.tmp$'
                },
                [pscustomobject]@{
                    Filter = ".replace-backup-*.tmp"
                    Regex = '^\.replace-backup-[0-9a-f]{32}\.tmp$'
                }
            )
        },
        [pscustomobject]@{
            Root = Split-Path -Parent $Paths.OverlayPath
            Candidates = @(
                [pscustomobject]@{
                    Filter = ".yunpin-e2e-replace-*.tmp"
                    Regex = '^\.yunpin-e2e-replace-[0-9a-f]{32}\.tmp$'
                },
                [pscustomobject]@{
                    Filter = ".replace-backup-*.tmp"
                    Regex = '^\.replace-backup-[0-9a-f]{32}\.tmp$'
                }
            )
        }
    )
    foreach ($location in $locations) {
        if (-not (Test-Path -LiteralPath $location.Root -PathType Container)) {
            continue
        }
        Assert-YunPinOwnedNonReparsePath -Path $location.Root
        foreach ($candidate in $location.Candidates) {
            foreach ($temporary in @(Get-ChildItem -LiteralPath $location.Root `
                -File -Force -Filter $candidate.Filter)) {
                if ($temporary.Name -cnotmatch $candidate.Regex) {
                    continue
                }
                Assert-YunPinOwnedNonReparsePath -Path $temporary.FullName
                Remove-Item -LiteralPath $temporary.FullName -Force
            }
        }
    }
}

function Get-YunPinFileSha256 {
    param([Parameter(Mandatory = $true)][string]$Path)
    return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Get-YunPinBytesSha256 {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace("-", "").ToLowerInvariant()
    } finally {
        $sha.Dispose()
    }
}

function Read-YunPinStrictUtf8File {
    param([Parameter(Mandatory = $true)][string]$Path)

    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xef -and
        $bytes[1] -eq 0xbb -and $bytes[2] -eq 0xbf) {
        throw "Private E2E overlay must use UTF-8 without BOM: $Path"
    }
    $utf8 = New-Object Text.UTF8Encoding($false, $true)
    return [pscustomobject]@{
        Bytes = $bytes
        Text = $utf8.GetString($bytes)
    }
}

function Get-YunPinOverlayGateMatches {
    param([Parameter(Mandatory = $true)][string]$Text)

    $options = [Text.RegularExpressions.RegexOptions]::Multiline -bor
        [Text.RegularExpressions.RegexOptions]::CultureInvariant
    $enabledKey = New-Object Text.RegularExpressions.Regex(
        '^[ \t]*(?:"yunpin/enabled"|''yunpin/enabled''|yunpin/enabled)[ \t]*:',
        $options
    )
    $enabledFalse = New-Object Text.RegularExpressions.Regex(
        '^(?<prefix>[ \t]*"yunpin/enabled"[ \t]*:[ \t]*)(?<value>false)(?<suffix>[ \t]*(?:#[^\r\n]*)?)\r?$',
        $options
    )
    $enabledTrue = New-Object Text.RegularExpressions.Regex(
        '^[ \t]*"yunpin/enabled"[ \t]*:[ \t]*true[ \t]*(?:#[^\r\n]*)?\r?$',
        $options
    )
    $learningKey = New-Object Text.RegularExpressions.Regex(
        '^[ \t]*(?:"yunpin/session_learning"|''yunpin/session_learning''|yunpin/session_learning)[ \t]*:',
        $options
    )
    $learningFalse = New-Object Text.RegularExpressions.Regex(
        '^[ \t]*"yunpin/session_learning"[ \t]*:[ \t]*false[ \t]*(?:#[^\r\n]*)?\r?$',
        $options
    )
    $learningTrue = New-Object Text.RegularExpressions.Regex(
        '^[ \t]*"yunpin/session_learning"[ \t]*:[ \t]*true[ \t]*(?:#[^\r\n]*)?\r?$',
        $options
    )
    return [pscustomobject]@{
        EnabledKeys = @($enabledKey.Matches($Text))
        EnabledFalse = @($enabledFalse.Matches($Text))
        EnabledTrue = @($enabledTrue.Matches($Text))
        LearningKeys = @($learningKey.Matches($Text))
        LearningFalse = @($learningFalse.Matches($Text))
        LearningTrue = @($learningTrue.Matches($Text))
    }
}

function Assert-YunPinDisabledOverlay {
    param([Parameter(Mandatory = $true)][string]$Text)

    $matches = Get-YunPinOverlayGateMatches -Text $Text
    if ($matches.EnabledKeys.Count -ne 1 -or
        $matches.EnabledFalse.Count -ne 1 -or
        $matches.EnabledTrue.Count -ne 0) {
        throw "Expected exactly one yunpin/enabled: false and no true value"
    }
    if ($matches.LearningKeys.Count -ne 1 -or
        $matches.LearningFalse.Count -ne 1 -or
        $matches.LearningTrue.Count -ne 0) {
        throw "Expected exactly one yunpin/session_learning: false and no true value"
    }
    return $matches.EnabledFalse[0]
}

function Assert-YunPinEnabledOverlay {
    param([Parameter(Mandatory = $true)][string]$Text)

    $matches = Get-YunPinOverlayGateMatches -Text $Text
    if ($matches.EnabledKeys.Count -ne 1 -or
        $matches.EnabledFalse.Count -ne 0 -or
        $matches.EnabledTrue.Count -ne 1) {
        throw "Expected exactly one yunpin/enabled: true and no false value"
    }
    if ($matches.LearningKeys.Count -ne 1 -or
        $matches.LearningFalse.Count -ne 1 -or
        $matches.LearningTrue.Count -ne 0) {
        throw "Session learning changed while enabling the private E2E snapshot"
    }
}

function ConvertTo-YunPinEnabledOverlay {
    param([Parameter(Mandatory = $true)][string]$Text)

    $match = Assert-YunPinDisabledOverlay -Text $Text
    $value = $match.Groups["value"]
    $enabled = $Text.Substring(0, $value.Index) + "true" +
        $Text.Substring($value.Index + $value.Length)
    Assert-YunPinEnabledOverlay -Text $enabled
    return $enabled
}

function Write-YunPinDurableBytes {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][byte[]]$Bytes
    )

    $stream = [IO.FileStream]::new(
        $Path,
        [IO.FileMode]::CreateNew,
        [IO.FileAccess]::Write,
        [IO.FileShare]::None,
        4096,
        [IO.FileOptions]::WriteThrough
    )
    try {
        $stream.Write($Bytes, 0, $Bytes.Length)
        $stream.Flush($true)
    } finally {
        $stream.Dispose()
    }
    Set-YunPinOwnerForCreatedPath -Path $Path
}

function Replace-YunPinFileAtomically {
    param(
        [Parameter(Mandatory = $true)][string]$ReplacementPath,
        [Parameter(Mandatory = $true)][string]$DestinationPath
    )

    $backupPath = Join-Path (Split-Path -Parent $DestinationPath) (
        ".replace-backup-" + [guid]::NewGuid().ToString("N") + ".tmp"
    )
    if (Test-Path -LiteralPath $backupPath) {
        throw "Atomic private E2E replacement backup path already exists"
    }
    # PowerShell/.NET on Windows rejects a null File.Replace backup path.  A
    # unique same-directory backup retains ReplaceFile's atomic semantics; it
    # is identity-checked before deletion and recognized by crash recovery.
    [IO.File]::Replace($ReplacementPath, $DestinationPath, $backupPath, $true)
    if (-not (Test-Path -LiteralPath $backupPath -PathType Leaf)) {
        throw "Atomic private E2E replacement did not create its recovery backup"
    }
    Assert-YunPinOwnedNonReparsePath -Path $backupPath
    Remove-Item -LiteralPath $backupPath -Force
}

function Set-YunPinFileAtomically {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][byte[]]$Bytes,
        [Parameter(Mandatory = $true)][string]$ExpectedSha256
    )

    $temporary = Join-Path (Split-Path -Parent $Path) (
        ".yunpin-e2e-replace-" + [guid]::NewGuid().ToString("N") + ".tmp"
    )
    try {
        Write-YunPinDurableBytes -Path $temporary -Bytes $Bytes
        Assert-YunPinOwnedNonReparsePath -Path $temporary
        if ((Get-YunPinFileSha256 -Path $temporary) -cne $ExpectedSha256) {
            throw "Durable private E2E replacement hash differs"
        }
        Replace-YunPinFileAtomically -ReplacementPath $temporary `
            -DestinationPath $Path
        if ((Get-YunPinFileSha256 -Path $Path) -cne $ExpectedSha256) {
            throw "Atomic private E2E replacement hash differs"
        }
        Assert-YunPinOwnedNonReparsePath -Path $Path
    } finally {
        if (Test-Path -LiteralPath $temporary -PathType Leaf) {
            Remove-Item -LiteralPath $temporary -Force
        }
    }
}

function New-YunPinGateState {
    param(
        [Parameter(Mandatory = $true)][string]$Phase,
        [Parameter(Mandatory = $true)][string]$PublicSha256,
        [Parameter(Mandatory = $true)][string]$EnabledSha256
    )
    return [ordered]@{
        schemaVersion = 1
        phase = $Phase
        publicOverlaySha256 = $PublicSha256
        enabledOverlaySha256 = $EnabledSha256
        updatedAtUtc = [DateTime]::UtcNow.ToString("o")
    }
}

function Write-YunPinGateState {
    param(
        [Parameter(Mandatory = $true)]$Paths,
        [Parameter(Mandatory = $true)][string]$Phase,
        [Parameter(Mandatory = $true)][string]$PublicSha256,
        [Parameter(Mandatory = $true)][string]$EnabledSha256
    )

    Ensure-YunPinOwnedDirectoryChain -BasePath $Paths.LocalAppDataBase `
        -DirectoryPath $Paths.StateRoot
    Assert-YunPinOwnedPathChain -BasePath $Paths.LocalAppDataBase -Path $Paths.StateRoot
    $state = New-YunPinGateState -Phase $Phase -PublicSha256 $PublicSha256 `
        -EnabledSha256 $EnabledSha256
    $bytes = (New-Object Text.UTF8Encoding($false)).GetBytes(
        (($state | ConvertTo-Json -Depth 4) + "`n")
    )
    $temporary = Join-Path $Paths.StateRoot (
        ".state-" + [guid]::NewGuid().ToString("N") + ".tmp"
    )
    try {
        Write-YunPinDurableBytes -Path $temporary -Bytes $bytes
        if (Test-Path -LiteralPath $Paths.StatePath -PathType Leaf) {
            Replace-YunPinFileAtomically -ReplacementPath $temporary `
                -DestinationPath $Paths.StatePath
        } else {
            [IO.File]::Move($temporary, $Paths.StatePath)
        }
        Assert-YunPinOwnedNonReparsePath -Path $Paths.StatePath
    } finally {
        if (Test-Path -LiteralPath $temporary -PathType Leaf) {
            Remove-Item -LiteralPath $temporary -Force
        }
    }
}

function Read-YunPinGateState {
    param(
        [Parameter(Mandatory = $true)]$Paths,
        [Parameter(Mandatory = $true)][string]$PublicSha256,
        [Parameter(Mandatory = $true)][string]$EnabledSha256
    )

    if (-not (Test-Path -LiteralPath $Paths.StatePath -PathType Leaf)) {
        return $null
    }
    Assert-YunPinOwnedNonReparsePath -Path $Paths.StatePath
    $state = Get-Content -LiteralPath $Paths.StatePath -Raw | ConvertFrom-Json
    foreach ($property in @(
        "schemaVersion", "phase", "publicOverlaySha256", "enabledOverlaySha256"
    )) {
        if ($state.PSObject.Properties.Name -notcontains $property) {
            throw "Private E2E state is missing $property"
        }
    }
    if ($state.schemaVersion -ne 1 -or
        [string]$state.publicOverlaySha256 -cne $PublicSha256 -or
        [string]$state.enabledOverlaySha256 -cne $EnabledSha256) {
        throw "Private E2E state does not match this same-run public overlay"
    }
    $allowed = @(
        "backup-ready", "overlay-enabled-pending-deploy", "enabled",
        "disable-pending-replace", "disabled-pending-deploy",
        "disabled-deploy-succeeded"
    )
    if ($allowed -notcontains [string]$state.phase) {
        throw "Unknown private E2E recovery phase: $($state.phase)"
    }
    return $state
}

function Get-YunPinBackupTransform {
    param(
        [Parameter(Mandatory = $true)]$Paths,
        [Parameter(Mandatory = $true)][string]$PublicSha256
    )

    if (-not (Test-Path -LiteralPath $Paths.BackupPath -PathType Leaf)) {
        throw "Durable private E2E backup is missing"
    }
    Assert-YunPinOwnedNonReparsePath -Path $Paths.BackupPath
    if ((Get-YunPinFileSha256 -Path $Paths.BackupPath) -cne $PublicSha256) {
        throw "Durable private E2E backup does not match the expected public overlay"
    }
    $public = Read-YunPinStrictUtf8File -Path $Paths.BackupPath
    Assert-YunPinDisabledOverlay -Text $public.Text | Out-Null
    $enabledText = ConvertTo-YunPinEnabledOverlay -Text $public.Text
    $enabledBytes = (New-Object Text.UTF8Encoding($false)).GetBytes($enabledText)
    return [pscustomobject]@{
        PublicBytes = $public.Bytes
        EnabledBytes = $enabledBytes
        EnabledSha256 = Get-YunPinBytesSha256 -Bytes $enabledBytes
    }
}

function Initialize-YunPinGateBackup {
    param(
        [Parameter(Mandatory = $true)]$Paths,
        [Parameter(Mandatory = $true)][string]$PublicSha256
    )

    $current = Read-YunPinStrictUtf8File -Path $Paths.OverlayPath
    if ((Get-YunPinBytesSha256 -Bytes $current.Bytes) -cne $PublicSha256) {
        throw "Installed overlay does not match the expected same-run public overlay"
    }
    Assert-YunPinDisabledOverlay -Text $current.Text | Out-Null
    Ensure-YunPinOwnedDirectoryChain -BasePath $Paths.LocalAppDataBase `
        -DirectoryPath $Paths.StateRoot
    Assert-YunPinOwnedPathChain -BasePath $Paths.LocalAppDataBase -Path $Paths.StateRoot
    if (-not (Test-Path -LiteralPath $Paths.BackupPath -PathType Leaf)) {
        $temporary = Join-Path $Paths.StateRoot (
            ".backup-" + [guid]::NewGuid().ToString("N") + ".tmp"
        )
        try {
            Write-YunPinDurableBytes -Path $temporary -Bytes $current.Bytes
            if ((Get-YunPinFileSha256 -Path $temporary) -cne $PublicSha256) {
                throw "Durable private E2E backup hash differs"
            }
            [IO.File]::Move($temporary, $Paths.BackupPath)
        } finally {
            if (Test-Path -LiteralPath $temporary -PathType Leaf) {
                Remove-Item -LiteralPath $temporary -Force
            }
        }
    }
    Assert-YunPinOwnedNonReparsePath -Path $Paths.BackupPath
}

function Invoke-YunPinPrivateSnapshotEnable {
    param(
        [Parameter(Mandatory = $true)]$Paths,
        [Parameter(Mandatory = $true)][string]$ExpectedPublicOverlaySha256,
        [Parameter(Mandatory = $true)][scriptblock]$DeployAction
    )

    $publicSha = ConvertTo-YunPinSha256 -Value $ExpectedPublicOverlaySha256
    Assert-YunPinGateTargetPaths -Paths $Paths -RequirePrivateSnapshot
    Remove-YunPinStaleGateTemporaryFiles -Paths $Paths

    $hasState = Test-Path -LiteralPath $Paths.StatePath -PathType Leaf
    $hasBackup = Test-Path -LiteralPath $Paths.BackupPath -PathType Leaf
    if (-not $hasState -and -not $hasBackup) {
        Initialize-YunPinGateBackup -Paths $Paths -PublicSha256 $publicSha
    }

    $transform = Get-YunPinBackupTransform -Paths $Paths -PublicSha256 $publicSha
    $state = Read-YunPinGateState -Paths $Paths -PublicSha256 $publicSha `
        -EnabledSha256 $transform.EnabledSha256
    $targetSha = Get-YunPinFileSha256 -Path $Paths.OverlayPath
    if ($null -eq $state) {
        if ($targetSha -ceq $publicSha) {
            $phase = "backup-ready"
        } elseif ($targetSha -ceq $transform.EnabledSha256) {
            $phase = "overlay-enabled-pending-deploy"
        } else {
            throw "Orphaned private E2E backup cannot be reconciled with the installed overlay"
        }
        Write-YunPinGateState -Paths $Paths -Phase $phase -PublicSha256 $publicSha `
            -EnabledSha256 $transform.EnabledSha256
        $state = Read-YunPinGateState -Paths $Paths -PublicSha256 $publicSha `
            -EnabledSha256 $transform.EnabledSha256
    }

    if ([string]$state.phase -in @(
        "disable-pending-replace", "disabled-pending-deploy",
        "disabled-deploy-succeeded"
    )) {
        throw "Private E2E disable recovery is pending; run the disable script"
    }
    if ([string]$state.phase -ceq "enabled") {
        if ($targetSha -cne $transform.EnabledSha256) {
            throw "Enabled private E2E overlay was modified outside the gate"
        }
        Assert-YunPinEnabledOverlay -Text (Read-YunPinStrictUtf8File -Path $Paths.OverlayPath).Text
        return "already-enabled"
    }
    if ([string]$state.phase -ceq "backup-ready") {
        if ($targetSha -ceq $publicSha) {
            Set-YunPinFileAtomically -Path $Paths.OverlayPath `
                -Bytes $transform.EnabledBytes -ExpectedSha256 $transform.EnabledSha256
        } elseif ($targetSha -cne $transform.EnabledSha256) {
            throw "Installed overlay changed after the durable backup was created"
        }
        Write-YunPinGateState -Paths $Paths -Phase "overlay-enabled-pending-deploy" `
            -PublicSha256 $publicSha -EnabledSha256 $transform.EnabledSha256
    } elseif ([string]$state.phase -ceq "overlay-enabled-pending-deploy") {
        if ($targetSha -cne $transform.EnabledSha256) {
            throw "Pending private E2E overlay does not match the recorded enabled hash"
        }
    } else {
        throw "Private E2E enable cannot resume phase: $($state.phase)"
    }

    Assert-YunPinEnabledOverlay -Text (Read-YunPinStrictUtf8File -Path $Paths.OverlayPath).Text
    & $DeployAction
    Write-YunPinGateState -Paths $Paths -Phase "enabled" -PublicSha256 $publicSha `
        -EnabledSha256 $transform.EnabledSha256
    return "enabled"
}

function Remove-YunPinGateStateAfterDeploy {
    param([Parameter(Mandatory = $true)]$Paths)

    # Delete the journal first. A crash after this point leaves only the durable
    # backup, which is an explicitly supported orphan-recovery state. Deleting
    # the backup first could strand an unrecoverable state-without-backup pair.
    if (Test-Path -LiteralPath $Paths.StatePath -PathType Leaf) {
        Assert-YunPinOwnedNonReparsePath -Path $Paths.StatePath
        Remove-Item -LiteralPath $Paths.StatePath -Force
    }
    if (Test-Path -LiteralPath $Paths.BackupPath -PathType Leaf) {
        Assert-YunPinOwnedNonReparsePath -Path $Paths.BackupPath
        Remove-Item -LiteralPath $Paths.BackupPath -Force
    }
    if (Test-Path -LiteralPath $Paths.StateRoot -PathType Container) {
        Assert-YunPinOwnedNonReparsePath -Path $Paths.StateRoot
        if (@(Get-ChildItem -LiteralPath $Paths.StateRoot -Force).Count -eq 0) {
            Remove-Item -LiteralPath $Paths.StateRoot -Force
        }
    }
}

function Invoke-YunPinPrivateSnapshotDisable {
    param(
        [Parameter(Mandatory = $true)]$Paths,
        [Parameter(Mandatory = $true)][string]$ExpectedPublicOverlaySha256,
        [Parameter(Mandatory = $true)][scriptblock]$DeployAction
    )

    $publicSha = ConvertTo-YunPinSha256 -Value $ExpectedPublicOverlaySha256
    Assert-YunPinGateTargetPaths -Paths $Paths
    Remove-YunPinStaleGateTemporaryFiles -Paths $Paths
    $hasState = Test-Path -LiteralPath $Paths.StatePath -PathType Leaf
    $hasBackup = Test-Path -LiteralPath $Paths.BackupPath -PathType Leaf
    if (-not $hasState -and -not $hasBackup) {
        throw "No private E2E activation backup exists"
    }

    $transform = Get-YunPinBackupTransform -Paths $Paths -PublicSha256 $publicSha
    $state = Read-YunPinGateState -Paths $Paths -PublicSha256 $publicSha `
        -EnabledSha256 $transform.EnabledSha256
    $targetSha = Get-YunPinFileSha256 -Path $Paths.OverlayPath
    if ($null -eq $state) {
        if ($targetSha -ceq $publicSha) {
            $phase = "disabled-pending-deploy"
        } elseif ($targetSha -ceq $transform.EnabledSha256) {
            $phase = "enabled"
        } else {
            throw "Orphaned private E2E backup cannot be reconciled for disable"
        }
        Write-YunPinGateState -Paths $Paths -Phase $phase -PublicSha256 $publicSha `
            -EnabledSha256 $transform.EnabledSha256
        $state = Read-YunPinGateState -Paths $Paths -PublicSha256 $publicSha `
            -EnabledSha256 $transform.EnabledSha256
    }

    if ([string]$state.phase -ceq "disabled-deploy-succeeded") {
        Remove-YunPinGateStateAfterDeploy -Paths $Paths
        return "already-disabled"
    }
    if ([string]$state.phase -ceq "disabled-pending-deploy") {
        if ($targetSha -cne $publicSha) {
            throw "Pending disabled overlay does not match the durable public backup"
        }
    } else {
        if ([string]$state.phase -notin @(
            "backup-ready", "overlay-enabled-pending-deploy", "enabled",
            "disable-pending-replace"
        )) {
            throw "Private E2E disable cannot resume phase: $($state.phase)"
        }
        Write-YunPinGateState -Paths $Paths -Phase "disable-pending-replace" `
            -PublicSha256 $publicSha -EnabledSha256 $transform.EnabledSha256
        if ($targetSha -ceq $transform.EnabledSha256) {
            Set-YunPinFileAtomically -Path $Paths.OverlayPath `
                -Bytes $transform.PublicBytes -ExpectedSha256 $publicSha
        } elseif ($targetSha -cne $publicSha) {
            throw "Installed overlay changed before private E2E disable"
        }
        Write-YunPinGateState -Paths $Paths -Phase "disabled-pending-deploy" `
            -PublicSha256 $publicSha -EnabledSha256 $transform.EnabledSha256
    }

    Assert-YunPinDisabledOverlay -Text (Read-YunPinStrictUtf8File -Path $Paths.OverlayPath).Text | Out-Null
    & $DeployAction
    Write-YunPinGateState -Paths $Paths -Phase "disabled-deploy-succeeded" `
        -PublicSha256 $publicSha -EnabledSha256 $transform.EnabledSha256
    Remove-YunPinGateStateAfterDeploy -Paths $Paths
    return "disabled"
}

function Invoke-YunPinDeployer {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [int]$TimeoutSeconds = 120
    )

    if ($TimeoutSeconds -lt 1 -or $TimeoutSeconds -gt 300) {
        throw "YunPin deploy timeout must be between 1 and 300 seconds"
    }

    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $Path
    $startInfo.Arguments = "/deploy"
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) {
            throw "Failed to start the fixed YunPin deployer"
        }
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            try {
                $process.Kill()
                [void]$process.WaitForExit(5000)
            } catch {
                # The journal and backup remain authoritative even if termination
                # races a process that has just exited.
            }
            throw "YunPinDeployer /deploy timed out after $TimeoutSeconds seconds; recovery state retained"
        }
        if ($process.ExitCode -ne 0) {
            throw "YunPinDeployer /deploy failed with exit code $($process.ExitCode)"
        }
    } finally {
        $process.Dispose()
    }
}

function Assert-YunPinPrivateArtifactBinding {
    param(
        [Parameter(Mandatory = $true)][string]$ArtifactRoot,
        [Parameter(Mandatory = $true)][string]$ExpectedPublicOverlaySha256
    )

    $expected = ConvertTo-YunPinSha256 -Value $ExpectedPublicOverlaySha256
    $root = [IO.Path]::GetFullPath($ArtifactRoot)
    if (-not [IO.Path]::IsPathRooted($root) -or
        $root -ceq [IO.Path]::GetPathRoot($root)) {
        throw "Private E2E artifact root is not a bounded directory"
    }
    Assert-YunPinOwnedNonReparsePath -Path $root
    $metadataPath = Join-Path $root "BUILD-METADATA.json"
    $sumsPath = Join-Path $root "SHA256SUMS"
    foreach ($path in @($metadataPath, $sumsPath)) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Private E2E artifact control file is missing: $path"
        }
        Assert-YunPinOwnedNonReparsePath -Path $path
    }

    $seen = @{}
    foreach ($line in Get-Content -LiteralPath $sumsPath) {
        if ($line -notmatch '^([0-9a-f]{64})  ([A-Za-z0-9_.@/-]+)$') {
            throw "Malformed private E2E checksum row"
        }
        $relative = $Matches[2]
        if ($relative.Contains("..") -or [IO.Path]::IsPathRooted($relative)) {
            throw "Private E2E checksum path is unsafe: $relative"
        }
        $key = $relative.ToLowerInvariant()
        if ($seen.ContainsKey($key)) {
            throw "Duplicate private E2E checksum path: $relative"
        }
        $seen[$key] = $Matches[1]
        $path = [IO.Path]::GetFullPath((Join-Path $root $relative.Replace("/", "\")))
        $prefix = $root.TrimEnd('\') + "\"
        if (-not $path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase) -or
            -not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Private E2E checksum path escapes or is missing: $relative"
        }
        Assert-YunPinOwnedPathChain -BasePath $root -Path $path
        if ((Get-YunPinFileSha256 -Path $path) -cne $Matches[1]) {
            throw "Private E2E checksum mismatch: $relative"
        }
    }
    foreach ($required in @(
        ".yunpin-private-e2e-generated", "build-metadata.json", "yunpin-sync-agent.exe",
        "private-snapshot-e2e.common.ps1",
        "enable-private-snapshot-e2e.ps1",
        "disable-private-snapshot-e2e.ps1", "readme.md"
    )) {
        if (-not $seen.ContainsKey($required)) {
            throw "Private E2E checksum coverage is missing: $required"
        }
    }
    $prefix = $root.TrimEnd('\') + "\"
    $actual = @(Get-ChildItem -LiteralPath $root -File -Recurse | Where-Object {
        $_.Name -cne "SHA256SUMS"
    } | ForEach-Object {
        $_.FullName.Substring($prefix.Length).Replace("\", "/").ToLowerInvariant()
    })
    if ($actual.Count -ne $seen.Count) {
        throw "Private E2E checksum coverage count differs from the artifact tree"
    }
    foreach ($relative in $actual) {
        if (-not $seen.ContainsKey($relative)) {
            throw "Private E2E file is not covered by SHA256SUMS: $relative"
        }
    }

    $metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
    if ($metadata.schemaVersion -ne 2 -or
        $metadata.publicReleaseEligible -ne $false -or
        [string]$metadata.activationGate -cne "private-snapshot-e2e-only" -or
        [string]$metadata.sameRunPublicOverlay.relativePath -cne
            "platform/windows/rime/rime_ice.custom.yaml" -or
        [string]$metadata.sameRunPublicOverlay.sha256 -cne $expected -or
        $metadata.overlayOnly -ne $true -or
        [string]$metadata.requiredHostCapability -cne "yunpin_learning_allowed" -or
        $metadata.hostCapabilityProvided -ne $false -or
        $metadata.realCandidateVisibilityClaimed -ne $false) {
        throw "Private E2E metadata does not bind the expected same-run public overlay"
    }
}
