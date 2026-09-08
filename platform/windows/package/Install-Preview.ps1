# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [switch]$AcceptUnsignedDevelopmentBuild,
    [string]$InstallRoot = "",
    [string]$UserDataRoot = "",
    [switch]$RecoverTransaction
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function ConvertTo-NativeCommandLineArgument {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Argument)

    if ($Argument.Length -gt 0 -and $Argument -notmatch '[\s"]') {
        return $Argument
    }

    $quoted = New-Object Text.StringBuilder
    [void]$quoted.Append([char]34)
    $backslashes = 0
    foreach ($character in $Argument.ToCharArray()) {
        if ($character -eq [char]92) {
            $backslashes++
            continue
        }
        if ($character -eq [char]34) {
            [void]$quoted.Append([char]92, (2 * $backslashes) + 1)
            [void]$quoted.Append([char]34)
            $backslashes = 0
            continue
        }
        if ($backslashes -gt 0) {
            [void]$quoted.Append([char]92, $backslashes)
            $backslashes = 0
        }
        [void]$quoted.Append($character)
    }
    if ($backslashes -gt 0) {
        [void]$quoted.Append([char]92, 2 * $backslashes)
    }
    [void]$quoted.Append([char]34)
    return $quoted.ToString()
}

function Invoke-CheckedExecutable {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $false)][string[]]$Arguments = @(),
        [ValidateRange(1, 3600)][int]$TimeoutSeconds = 600
    )
    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $FilePath
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    if ($Arguments.Count -gt 0) {
        $startInfo.Arguments = (($Arguments | ForEach-Object {
            ConvertTo-NativeCommandLineArgument -Argument $_
        }) -join " ")
    }
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        throw "Failed to start executable: $FilePath"
    }
    if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
        try { $process.Kill(); $process.WaitForExit(15000) | Out-Null } catch { }
        throw "An installation child exceeded its bounded deadline: $FilePath"
    }
    if ($process.ExitCode -ne 0) {
        throw "Command failed with exit code $($process.ExitCode): $FilePath $($Arguments -join ' ')"
    }
}

function Set-YunPinMachineRegistry64 {
    param([Parameter(Mandatory = $true)][string]$RuntimeRoot)

    $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey(
        [Microsoft.Win32.RegistryHive]::LocalMachine,
        [Microsoft.Win32.RegistryView]::Registry64
    )
    $path = "Software\YunPin\IME"
    try {
        $existing = $base.OpenSubKey($path)
        try {
            if ($existing) {
                $registeredRoot = $existing.GetValue("WeaselRoot")
                if ($registeredRoot -and $registeredRoot -ne $RuntimeRoot) {
                    throw "A different 64-bit YunPin runtime is already registered: $registeredRoot"
                }
            }
        } finally {
            if ($existing) {
                $existing.Dispose()
            }
        }
        $key = $base.CreateSubKey($path)
        try {
            $key.SetValue(
                "WeaselRoot",
                $RuntimeRoot,
                [Microsoft.Win32.RegistryValueKind]::String
            )
            $key.SetValue(
                "ServerExecutable",
                "YunPinServer.exe",
                [Microsoft.Win32.RegistryValueKind]::String
            )
        } finally {
            $key.Dispose()
        }
    } finally {
        $base.Dispose()
    }
}

function Assert-BundleManifest {
    param([Parameter(Mandatory = $true)][string]$BundleRoot)
    $manifest = Join-Path $BundleRoot "MANIFEST.sha256"
    if (-not (Test-Path $manifest -PathType Leaf)) {
        throw "MANIFEST.sha256 is missing"
    }
    $prefix = [IO.Path]::GetFullPath($BundleRoot).TrimEnd("\") + "\"
    $expected = @{}
    foreach ($line in Get-Content -LiteralPath $manifest) {
        if ($line -notmatch '^([0-9a-f]{64})  (.+)$') {
            throw "Malformed manifest row: $line"
        }
        $relative = $Matches[2].Replace("/", "\")
        $path = [IO.Path]::GetFullPath((Join-Path $BundleRoot $relative))
        if (-not $path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Manifest path escapes the bundle: $relative"
        }
        if (-not (Test-Path $path -PathType Leaf)) {
            throw "Manifest file is missing: $relative"
        }
        $observed = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        if ($observed -ne $Matches[1]) {
            throw "Manifest hash mismatch: $relative"
        }
        $expected[$Matches[2]] = $Matches[1]
    }
    return $expected
}

function Copy-OverlayWithBackup {
    param(
        [Parameter(Mandatory = $true)][string]$SourceRoot,
        [Parameter(Mandatory = $true)][string]$DestinationRoot,
        [Parameter(Mandatory = $true)][string]$BackupRoot
    )
    $sourcePrefix = [IO.Path]::GetFullPath($SourceRoot).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    foreach ($source in Get-ChildItem -LiteralPath $SourceRoot -File -Recurse) {
        $relative = $source.FullName.Substring($sourcePrefix.Length)
        $destination = Join-Path $DestinationRoot $relative
        if (Test-Path $destination -PathType Leaf) {
            $oldHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $destination).Hash
            $newHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $source.FullName).Hash
            if ($oldHash -ne $newHash) {
                $backup = Join-Path $BackupRoot $relative
                New-Item -ItemType Directory -Path (Split-Path $backup -Parent) -Force | Out-Null
                Copy-Item -LiteralPath $destination -Destination $backup -Force
            }
            # A custom overlay is user-owned, including unknown YAML and
            # explicit false choices. Never reconstruct it from a few booleans.
            # Retain new defaults outside Rime's active config for review.
            if ($source.Name.EndsWith('.custom.yaml', [StringComparison]::OrdinalIgnoreCase)) {
                [void](Read-YunPinStrictUtf8File -Path $destination)
                if ($oldHash -ne $newHash) {
                    $proposed = Join-Path (Join-Path $BackupRoot 'incoming-defaults') $relative
                    New-Item -ItemType Directory -Path (Split-Path $proposed -Parent) -Force | Out-Null
                    Copy-Item -LiteralPath $source.FullName -Destination $proposed -Force
                    Write-Host "Preserved user overlay; packaged defaults saved for review: $relative"
                }
                continue
            }
        }
        New-Item -ItemType Directory -Path (Split-Path $destination -Parent) -Force | Out-Null
        Copy-Item -LiteralPath $source.FullName -Destination $destination -Force
    }
}

function Restore-YunPinServerStartup {
    param([bool]$WasInstalled, [bool]$WasRunning, [string]$RunKey, [string]$Server)
    if (-not $WasInstalled) {
        New-Item -Path $RunKey -Force | Out-Null
        New-ItemProperty -Path $RunKey -Name 'YunPinIMEPreview' -PropertyType String -Value ('"' + $Server + '"') -Force | Out-Null
    }
    # Existing Run value, including its absence, remains byte-for-byte intact.
    if (-not $WasInstalled -or $WasRunning) { Start-Process -FilePath $Server | Out-Null }
}

function Write-YunPinInstallJournal {
    param([string]$Path, [object]$Journal)
    $temporary = $Path + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    $bytes = (New-Object Text.UTF8Encoding($false)).GetBytes(($Journal | ConvertTo-Json -Depth 8))
    $stream = New-Object IO.FileStream($temporary, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $stream.Write($bytes, 0, $bytes.Length); $stream.Flush($true) } finally { $stream.Dispose() }
    if ([IO.File]::Exists($Path)) {
        [IO.File]::Replace($temporary, $Path, ($temporary + '.previous'), $true)
    } else {
        [IO.File]::Move($temporary, $Path)
    }
}

function Write-YunPinPriorState {
    param([string]$Path, [object]$Prior)
    $temporary = $Path + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    $Prior | Export-Clixml -LiteralPath $temporary -Depth 30
    $stream = [IO.File]::Open($temporary, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    try { $stream.Flush($true) } finally { $stream.Dispose() }
    if ([IO.File]::Exists($Path)) { [IO.File]::Replace($temporary, $Path, ($temporary + '.previous'), $true) }
    else { [IO.File]::Move($temporary, $Path) }
}

function Invoke-YunPinInstallTransaction {
    param([string]$JournalPath, [object[]]$Stages, [scriptblock]$Rollback)
    if (Test-Path -LiteralPath $JournalPath) { throw 'An installation transaction already exists; recover it instead of starting another attempt.' }
    $journal = [ordered]@{ schemaVersion = 1; state = 'COMMITTED'; stage = ''; completed = @(); startedAtUtc = [DateTime]::UtcNow.ToString('o') }
    Write-YunPinInstallJournal -Path $JournalPath -Journal $journal
    try {
        foreach ($stage in $Stages) {
            $journal.stage = [string]$stage.Name
            Write-YunPinInstallJournal -Path $JournalPath -Journal $journal
            & $stage.Action
            $journal.completed += [string]$stage.Name
            Write-YunPinInstallJournal -Path $JournalPath -Journal $journal
        }
        $journal.state = 'SUCCEEDED'
        Write-YunPinInstallJournal -Path $JournalPath -Journal $journal
    } catch {
        $failure = $_
        $journal.state = 'ROLLING_BACK'
        Write-YunPinInstallJournal -Path $JournalPath -Journal $journal
        try {
            & $Rollback $journal
            $journal.state = 'ROLLED_BACK'
            Write-YunPinInstallJournal -Path $JournalPath -Journal $journal
        } catch {
            $journal.state = 'RECOVERY_REQUIRED'
            Write-YunPinInstallJournal -Path $JournalPath -Journal $journal
            throw "Installation failed and automatic recovery is incomplete. Writers remain stopped; recover the existing journal: $JournalPath"
        }
        throw $failure
    }
}

function Assert-YunPinSafeTree {
    param([string]$Path)
    $cursor = [IO.Path]::GetFullPath($Path)
    while ($cursor) {
        if (Test-Path -LiteralPath $cursor) {
            if ((Get-Item -LiteralPath $cursor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw 'An installation or backup path contains a reparse point.'
            }
        }
        $cursor = [IO.Path]::GetDirectoryName($cursor)
    }
    if (Test-Path -LiteralPath $Path -PathType Container) {
        $queue = New-Object 'Collections.Generic.Queue[string]'
        $queue.Enqueue($Path)
        while ($queue.Count -gt 0) {
            foreach ($item in Get-ChildItem -LiteralPath $queue.Dequeue() -Force) {
                if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'A managed installation tree contains a reparse point.' }
                if ($item.PSIsContainer) { $queue.Enqueue($item.FullName) }
            }
        }
    }
}

function Save-YunPinPathBackups {
    param([string[]]$Paths, [string]$BackupRoot)
    New-Item -ItemType Directory -Path $BackupRoot -Force | Out-Null
    $entries = @()
    foreach ($path in $Paths) {
        Assert-YunPinSafeTree -Path $path
        $entry = [ordered]@{ path = $path; backup = (Join-Path $BackupRoot ([string]$entries.Count)); existed = (Test-Path -LiteralPath $path) }
        if ($entry.existed) { Copy-Item -LiteralPath $path -Destination $entry.backup -Recurse -Force }
        $entries += [pscustomobject]$entry
    }
    return $entries
}

function Restore-YunPinPathBackups {
    param([object[]]$Entries, [string]$QuarantineRoot)
    New-Item -ItemType Directory -Path $QuarantineRoot -Force | Out-Null
    for ($index = $Entries.Count - 1; $index -ge 0; $index--) {
        $entry = $Entries[$index]
        Assert-YunPinSafeTree -Path $entry.path
        if ($entry.existed -and -not (Test-Path -LiteralPath $entry.backup)) { throw 'A required installation rollback source is missing.' }
        if ((Test-Path -LiteralPath $entry.path -PathType Leaf) -and $entry.existed -and
            (Test-Path -LiteralPath $entry.backup -PathType Leaf) -and
            (Get-FileHash -LiteralPath $entry.path -Algorithm SHA256).Hash -ceq (Get-FileHash -LiteralPath $entry.backup -Algorithm SHA256).Hash) {
            continue
        }
        if (Test-Path -LiteralPath $entry.path) {
            Move-Item -LiteralPath $entry.path -Destination (Join-Path $QuarantineRoot ([guid]::NewGuid().ToString('N')))
        }
        if ($entry.existed) {
            New-Item -ItemType Directory -Path (Split-Path $entry.path -Parent) -Force | Out-Null
            Copy-Item -LiteralPath $entry.backup -Destination $entry.path -Recurse -Force
        }
    }
}

function Get-YunPinRegistryTree {
    param([Microsoft.Win32.RegistryKey]$Key)
    $values = @()
    foreach ($name in ($Key.GetValueNames() | Sort-Object)) {
        $values += [pscustomobject]@{ name = $name; kind = $Key.GetValueKind($name).ToString(); data = $Key.GetValue($name, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) }
    }
    $children = @()
    foreach ($name in ($Key.GetSubKeyNames() | Sort-Object)) {
        $child = $Key.OpenSubKey($name)
        try { $children += [pscustomobject]@{ name = $name; tree = (Get-YunPinRegistryTree -Key $child) } } finally { $child.Dispose() }
    }
    return [pscustomobject]@{ values = $values; children = $children }
}

function Set-YunPinRegistryTree {
    param([Microsoft.Win32.RegistryKey]$Key, [object]$Tree)
    foreach ($value in $Tree.values) {
        $kind = [Microsoft.Win32.RegistryValueKind]$value.kind
        $data = $value.data
        switch ($kind.ToString()) {
            'Binary' { $data = [byte[]]$data }
            'None' { $data = [byte[]]$data }
            'DWord' { $data = [int]$data }
            'QWord' { $data = [long]$data }
            'MultiString' { $data = [string[]]$data }
            default { $data = [string]$data }
        }
        $Key.SetValue($value.name, $data, $kind)
    }
    foreach ($child in $Tree.children) {
        $keyChild = $Key.CreateSubKey($child.name)
        try { Set-YunPinRegistryTree -Key $keyChild -Tree $child.tree } finally { $keyChild.Dispose() }
    }
}

function Save-YunPinRegistryBackups {
    param([object[]]$Targets)
    $entries = @()
    foreach ($target in $Targets) {
        $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]$target.hive, [Microsoft.Win32.RegistryView]$target.view)
        try {
            $key = $base.OpenSubKey($target.path)
            try {
                $tree = $null
                if ($null -ne $key) { $tree = Get-YunPinRegistryTree -Key $key }
                $entries += [pscustomobject]@{ hive = $target.hive; view = $target.view; path = $target.path; tree = $tree }
            } finally { if ($null -ne $key) { $key.Dispose() } }
        } finally { $base.Dispose() }
    }
    return $entries
}

function Restore-YunPinRegistryBackups {
    param([object[]]$Entries)
    foreach ($entry in $Entries) {
        $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]$entry.hive, [Microsoft.Win32.RegistryView]$entry.view)
        try {
            $base.DeleteSubKeyTree($entry.path, $false)
            if ($null -ne $entry.tree) {
                $key = $base.CreateSubKey($entry.path)
                try { Set-YunPinRegistryTree -Key $key -Tree $entry.tree } finally { $key.Dispose() }
            }
        } finally { $base.Dispose() }
    }
}

function Assert-YunPinReplaceableDlls {
    param([string[]]$Paths)
    foreach ($path in $Paths) {
        Assert-YunPinSafeTree -Path $path
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            try {
                $stream = [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
                $stream.Dispose()
            } catch {
                throw 'YunPin TSF DLL is in use or not writable. No package replacement was started; use a controlled sign-out/reboot installation window.'
            }
        }
    }
}

function Assert-YunPinNoPendingDllRenames {
    $pending = Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager' -Name 'PendingFileRenameOperations' -ErrorAction SilentlyContinue
    if ($null -ne $pending) {
        foreach ($entry in $pending.PendingFileRenameOperations) {
            if ($entry -match '(?i)\\yunpin(?:x64)?\.dll(?:\.old\.[0-9]+)?$') {
                throw 'YunPin has a pending system DLL rename. Use a controlled reboot and recover the same transaction; no new installation attempt is safe.'
            }
        }
    }
}

function Stop-YunPinPackageWriters {
    param([object]$Prior)
    $task = Get-ScheduledTask -TaskName 'YunPinSyncAgent' -ErrorAction SilentlyContinue
    if ($null -ne $task) {
        if ($task.Actions.Count -ne 1 -or -not (
            ($task.Actions[0].Execute -ceq $Prior.resident -and $task.Actions[0].Arguments -ceq '--interval 1m') -or
            ($task.Actions[0].Execute -ceq $Prior.agent -and $task.Actions[0].Arguments -ceq 'run --interval 1m'))) {
            throw 'An unrecognized task replaced the installation task; recovery will not touch it.'
        }
        Disable-ScheduledTask -TaskName 'YunPinSyncAgent' | Out-Null
        Stop-ScheduledTask -TaskName 'YunPinSyncAgent'
    }
    if (Test-Path -LiteralPath $Prior.server -PathType Leaf) {
        Invoke-CheckedExecutable -FilePath $Prior.server -Arguments @('/quit')
    }
    $executables = @($Prior.server, (Join-Path $Prior.current 'YunPinDeployer.exe'), $Prior.agent, $Prior.resident)
    foreach ($executable in $executables) {
        $processes = @(Get-CimInstance Win32_Process -Filter ("Name = '" + [IO.Path]::GetFileName($executable) + "'") -ErrorAction Stop |
            Where-Object { $_.ExecutablePath -eq $executable })
        foreach ($process in $processes) {
            Stop-Process -Id $process.ProcessId -Force -ErrorAction Stop
            Wait-Process -Id $process.ProcessId -Timeout 15 -ErrorAction SilentlyContinue
        }
        if (@(Get-CimInstance Win32_Process -Filter ("Name = '" + [IO.Path]::GetFileName($executable) + "'") -ErrorAction Stop |
            Where-Object { $_.ExecutablePath -eq $executable }).Count -ne 0) {
            throw 'An installation writer did not stop; recovery must not overwrite its data.'
        }
    }
}

function Restore-YunPinPackageWriters {
    param([object]$Prior)
    Unregister-ScheduledTask -TaskName 'YunPinSyncAgent' -Confirm:$false -ErrorAction SilentlyContinue
    if ($null -ne $Prior.taskXml) {
        Register-ScheduledTask -TaskName 'YunPinSyncAgent' -Xml $Prior.taskXml -Force | Out-Null
        if (-not $Prior.taskEnabled) { Disable-ScheduledTask -TaskName 'YunPinSyncAgent' | Out-Null }
        if ($Prior.taskRunning) { Start-ScheduledTask -TaskName 'YunPinSyncAgent' }
    }
    if ($Prior.serverRunning) { Start-Process -FilePath $Prior.server | Out-Null }
}

function Restore-YunPinPackage {
    param([string]$TransactionRoot, [object]$Journal)
    $prior = Import-Clixml -LiteralPath (Join-Path $TransactionRoot 'prior.clixml')
    Stop-YunPinPackageWriters -Prior $prior
    if ($Journal.stage -in @('freeze', 'backup')) {
        # No installed file or registration has been replaced in these stages.
        Restore-YunPinPackageWriters -Prior $prior
        return
    }
    Assert-YunPinNoPendingDllRenames
    Assert-YunPinReplaceableDlls -Paths $prior.systemDlls
    $registrationStarted = $Journal.stage -in @('registration', 'deploy', 'agent', 'state', 'resume') -or $Journal.completed -contains 'registration'
    if ($registrationStarted) {
        # Only the new YunPin TSF identity is unregistered; no unrelated IME.
        foreach ($registration in $prior.registrations) {
            if (Test-Path -LiteralPath $registration.dll -PathType Leaf) {
                Invoke-CheckedExecutable -FilePath $registration.regsvr -Arguments @('/s', '/u', $registration.dll)
            }
        }
    }
    Restore-YunPinPathBackups -Entries $prior.pathBackups -QuarantineRoot (Join-Path $TransactionRoot 'failed-state')
    if ($registrationStarted -and $prior.hadRuntime) {
        foreach ($registration in $prior.registrations) {
            Invoke-CheckedExecutable -FilePath $registration.regsvr -Arguments @('/s', $registration.dll)
        }
    }
    Restore-YunPinRegistryBackups -Entries $prior.registryBackups
    foreach ($entry in $prior.runValues) {
        if ($entry.existed) {
            New-ItemProperty -LiteralPath $prior.runKey -Name $entry.name -Value $entry.value -PropertyType $entry.kind -Force | Out-Null
        } else {
            Remove-ItemProperty -LiteralPath $prior.runKey -Name $entry.name -ErrorAction SilentlyContinue
        }
    }
    Restore-YunPinPackageWriters -Prior $prior
}

function Read-YunPinStrictUtf8File {
    param(
        [Parameter(Mandatory = $true)][string]$Path
    )
    $strictUtf8 = New-Object Text.UTF8Encoding($false, $true)
    try {
        $content = [IO.File]::ReadAllText($Path, $strictUtf8)
    } catch [Text.DecoderFallbackException] {
        throw "YunPin text file is not valid UTF-8: $Path"
    }
    if ($content.Contains([char]0xfffd)) {
        throw "YunPin text file contains the Unicode replacement character: $Path"
    }
    return $content
}

function Get-YunPinBooleanOptIn {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Name
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return $false
    }
    $content = Read-YunPinStrictUtf8File -Path $Path
    $key = [regex]::Escape($Name)
    $truePattern = '(?m)^[ \t]*"' + $key + '"[ \t]*:[ \t]*true[ \t]*(?:#[^\r\n]*)?\r?$'
    $falsePattern = '(?m)^[ \t]*"' + $key + '"[ \t]*:[ \t]*false[ \t]*(?:#[^\r\n]*)?\r?$'
    return (
        [regex]::Matches($content, $truePattern).Count -eq 1 -and
        [regex]::Matches($content, $falsePattern).Count -eq 0
    )
}

function Preserve-YunPinBooleanOptIns {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][bool]$PrivateCandidates,
        [Parameter(Mandatory = $true)][bool]$SessionLearning
    )
    if (-not $PrivateCandidates -and -not $SessionLearning) {
        return
    }
    $content = Read-YunPinStrictUtf8File -Path $Path
    $originalContent = $content
    foreach ($choice in @(
        @{ Name = 'yunpin/enabled'; Preserve = $PrivateCandidates },
        @{ Name = 'yunpin/session_learning'; Preserve = $SessionLearning }
    )) {
        if (-not $choice.Preserve) {
            continue
        }
        if (Get-YunPinBooleanOptIn -Path $Path -Name $choice.Name) {
            continue
        }
        $key = [regex]::Escape([string]$choice.Name)
        $falsePattern = '(?m)^(?<prefix>[ \t]*"' + $key + '"[ \t]*:[ \t]*)false(?<suffix>[ \t]*(?:#[^\r\n]*)?\r?)$'
        if ([regex]::Matches($content, $falsePattern).Count -ne 1) {
            throw "Packaged overlay does not contain one disabled $($choice.Name) setting."
        }
        $content = [regex]::Replace($content, $falsePattern, '${prefix}true${suffix}')
    }
    # A retained overlay already expresses its choices. Do not normalize its
    # encoding/BOM or rewrite it merely because preservation was requested.
    if ($content -ceq $originalContent) { return }

    $attempt = [guid]::NewGuid().ToString('N')
    $temporary = $Path + '.preserve-' + $attempt + '.tmp'
    $metadataBackup = $Path + '.preserve-' + $attempt + '.bak'
    try {
        [IO.File]::WriteAllText($temporary, $content, (New-Object Text.UTF8Encoding($false, $true)))
        if ((Read-YunPinStrictUtf8File -Path $temporary) -cne $content) {
            throw "UTF-8 overlay staging did not preserve the decoded configuration exactly."
        }
        [IO.File]::Replace($temporary, $Path, $metadataBackup, $true)
    } finally {
        Remove-Item -LiteralPath $temporary, $metadataBackup -Force -ErrorAction SilentlyContinue
    }
    if (($PrivateCandidates -and -not (Get-YunPinBooleanOptIn -Path $Path -Name 'yunpin/enabled')) -or
        ($SessionLearning -and -not (Get-YunPinBooleanOptIn -Path $Path -Name 'yunpin/session_learning'))) {
        throw "Existing YunPin opt-in settings were not preserved."
    }
}

if (-not $AcceptUnsignedDevelopmentBuild -and -not $RecoverTransaction) {
    throw "This archive is unsigned and for development only. Re-run with -AcceptUnsignedDevelopmentBuild after reading README.txt."
}
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT -or -not [Environment]::Is64BitOperatingSystem) {
    throw "YunPin Windows preview requires 64-bit Windows"
}
$windowsVersion = [Environment]::OSVersion.Version
if ($windowsVersion.Major -lt 10 -or $windowsVersion.Build -lt 19045) {
    throw "YunPin Windows preview requires Windows 10 22H2 (build 19045) or newer"
}
if (-not [Environment]::Is64BitProcess) { throw 'Run this installer in 64-bit PowerShell so system DLL paths and registry views are unambiguous.' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run the package transaction elevated as the signed-in user; no target has been changed.'
}
$installMutex = New-Object Threading.Mutex($false, 'Global\YunPinIMEPreviewInstaller')
try { $ownsInstallMutex = $installMutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $ownsInstallMutex = $true }
if (-not $ownsInstallMutex) { $installMutex.Dispose(); throw 'Another YunPin package transaction is active.' }
try {
if ([string]::IsNullOrWhiteSpace($InstallRoot)) { $InstallRoot = Join-Path $env:LOCALAPPDATA 'Programs\YunPinIME\Preview' }
$InstallRoot = [IO.Path]::GetFullPath($InstallRoot)
Assert-YunPinSafeTree -Path $InstallRoot
$transactionsRoot = Join-Path $InstallRoot 'transactions'
$unfinished = @()
if (Test-Path -LiteralPath $transactionsRoot -PathType Container) {
    foreach ($directory in Get-ChildItem -LiteralPath $transactionsRoot -Directory) {
        $existingJournal = Join-Path $directory.FullName 'journal.json'
        if (Test-Path -LiteralPath $existingJournal -PathType Leaf) {
            $receipt = Get-Content -LiteralPath $existingJournal -Raw | ConvertFrom-Json
            if ($receipt.state -notin @('SUCCEEDED', 'ROLLED_BACK')) { $unfinished += [pscustomobject]@{ root = $directory.FullName; journal = $receipt; path = $existingJournal } }
        }
    }
}
if ($unfinished.Count -gt 0 -or $RecoverTransaction) {
    if (-not $RecoverTransaction -or $unfinished.Count -ne 1) {
        throw 'An unfinished package transaction requires explicit recovery; do not start a fresh installation.'
    }
    $recovery = $unfinished[0]
    $saved = Import-Clixml -LiteralPath (Join-Path $recovery.root 'prior.clixml')
    if ($saved.ownerSid -cne $identity.User.Value -or $saved.installRoot -cne $InstallRoot) { throw 'Transaction owner or installation root differs.' }
    try {
        Restore-YunPinPackage -TransactionRoot $recovery.root -Journal $recovery.journal
        $recovery.journal.state = 'ROLLED_BACK'
        Write-YunPinInstallJournal -Path $recovery.path -Journal $recovery.journal
    } catch {
        $recovery.journal.state = 'RECOVERY_REQUIRED'
        Write-YunPinInstallJournal -Path $recovery.path -Journal $recovery.journal
        throw
    }
    Write-Host 'The original package transaction was rolled back; retained evidence was not deleted.'
    return
}

$bundleRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$bundleManifest = Assert-BundleManifest -BundleRoot $bundleRoot
$metadata = Get-Content -LiteralPath (Join-Path $bundleRoot "BUILD-METADATA.json") -Raw | ConvertFrom-Json
if ($metadata.signed -ne $false -or $metadata.productionReady -ne $false -or $metadata.privateCandidateSnapshotEnabled -ne $false) {
    throw "Unexpected development-preview metadata"
}
$privateConfig = Read-YunPinStrictUtf8File -Path (Join-Path $bundleRoot "rime-data\rime_ice.custom.yaml")
if ($privateConfig -notmatch '(?m)^\s*"yunpin/enabled": false\s*$') {
    throw "Private candidate snapshot must remain disabled in this preview"
}
if ($privateConfig -notmatch '(?m)^\s*"yunpin/short_input_guard": true\s*$') {
    throw "Short-input upstream guard must remain enabled in this preview"
}
if ($privateConfig -notmatch '(?m)^\s*"yunpin/session_learning": false\s*$') {
    throw "Session learning must remain disabled until the Windows secure-input gate passes"
}

if ([string]::IsNullOrWhiteSpace($InstallRoot)) {
    $InstallRoot = Join-Path $env:LOCALAPPDATA "Programs\YunPinIME\Preview"
}
if ([string]::IsNullOrWhiteSpace($UserDataRoot)) {
    $UserDataRoot = Join-Path $env:APPDATA "YunPin\Rime"
}
$InstallRoot = [IO.Path]::GetFullPath($InstallRoot)
$UserDataRoot = [IO.Path]::GetFullPath($UserDataRoot)
if ($InstallRoot.StartsWith('\\') -or $UserDataRoot.StartsWith('\\')) { throw 'Package transactions require local volume paths, not network/device namespaces.' }
$timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ") + '-' + [guid]::NewGuid().ToString('N')
$incoming = Join-Path $InstallRoot ("incoming-" + [guid]::NewGuid().ToString("N"))
$current = Join-Path $InstallRoot "current"
$backupRoot = Join-Path $InstallRoot "previous"
$previous = Join-Path $backupRoot $timestamp
$userBackup = Join-Path $UserDataRoot ("yunpin-preview-backups\" + $timestamp)
$existingRimeOverlay = Join-Path $UserDataRoot "rime_ice.custom.yaml"
$preservePrivateCandidates = Get-YunPinBooleanOptIn -Path $existingRimeOverlay -Name 'yunpin/enabled'
$preserveSessionLearning = Get-YunPinBooleanOptIn -Path $existingRimeOverlay -Name 'yunpin/session_learning'
$previousSyncTask = Get-ScheduledTask -TaskName 'YunPinSyncAgent' -ErrorAction SilentlyContinue
$restoreSyncEnabled = $null -ne $previousSyncTask -and $previousSyncTask.State.ToString() -cne 'Disabled'
$restoreSyncRunning = $null -ne $previousSyncTask -and $previousSyncTask.State.ToString() -ceq 'Running'
$hadCurrentRuntime = Test-Path -LiteralPath $current -PathType Container
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$serverWasRunning = @(Get-CimInstance Win32_Process -Filter "Name = 'YunPinServer.exe'" -ErrorAction Stop |
    Where-Object { $_.ExecutablePath -eq (Join-Path $current 'YunPinServer.exe') }).Count -gt 0
$supportRoot = Join-Path $InstallRoot 'support'
$syncBundleRoot = Join-Path $bundleRoot 'sync-agent'
$syncSupportRoot = Join-Path $supportRoot 'sync-agent'
$setup = Join-Path $current 'YunPinSetup.exe'
$deployer = Join-Path $current 'YunPinDeployer.exe'
$server = Join-Path $current 'YunPinServer.exe'
$syncBin = Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) 'YunPinIME\sync\bin'
$interactiveAgents = @(Get-CimInstance Win32_Process -Filter "Name = 'yunpin-sync-agent.exe'" -ErrorAction Stop |
    Where-Object { $_.ExecutablePath -eq (Join-Path $syncBin 'yunpin-sync-agent.exe') })
$legacyResident = $null -ne $previousSyncTask -and $previousSyncTask.Actions.Count -eq 1 -and
    $previousSyncTask.Actions[0].Execute -ceq (Join-Path $syncBin 'yunpin-sync-agent.exe') -and
    $previousSyncTask.Actions[0].Arguments -ceq 'run --interval 1m' -and $restoreSyncRunning
if ($interactiveAgents.Count -gt $(if ($legacyResident) { 1 } else { 0 })) {
    throw 'An interactive sync operation is active; finish it before installing. No credential operation will be interrupted.'
}
$systemDlls = @((Join-Path $env:SystemRoot 'System32\yunpin.dll'), (Join-Path $env:SystemRoot 'SysWOW64\yunpin.dll'))
if ($hadCurrentRuntime -and -not (Test-Path -LiteralPath $setup -PathType Leaf)) { throw 'The existing current directory is not a recognized YunPin runtime.' }
if (-not $hadCurrentRuntime -and @($systemDlls | Where-Object { Test-Path -LiteralPath $_ }).Count -gt 0) { throw 'Existing YunPin system DLLs without a current runtime require recovery, not a fresh installation.' }
Assert-YunPinReplaceableDlls -Paths $systemDlls
Assert-YunPinNoPendingDllRenames
foreach ($path in @($InstallRoot, $UserDataRoot, $syncBin)) { Assert-YunPinSafeTree -Path $path }
if ($InstallRoot.StartsWith($UserDataRoot.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase) -or
    $UserDataRoot.StartsWith($InstallRoot.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase) -or $InstallRoot -ieq $UserDataRoot -or
    $InstallRoot.TrimEnd('\') -ieq [IO.Path]::GetPathRoot($InstallRoot).TrimEnd('\') -or
    $UserDataRoot.TrimEnd('\') -ieq [IO.Path]::GetPathRoot($UserDataRoot).TrimEnd('\')) { throw 'Installation and user-data roots must be separate, bounded directories.' }
if ($null -ne $previousSyncTask) {
    $knownAgent = Join-Path $syncBin 'yunpin-sync-agent.exe'
    $knownResident = Join-Path $syncBin 'yunpin-sync-resident.exe'
    if ($previousSyncTask.Actions.Count -ne 1 -or -not (
        ($previousSyncTask.Actions[0].Execute -ceq $knownResident -and $previousSyncTask.Actions[0].Arguments -ceq '--interval 1m') -or
        ($previousSyncTask.Actions[0].Execute -ceq $knownAgent -and $previousSyncTask.Actions[0].Arguments -ceq 'run --interval 1m'))) {
        throw 'Refusing to freeze or replace an unrecognized sync task.'
    }
}
$registryTargets = @()
$clsid = '{1C4FBFE5-AA8C-43A4-A272-A5ECE54D7BAB}'
foreach ($view in @('Registry32', 'Registry64')) {
    foreach ($hive in @('LocalMachine', 'CurrentUser')) {
        foreach ($path in @('Software\YunPin\IME', ('Software\Classes\CLSID\' + $clsid), ('Software\Microsoft\CTF\TIP\' + $clsid))) {
            $registryTargets += [pscustomobject]@{ hive = $hive; view = $view; path = $path }
        }
    }
    $registryTargets += [pscustomobject]@{ hive = 'LocalMachine'; view = $view; path = 'Software\Microsoft\Windows\Windows Error Reporting\LocalDumps\YunPinServer.exe' }
}
$runValues = @()
foreach ($name in @('YunPinIMEPreview', 'YunPinSyncAgent')) {
    $value = Get-ItemProperty -LiteralPath $runKey -Name $name -ErrorAction SilentlyContinue
    $exists = $null -ne $value -and $value.PSObject.Properties.Name -contains $name
    $runValues += [pscustomobject]@{ name = $name; existed = $exists; value = $(if ($exists) { $value.$name } else { $null }); kind = $(if ($exists) { (Get-Item -LiteralPath $runKey).GetValueKind($name).ToString() } else { 'String' }) }
}
$prior = [pscustomobject]@{
    ownerSid = $identity.User.Value; installRoot = $InstallRoot; current = $current; server = $server
    agent = (Join-Path $syncBin 'yunpin-sync-agent.exe'); resident = (Join-Path $syncBin 'yunpin-sync-resident.exe')
    hadRuntime = $hadCurrentRuntime; taskEnabled = $restoreSyncEnabled; taskRunning = $restoreSyncRunning
    taskXml = $(if ($null -ne $previousSyncTask) { Export-ScheduledTask -TaskName 'YunPinSyncAgent' } else { $null })
    serverRunning = $serverWasRunning; runKey = $runKey; runValues = $runValues
    registryBackups = @(Save-YunPinRegistryBackups -Targets $registryTargets); pathBackups = @(); systemDlls = $systemDlls
    registrations = @(
        [pscustomobject]@{ regsvr = (Join-Path $env:SystemRoot 'System32\regsvr32.exe'); dll = $systemDlls[0] },
        [pscustomobject]@{ regsvr = (Join-Path $env:SystemRoot 'SysWOW64\regsvr32.exe'); dll = $systemDlls[1] }
    )
}
foreach ($entry in $prior.registryBackups) {
    if ($entry.path -ceq 'Software\YunPin\IME' -and $null -ne $entry.tree) {
        foreach ($value in $entry.tree.values) {
            if (($value.name -ceq 'WeaselRoot' -and $value.data -ine $current) -or
                ($value.name -ceq 'RimeUserDir' -and $value.data -ine $UserDataRoot)) {
                throw 'A different YunPin runtime or user-data root is registered; explicit migration is required.'
            }
        }
    }
}
$transactionRoot = Join-Path $transactionsRoot $timestamp
New-Item -ItemType Directory -Path $transactionRoot -Force | Out-Null
Invoke-CheckedExecutable -FilePath (Join-Path $env:SystemRoot 'System32\icacls.exe') -Arguments @(
    $transactionRoot, '/inheritance:r', '/grant:r', ('*' + $identity.User.Value + ':(OI)(CI)F'), '/grant:r', '*S-1-5-18:(OI)(CI)F'
)
Write-YunPinPriorState -Path (Join-Path $transactionRoot 'prior.clixml') -Prior $prior
Copy-Item -LiteralPath $MyInvocation.MyCommand.Path -Destination (Join-Path $transactionRoot 'Install-Preview.ps1')
$stages = @(
    @{ Name = 'freeze'; Action = { Stop-YunPinPackageWriters -Prior $prior; Assert-YunPinReplaceableDlls -Paths $systemDlls } },
    @{ Name = 'backup'; Action = {
        $paths = @($current, $supportRoot, (Join-Path $InstallRoot 'install-state.json'), $prior.agent, $prior.resident)
        $paths += @((Join-Path $UserDataRoot 'build'), (Join-Path $UserDataRoot 'installation.yaml'), (Join-Path $UserDataRoot 'user.yaml'))
        $overlayRoot = Join-Path $bundleRoot 'rime-data'
        $prefix = [IO.Path]::GetFullPath($overlayRoot).TrimEnd('\') + '\'
        foreach ($file in Get-ChildItem -LiteralPath $overlayRoot -File -Recurse) { $paths += Join-Path $UserDataRoot $file.FullName.Substring($prefix.Length) }
        $paths += $systemDlls
        $prior.pathBackups = @(Save-YunPinPathBackups -Paths ($paths | Select-Object -Unique) -BackupRoot (Join-Path $transactionRoot 'backups'))
        Write-YunPinPriorState -Path (Join-Path $transactionRoot 'prior.clixml') -Prior $prior
    } },
    @{ Name = 'support'; Action = {
    New-Item -ItemType Directory -Path $InstallRoot, $backupRoot, $UserDataRoot -Force | Out-Null
    New-Item -ItemType Directory -Path $supportRoot -Force | Out-Null
    foreach ($supportFile in @(
        "Install-Preview.ps1", "Uninstall-Preview.ps1", "README.txt",
        "BUILD-METADATA.json", "MANIFEST.sha256"
    )) {
        Copy-Item -LiteralPath (Join-Path $bundleRoot $supportFile) -Destination $supportRoot -Force
    }
    $syncBundleRoot = Join-Path $bundleRoot "sync-agent"
    $syncSupportRoot = Join-Path $supportRoot "sync-agent"
    New-Item -ItemType Directory -Path $syncSupportRoot -Force | Out-Null
    Get-ChildItem -LiteralPath $syncBundleRoot -File | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $syncSupportRoot -Force
    }
    New-Item -ItemType Directory -Path $incoming -Force | Out-Null
    Get-ChildItem -LiteralPath (Join-Path $bundleRoot "runtime") -Force | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $incoming -Recurse -Force
    }

    } },
    @{ Name = 'runtime'; Action = {
    if (Test-Path $current -PathType Container) {
        Move-Item -LiteralPath $current -Destination $previous
    }
    Move-Item -LiteralPath $incoming -Destination $current
    } },
    @{ Name = 'overlay'; Action = {
    Copy-OverlayWithBackup -SourceRoot (Join-Path $bundleRoot "rime-data") -DestinationRoot $UserDataRoot -BackupRoot $userBackup
    Preserve-YunPinBooleanOptIns -Path (Join-Path $UserDataRoot "rime_ice.custom.yaml") `
        -PrivateCandidates $preservePrivateCandidates -SessionLearning $preserveSessionLearning
    } },
    @{ Name = 'registration'; Action = {
    $setup = Join-Path $current "YunPinSetup.exe"
    $deployer = Join-Path $current "YunPinDeployer.exe"
    $server = Join-Path $current "YunPinServer.exe"
    Invoke-CheckedExecutable -FilePath $setup -Arguments @(('/userdir:' + $UserDataRoot))
    Invoke-CheckedExecutable -FilePath $setup -Arguments @('/du')
    Invoke-CheckedExecutable -FilePath $setup -Arguments @('/s')
    Assert-YunPinNoPendingDllRenames
    Set-YunPinMachineRegistry64 -RuntimeRoot $current
    } },
    @{ Name = 'deploy'; Action = {
    Invoke-CheckedExecutable -FilePath $deployer -Arguments @('/deploy')
    } },
    @{ Name = 'agent'; Action = {
    $syncInstaller = Join-Path $syncSupportRoot "Install-SyncAgent.ps1"
    $syncVerifier = Join-Path $syncSupportRoot "Verify-SyncAgent.ps1"
    $syncAgent = Join-Path $syncSupportRoot "yunpin-sync-agent.exe"
    $syncResident = Join-Path $syncSupportRoot "yunpin-sync-resident.exe"
    $settingsLauncher = Join-Path $syncSupportRoot "yunpin-settings.exe"
    $replayLab = Join-Path $syncSupportRoot "yunpin-replay-lab.exe"
    $syncManifestPath = "sync-agent/yunpin-sync-agent.exe"
    $syncResidentManifestPath = "sync-agent/yunpin-sync-resident.exe"
    $settingsManifestPath = "sync-agent/yunpin-settings.exe"
    $replayManifestPath = "sync-agent/yunpin-replay-lab.exe"
    if (-not $bundleManifest.ContainsKey($syncManifestPath)) {
        throw "Public sync agent is absent from the verified bundle manifest."
    }
    if (-not $bundleManifest.ContainsKey($syncResidentManifestPath)) {
        throw "Windowless sync resident is absent from the verified bundle manifest."
    }
    if (-not $bundleManifest.ContainsKey($settingsManifestPath) -or
        -not (Test-Path -LiteralPath $settingsLauncher -PathType Leaf)) {
        throw "Windowless settings launcher is absent from the verified bundle."
    }
    if (-not $bundleManifest.ContainsKey($replayManifestPath) -or
        -not (Test-Path -LiteralPath $replayLab -PathType Leaf)) {
        throw "Replay Lab CLI is absent from the verified bundle."
    }
    & $syncInstaller -AgentPath $syncAgent -ExpectedSha256 $bundleManifest[$syncManifestPath] `
        -ResidentPath $syncResident -ResidentExpectedSha256 $bundleManifest[$syncResidentManifestPath] -LeaveDisabled
    & $syncVerifier
    } },
    @{ Name = 'state'; Action = {
    $state = [ordered]@{
        schemaVersion = 1
        installedAtUtc = [DateTime]::UtcNow.ToString("o")
        currentRuntime = $current
        userData = $UserDataRoot
        userOverlayBackup = $userBackup
        previousRuntime = $(if (Test-Path $previous) { $previous } else { $null })
        registry64Runtime = $current
        unsignedDevelopmentBuild = $true
        syncAgentRegistration = $(if ($restoreSyncEnabled) { 'enabled' } else { 'disabled' })
    }
    $state | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $InstallRoot "install-state.json") -Encoding UTF8
    } },
    @{ Name = 'resume'; Action = {
    Restore-YunPinServerStartup -WasInstalled $hadCurrentRuntime -WasRunning $serverWasRunning -RunKey $runKey -Server $server
    if ($restoreSyncEnabled) {
        Enable-ScheduledTask -TaskName 'YunPinSyncAgent' | Out-Null
        if ($restoreSyncRunning) { Start-ScheduledTask -TaskName 'YunPinSyncAgent' }
    }
    } }
)
Invoke-YunPinInstallTransaction -JournalPath (Join-Path $transactionRoot 'journal.json') -Stages $stages -Rollback {
    param($journal)
    Restore-YunPinPackage -TransactionRoot $transactionRoot -Journal $journal
}

Write-Host "YunPin Windows development preview installed."
Write-Host "Runtime: $current"
Write-Host "User data: $UserDataRoot"
Write-Host 'Existing custom overlays were retained; fresh-install private candidates remain disabled.'
Write-Host ('YunPinSyncAgent registration: ' + $(if ($restoreSyncEnabled) { 'enabled' } else { 'disabled' }) + '; no new account or pairing was performed.')
Write-Host "Private transaction journal and rollback material retained: $transactionRoot"
} finally {
    $installMutex.ReleaseMutex()
    $installMutex.Dispose()
}
