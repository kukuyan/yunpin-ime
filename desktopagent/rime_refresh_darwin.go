// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func newFixedRimeMaintenanceInvoker() (fixedRimeMaintenanceInvoker, error) {
	const host = "/Library/Input Methods/YunPin.app/Contents/MacOS/YunPin"
	return fixedRimeMaintenanceInvoker{
		RequiresAck: true,
		Invoke: func(ctx context.Context, nonce string) error {
			if !safeMaintenanceNonce(nonce) {
				return errors.New("Rime maintenance nonce is invalid")
			}
			info, err := os.Lstat(host)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 == 0 {
				return errors.New("fixed YunPin host is not an executable regular file")
			}
			command := exec.CommandContext(ctx, host, "--sync", nonce)
			command.Stdin, command.Stdout, command.Stderr = nil, nil, nil
			return command.Run()
		},
	}, nil
}
