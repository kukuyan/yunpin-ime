// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// FILE_ALL_ACCESS after generic-rights expansion. Requiring the concrete
	// mask keeps GENERIC_ALL and accidentally broader/narrower ACL variants from
	// being accepted as equivalent private state.
	privateWindowsFullControl       windows.ACCESS_MASK = 0x001f01ff
	privateWindowsDirectoryACEFlags                     = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
)

type windowsFileIdentity struct {
	volumeSerial uint32
	fileIndexHi  uint32
	fileIndexLo  uint32
	createdHi    uint32
	createdLo    uint32
	directory    bool
}

type windowsPathComponent struct {
	path     string
	identity windowsFileIdentity
}

func currentUserAndSystemSID() (*windows.SID, *windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, nil, errors.New("current Windows user SID is unavailable")
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, err
	}
	return user.User.Sid, system, nil
}

func knownWindowsFolder(id *windows.KNOWNFOLDERID) (string, error) {
	path, err := windows.KnownFolderPath(id, windows.KF_FLAG_CREATE|windows.KF_FLAG_NO_ALIAS)
	if err != nil {
		return "", fmt.Errorf("resolve current-user Windows Known Folder: %w", err)
	}
	path = filepath.Clean(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("Windows Known Folder did not resolve to an absolute path")
	}
	if _, err := windowsAbsoluteComponents(path); err != nil {
		return "", fmt.Errorf("validate Windows Known Folder: %w", err)
	}
	return path, nil
}

func windowsAbsoluteComponents(path string) ([]string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return nil, errors.New("Windows private path must be an absolute filesystem path")
	}
	clean := filepath.Clean(path)
	lower := strings.ToLower(clean)
	// Device namespaces can bypass ordinary Win32 path-component semantics.
	// Known Folder paths never require them, so private state rejects them.
	if strings.HasPrefix(lower, `\\?\`) || strings.HasPrefix(lower, `\\.\`) {
		return nil, errors.New("Windows device-namespace paths are not allowed for private state")
	}
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return nil, errors.New("Windows private path has no volume")
	}
	root := volume + string(filepath.Separator)
	components := []string{root}
	rawVolume := filepath.VolumeName(path)
	for _, name := range strings.FieldsFunc(strings.TrimLeft(path[len(rawVolume):], `\/`), func(character rune) bool {
		return character == '\\' || character == '/'
	}) {
		if name == "." || name == ".." {
			return nil, errors.New("Windows private path contains traversal components")
		}
	}
	tail := strings.TrimLeft(clean[len(volume):], `\/`)
	if tail == "" {
		return components, nil
	}
	current := root
	for _, name := range strings.FieldsFunc(tail, func(character rune) bool {
		return character == '\\' || character == '/'
	}) {
		if !safeWindowsPathComponent(name) {
			return nil, errors.New("Windows private path contains an unsafe component")
		}
		current = filepath.Join(current, name)
		components = append(components, current)
	}
	if !strings.EqualFold(filepath.Clean(components[len(components)-1]), clean) {
		return nil, errors.New("Windows private path could not be decomposed canonically")
	}
	return components, nil
}

func safeWindowsPathComponent(name string) bool {
	if name == "" || name == "." || name == ".." || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, character := range name {
		if character < 0x20 || strings.ContainsRune(`<>:"|?*`, character) {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		(len(base) == 4 && ((strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9')) {
		return false
	}
	return true
}

func openWindowsPathNoReparse(path string, access uint32, disposition uint32, security *windows.SecurityAttributes) (windows.Handle, error) {
	encoded, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		encoded,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		security,
		disposition,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
}

func windowsIdentityForHandle(handle windows.Handle) (windowsFileIdentity, uint32, error) {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return windowsFileIdentity{}, 0, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return windowsFileIdentity{}, information.FileAttributes, errors.New("Windows private path contains a reparse point or junction")
	}
	identity := windowsFileIdentity{
		volumeSerial: information.VolumeSerialNumber,
		fileIndexHi:  information.FileIndexHigh,
		fileIndexLo:  information.FileIndexLow,
		createdHi:    information.CreationTime.HighDateTime,
		createdLo:    information.CreationTime.LowDateTime,
		directory:    information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
	}
	return identity, information.FileAttributes, nil
}

func finalWindowsPathForHandle(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 512)
	for {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if length < uint32(len(buffer)) {
			path := windows.UTF16ToString(buffer[:length])
			switch {
			case strings.HasPrefix(strings.ToLower(path), `\\?\unc\`):
				path = `\\` + path[len(`\\?\UNC\`):]
			case strings.HasPrefix(strings.ToLower(path), `\\?\`):
				path = path[len(`\\?\`):]
			}
			return filepath.Clean(path), nil
		}
		if length > 32768 {
			return "", errors.New("Windows final path exceeds the Win32 limit")
		}
		buffer = make([]uint16, length+1)
	}
}

func inspectWindowsPathChain(path string, targetDirectory bool) ([]windowsPathComponent, error) {
	components, err := windowsAbsoluteComponents(path)
	if err != nil {
		return nil, err
	}
	result := make([]windowsPathComponent, 0, len(components))
	for index, component := range components {
		handle, err := openWindowsPathNoReparse(component, windows.FILE_READ_ATTRIBUTES, windows.OPEN_EXISTING, nil)
		if err != nil {
			return nil, fmt.Errorf("open Windows path component %q without following reparse points: %w", component, err)
		}
		identity, _, identityErr := windowsIdentityForHandle(handle)
		closeErr := windows.CloseHandle(handle)
		if identityErr != nil {
			return nil, fmt.Errorf("inspect Windows path component %q: %w", component, identityErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		expectedDirectory := index < len(components)-1 || targetDirectory
		if identity.directory != expectedDirectory {
			return nil, fmt.Errorf("Windows path component %q has an unexpected object type", component)
		}
		result = append(result, windowsPathComponent{path: component, identity: identity})
	}
	return result, nil
}

func sameWindowsPathChain(left, right []windowsPathComponent) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(filepath.Clean(left[index].path), filepath.Clean(right[index].path)) ||
			left[index].identity != right[index].identity {
			return false
		}
	}
	return true
}

func validatePrivateWindowsSecurityDescriptor(sd *windows.SECURITY_DESCRIPTOR, directory bool) error {
	if sd == nil || !sd.IsValid() {
		return errors.New("Windows private object has no valid security descriptor")
	}
	user, system, err := currentUserAndSystemSID()
	if err != nil {
		return err
	}
	owner, ownerDefaulted, err := sd.Owner()
	if err != nil || owner == nil || ownerDefaulted || !owner.Equals(user) {
		return errors.New("Windows private object is not explicitly owned by the current user")
	}
	control, _, err := sd.Control()
	if err != nil {
		return errors.New("Windows private DACL control bits are unavailable")
	}
	if control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 ||
		control&(windows.SE_DACL_DEFAULTED|windows.SE_DACL_AUTO_INHERIT_REQ) != 0 {
		return fmt.Errorf("Windows private DACL is absent, defaulted, inheritance-requested, or not protected (control=0x%04x)", uint16(control))
	}
	// NTFS can retain SE_DACL_AUTO_INHERITED as a history/current-inheritance-
	// model marker even after a DACL is protected and replaced. It grants no
	// access. The effective inheritance boundary is enforced by
	// SE_DACL_PROTECTED and, below, by requiring every one of the exactly two
	// ACEs to be explicit with the exact role-specific AceFlags.
	dacl, daclDefaulted, err := sd.DACL()
	if err != nil || dacl == nil || daclDefaulted || dacl.AceCount != 2 {
		return errors.New("Windows private DACL must contain exactly two explicit ACEs")
	}
	expectedFlags := uint8(0)
	if directory {
		expectedFlags = privateWindowsDirectoryACEFlags
	}
	userAllowed, systemAllowed := false, false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil ||
			ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags != expectedFlags || ace.Mask != privateWindowsFullControl ||
			ace.Header.AceSize < uint16(unsafe.Offsetof(ace.SidStart))+4 {
			return errors.New("Windows private DACL has an unexpected ACE type, flags, or access mask")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || int(ace.Header.AceSize) != int(unsafe.Offsetof(ace.SidStart))+sid.Len() {
			return errors.New("Windows private DACL contains a malformed SID")
		}
		switch {
		case sid.Equals(user) && !userAllowed:
			userAllowed = true
		case sid.Equals(system) && !systemAllowed:
			systemAllowed = true
		default:
			return errors.New("Windows private DACL contains a duplicate or extra principal")
		}
	}
	if !userAllowed || !systemAllowed {
		return errors.New("Windows private DACL does not grant exact access to user and SYSTEM")
	}
	return nil
}

func validatePrivateWindowsHandle(handle windows.Handle, directory bool) (windowsFileIdentity, error) {
	identity, _, err := windowsIdentityForHandle(handle)
	if err != nil {
		return windowsFileIdentity{}, err
	}
	if identity.directory != directory {
		return windowsFileIdentity{}, errors.New("Windows private object type does not match its required role")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return windowsFileIdentity{}, err
	}
	if err := validatePrivateWindowsSecurityDescriptor(sd, directory); err != nil {
		return windowsFileIdentity{}, err
	}
	return identity, nil
}

func privateWindowsSecurityDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, *windows.ACL, error) {
	user, _, err := currentUserAndSystemSID()
	if err != nil {
		return nil, nil, nil, err
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;SY)(A;" + flags + ";FA;;;" + user.String() + ")")
	if err != nil {
		return nil, nil, nil, err
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil || dacl == nil || defaulted {
		return nil, nil, nil, errors.New("construct exact private Windows DACL")
	}
	return sd, user, dacl, nil
}

func setPrivateWindowsSecurityOnHandle(handle windows.Handle, directory bool) error {
	sd, user, dacl, err := privateWindowsSecurityDescriptor(directory)
	if err != nil {
		return err
	}
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user, nil, dacl, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		return err
	}
	_, err = validatePrivateWindowsHandle(handle, directory)
	return err
}

func setAndVerifyPrivateWindowsPath(path string, directory bool) error {
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
	if err != nil || identity != before[len(before)-1].identity {
		return errors.New("Windows private object changed before its ACL could be protected")
	}
	if err := setPrivateWindowsSecurityOnHandle(handle, directory); err != nil {
		return err
	}
	identityAfter, err := validatePrivateWindowsHandle(handle, directory)
	if err != nil || identityAfter != identity {
		return errors.New("Windows private object changed while its ACL was protected")
	}
	after, err := inspectWindowsPathChain(path, directory)
	if err != nil || !sameWindowsPathChain(before, after) || after[len(after)-1].identity != identity {
		return errors.New("Windows private path changed while its ACL was protected")
	}
	return nil
}

func verifyPrivateWindowsPath(path string, directory bool) bool {
	before, err := inspectWindowsPathChain(path, directory)
	if err != nil {
		return false
	}
	handle, err := openWindowsPathNoReparse(path, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
	if err != nil {
		return false
	}
	identity, validationErr := validatePrivateWindowsHandle(handle, directory)
	closeErr := windows.CloseHandle(handle)
	if validationErr != nil || closeErr != nil || identity != before[len(before)-1].identity {
		return false
	}
	after, err := inspectWindowsPathChain(path, directory)
	return err == nil && sameWindowsPathChain(before, after) && after[len(after)-1].identity == identity
}

func ensurePrivateWindowsDirectory(path string) error {
	components, err := windowsAbsoluteComponents(path)
	if err != nil {
		return err
	}
	for index, component := range components {
		handle, openErr := openWindowsPathNoReparse(component, windows.FILE_READ_ATTRIBUTES, windows.OPEN_EXISTING, nil)
		if openErr == nil {
			identity, _, inspectErr := windowsIdentityForHandle(handle)
			closeErr := windows.CloseHandle(handle)
			if inspectErr != nil {
				return fmt.Errorf("inspect Windows private directory component %q: %w", component, inspectErr)
			}
			if closeErr != nil {
				return closeErr
			}
			if !identity.directory {
				return fmt.Errorf("Windows private directory component %q is not a directory", component)
			}
			continue
		}
		if !errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(openErr, windows.ERROR_PATH_NOT_FOUND) {
			return fmt.Errorf("inspect Windows private directory component %q: %w", component, openErr)
		}
		if index == 0 {
			return fmt.Errorf("Windows private volume root is unavailable: %w", openErr)
		}
		if err := os.Mkdir(component, 0700); err != nil {
			return fmt.Errorf("create Windows private directory component %q: %w", component, err)
		}
		if err := setAndVerifyPrivateWindowsPath(component, true); err != nil {
			return fmt.Errorf("protect new Windows private directory component %q: %w", component, err)
		}
	}
	if err := setAndVerifyPrivateWindowsPath(path, true); err != nil {
		return fmt.Errorf("protect Windows private directory: %w", err)
	}
	return nil
}

func makePrivateDirectory(path string) error { return ensurePrivateWindowsDirectory(path) }

func privateFilePermissionsOK(path string, info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && verifyPrivateWindowsPath(path, false)
}

func privateDirectoryPermissionsOK(path string, info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && verifyPrivateWindowsPath(path, true)
}

func openedPrivateFilePermissionsOK(path string, file *os.File, directory bool) bool {
	if file == nil {
		return false
	}
	handle := windows.Handle(file.Fd())
	identity, err := validatePrivateWindowsHandle(handle, directory)
	if err != nil {
		return false
	}
	chain, err := inspectWindowsPathChain(path, directory)
	return err == nil && chain[len(chain)-1].identity == identity
}

func protectPrivateFile(file *os.File) error {
	if file == nil || file.Name() == "" || !filepath.IsAbs(file.Name()) {
		return errors.New("private Windows file handle and absolute path are required")
	}
	handleIdentity, _, err := windowsIdentityForHandle(windows.Handle(file.Fd()))
	if err != nil || handleIdentity.directory {
		return errors.New("private Windows file handle is not a regular non-reparse file")
	}
	if err := setAndVerifyPrivateWindowsPath(file.Name(), false); err != nil {
		return err
	}
	verifiedIdentity, err := validatePrivateWindowsHandle(windows.Handle(file.Fd()), false)
	if err != nil || verifiedIdentity != handleIdentity {
		return errors.New("private Windows file changed while its ACL was protected")
	}
	return nil
}

func removePrivateFile(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("private Windows deletion requires an absolute path")
	}
	chain, err := inspectWindowsPathChain(path, false)
	if err != nil {
		return err
	}
	handle, err := openWindowsPathNoReparse(path,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.OPEN_EXISTING, nil)
	if err != nil {
		return err
	}
	identity, validationErr := validatePrivateWindowsHandle(handle, false)
	if validationErr != nil || identity != chain[len(chain)-1].identity {
		windows.CloseHandle(handle)
		return errors.New("private Windows deletion path and handle identity do not match")
	}
	deleteFile := byte(1)
	if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &deleteFile, 1); err != nil {
		windows.CloseHandle(handle)
		return err
	}
	if err := windows.CloseHandle(handle); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("private Windows file still resolves after handle-bound deletion")
	}
	return nil
}

func databaseSidecarPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func verifyPrivateDatabaseFiles(path string) error {
	for index, candidate := range databaseSidecarPaths(path) {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) && index > 0 {
			continue
		}
		if err != nil {
			return err
		}
		if !privateFilePermissionsOK(candidate, info) {
			return fmt.Errorf("encrypted SQLite file %q does not have the exact private Windows ACL", candidate)
		}
	}
	return nil
}

func protectPrivateDatabaseFiles(path string) error {
	if err := ensurePrivateWindowsDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	for index, candidate := range databaseSidecarPaths(path) {
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) && index > 0 {
			continue
		}
		if err != nil {
			return err
		}
		if err := setAndVerifyPrivateWindowsPath(candidate, false); err != nil {
			return fmt.Errorf("protect encrypted SQLite file %q: %w", candidate, err)
		}
	}
	return verifyPrivateDatabaseFiles(path)
}
