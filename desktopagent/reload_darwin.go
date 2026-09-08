// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// DefaultReloadHook waits for the InputMethodKit host's actual engine ACK.
// Old hosts safely fail capability discovery; an unknown argument must never
// accidentally launch a second input-method host.
func DefaultReloadHook() func(context.Context) error {
	const host = "/Library/Input Methods/YunPin.app/Contents/MacOS/YunPin"
	return func(ctx context.Context) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		directory := filepath.Join(home, "Library", "Application Support", "YunPin", "Sync")
		return runDarwinReload(ctx, host, directory)
	}
}

func runDarwinReload(ctx context.Context, host, directory string) error {
	request, snapshot := ctx.Value(snapshotReloadContextKey{}).(snapshotReloadRequest)
	if !snapshot {
		// This explicit marker is set only by ApplyGuardSettings. Missing
		// snapshot identity must never silently downgrade into a notification.
		if ctx.Value(settingsDeployContextKey{}) == true {
			return executableReloadHook(host, "--reload")(ctx)
		}
		return errors.New("snapshot reload request identity is missing")
	}
	return reloadSnapshotWithAck(ctx, directory, request, func(ctx context.Context, nonce string) error {
		return invokeDarwinSnapshotReload(ctx, host, nonce)
	})
}

type reloadHelpOutput struct{ bytes.Buffer }

func (output *reloadHelpOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 16384 {
		return 0, errors.New("host capability response exceeds bound")
	}
	return output.Buffer.Write(data)
}

func invokeDarwinSnapshotReload(ctx context.Context, host, nonce string) error {
	if !filepath.IsAbs(host) || !safeMaintenanceNonce(nonce) {
		return errors.New("snapshot reload invocation identity is invalid")
	}
	info, err := os.Lstat(host)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !platformReloadExecutableOK(info) {
		return errors.New("fixed YunPin host is not an executable regular file")
	}
	// --help is understood by every old supported host and has no
	// registration, deploy, credential, or server-start side effect.
	help := exec.CommandContext(ctx, host, "--help")
	var output reloadHelpOutput
	help.Stdout = &output
	if err := help.Run(); err != nil || !bytes.Contains(output.Bytes(), []byte("--reload-snapshot <request nonce>")) {
		return errors.New("installed YunPin host does not support snapshot application acknowledgements")
	}
	return executableReloadHook(host, "--reload-snapshot", nonce)(ctx)
}
