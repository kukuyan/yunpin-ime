// SPDX-License-Identifier: Apache-2.0
//go:build !darwin && !windows

package desktopagent

import "errors"

func newFixedRimeMaintenanceInvoker() (fixedRimeMaintenanceInvoker, error) {
	return fixedRimeMaintenanceInvoker{}, errors.New("fixed Rime maintenance is unsupported on this platform")
}
