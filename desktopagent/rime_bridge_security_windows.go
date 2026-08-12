// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import "errors"

func bridgePathComponentsOK(path string, directory bool) bool {
	_, err := inspectWindowsPathChain(path, directory)
	return err == nil
}

func hardenExistingPrivateDirectory(path string) error {
	if err := setAndVerifyPrivateWindowsPath(path, true); err != nil {
		return err
	}
	if !verifyPrivateWindowsPath(path, true) {
		return errors.New("private Windows directory could not be verified")
	}
	return nil
}
