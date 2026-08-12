// SPDX-License-Identifier: Apache-2.0
package desktopagent

import "errors"

var ErrAlreadyRunning = errors.New("YunPin sync agent is already running for this user")

type processLock interface {
	Release() error
}

// WithProcessLock serializes provisioning, one-shot sync, and the resident
// agent across processes. In particular, two prepare-account processes must
// never race after displaying two different recovery roots for one profile.
func WithProcessLock(path string, operation func() error) (err error) {
	if operation == nil {
		return errors.New("locked operation is required")
	}
	lock, err := acquireProcessLock(path)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()
	return operation()
}
