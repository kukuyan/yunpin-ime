// SPDX-License-Identifier: Apache-2.0
package desktopagent

import "errors"

var ErrAlreadyRunning = errors.New("YunPin sync agent is already running for this user")

type processLock interface {
	Release() error
}
