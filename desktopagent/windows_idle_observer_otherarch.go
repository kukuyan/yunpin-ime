// SPDX-License-Identifier: Apache-2.0
//go:build windows && !amd64

package desktopagent

import "errors"

func observeWindowsIdle(uint32) (windowsIdleSample, error) {
	return windowsIdleSample{}, errors.New("session observation is unavailable on this Windows architecture")
}

func currentWindowsSessionID() (uint32, error) {
	return 0, errors.New("session identity is unavailable on this Windows architecture")
}
