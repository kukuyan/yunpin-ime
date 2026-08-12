// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"errors"

	"golang.org/x/sys/windows"
)

// hardenRimeBridgePreflightPath is the Windows counterpart of the Unix
// handle-bound crash-recovery hardening. It refuses a foreign owner before
// changing the protected, exact user+SYSTEM DACL.
func hardenRimeBridgePreflightPath(path string, directory bool) error {
	before, err := inspectWindowsPathChain(path, directory)
	if err != nil {
		return err
	}
	handle, err := openWindowsPathNoReparse(path,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER,
		windows.OPEN_EXISTING, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	identity, _, err := windowsIdentityForHandle(handle)
	if err != nil || identity.directory != directory || identity != before[len(before)-1].identity {
		return errors.New("Windows Rime preflight object changed or has an unexpected type")
	}
	security, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, defaulted, err := security.Owner()
	user, _, userErr := currentUserAndSystemSID()
	if err != nil || userErr != nil || owner == nil || defaulted || !owner.Equals(user) {
		return errors.New("Windows Rime preflight object is not explicitly owned by the current user")
	}
	if err := setPrivateWindowsSecurityOnHandle(handle, directory); err != nil {
		return err
	}
	protectedIdentity, err := validatePrivateWindowsHandle(handle, directory)
	if err != nil || protectedIdentity != identity {
		return errors.New("Windows Rime preflight object changed while its ACL was protected")
	}
	after, err := inspectWindowsPathChain(path, directory)
	if err != nil || !sameWindowsPathChain(before, after) || after[len(after)-1].identity != identity {
		return errors.New("Windows Rime preflight path changed while its ACL was protected")
	}
	return nil
}
