// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

//go:embed install/windows/Enable-SyncAgent.ps1
var backgroundEnableScript []byte

func configureBackgroundCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func backgroundPowerShell() (string, error) {
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "WindowsPowerShell", "v1.0", "powershell.exe"), nil
}

func encodeBackgroundPowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, 2*len(units))
	for index, unit := range units {
		binary.LittleEndian.PutUint16(encoded[2*index:], unit)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

func readPlatformBackgroundStatus(ctx context.Context, paths Paths) (BackgroundStatus, error) {
	state := BackgroundStatus{Supported: true}
	executable, err := backgroundPowerShell()
	if err != nil {
		return state, err
	}
	// The script only serializes explicit scalar properties. In particular,
	// never serialize a Get-Content extended object on Windows PowerShell 5.1.
	script := `$ErrorActionPreference='Stop';[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false)
$task=Get-ScheduledTask -TaskName 'YunPinSyncAgent' -ErrorAction SilentlyContinue
if($null -eq $task){[Console]::Out.Write('{"supported":true,"installed":false}');exit 0}
$root=Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) 'YunPinIME\sync'
$exe=Join-Path $root 'bin\yunpin-sync-resident.exe'
if($task.Actions.Count -ne 1 -or $task.Actions[0].Execute -cne $exe -or $task.Actions[0].Arguments -cne '--interval 1m'){throw 'Unexpected task registration'}
$sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$matches=@(Get-CimInstance Win32_Process -Filter "Name = 'yunpin-sync-resident.exe'" -ErrorAction Stop|Where-Object {$_.ExecutablePath -ieq $exe}|Where-Object {(Invoke-CimMethod -InputObject $_ -MethodName GetOwnerSid -ErrorAction Stop).Sid -eq $sid})
$running=$matches.Count -eq 1
[pscustomobject]@{supported=$true;installed=$true;enabled=([string]$task.State -ne 'Disabled');running=$running}|ConvertTo-Json -Compress`
	encoded, err := backgroundCommand(ctx, executable, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeBackgroundPowerShell(script))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(encoded, &state); err != nil {
		return state, errors.New("background status response is invalid")
	}
	return state, nil
}

func enablePlatformBackground(ctx context.Context, paths Paths) error {
	executable, err := backgroundPowerShell()
	if err != nil {
		return err
	}
	return withBackgroundScript(paths, ".ps1", backgroundEnableScript, func(name string) error {
		_, err := backgroundCommand(ctx, executable, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", name)
		return err
	})
}
