// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const (
	windowsRimeMaintenanceUnavailableExitCode = 69
	windowsRimeMaintenanceBusyExitCode        = 75
)

func windowsRimeMaintenanceExitCodeError(code int) error {
	switch code {
	case 0:
		return nil
	case windowsRimeMaintenanceUnavailableExitCode:
		return ErrRimeMaintenanceUnavailable
	case windowsRimeMaintenanceBusyExitCode:
		return ErrRimeMaintenanceBusy
	default:
		return fmt.Errorf("fixed YunPin deployer exited with code %d", code)
	}
}

func invokeFixedWindowsRimeMaintenance(ctx context.Context, path, nonce string) error {
	if !safeMaintenanceNonce(nonce) {
		return errors.New("Rime maintenance nonce is invalid")
	}
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("fixed YunPin deployer path must be absolute")
	}
	if _, err := inspectWindowsPathChain(path, false); err != nil {
		return errors.New("fixed YunPin deployer path contains a reparse point or unsafe component")
	}
	info, err := os.Lstat(path)
	// Windows reports ordinary executable files as 0666, without POSIX execute
	// bits. The fixed .exe path, regular-file type, no-reparse validation and
	// CreateProcess result are the applicable checks here.
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("fixed YunPin deployer is not a regular non-reparse file")
	}
	command := exec.CommandContext(ctx, filepath.Clean(path), "/sync")
	command.Stdin, command.Stdout, command.Stderr = nil, nil, nil
	err = command.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		// Exit status is read from the exact child process started from the fixed
		// platform path. The closed contract distinguishes initial IPC
		// unavailability from an authenticated busy response; no writable
		// acknowledgement file or PATH lookup participates.
		return windowsRimeMaintenanceExitCodeError(exitError.ExitCode())
	}
	return err
}

func newFixedRimeMaintenanceInvoker() (fixedRimeMaintenanceInvoker, error) {
	root, err := knownWindowsFolder(windows.FOLDERID_LocalAppData)
	if err != nil {
		return fixedRimeMaintenanceInvoker{}, fmt.Errorf("resolve fixed YunPin deployer location: %w", err)
	}
	deployer := filepath.Join(root, "Programs", "YunPinIME", "Preview", "current", "YunPinDeployer.exe")
	return fixedRimeMaintenanceInvoker{Invoke: func(ctx context.Context, nonce string) error {
		return invokeFixedWindowsRimeMaintenance(ctx, deployer, nonce)
	}}, nil
}
