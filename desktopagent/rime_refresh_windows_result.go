// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"errors"
	"fmt"
)

const (
	windowsRimeMaintenanceUnavailableExitCode   = 69
	windowsRimeMaintenanceProtocolErrorExitCode = 70
	windowsRimeMaintenanceBusyExitCode          = 75
)

var ErrRimeMaintenanceProtocol = errors.New("Rime host maintenance IPC protocol error")

func windowsRimeMaintenanceExitCodeError(code int) error {
	switch code {
	case 0:
		return nil
	case windowsRimeMaintenanceUnavailableExitCode:
		return ErrRimeMaintenanceUnavailable
	case windowsRimeMaintenanceProtocolErrorExitCode:
		return ErrRimeMaintenanceProtocol
	case windowsRimeMaintenanceBusyExitCode:
		return ErrRimeMaintenanceBusy
	default:
		return fmt.Errorf("fixed YunPin deployer exited with code %d", code)
	}
}
