// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	cryptprotectUIForbidden = 0x1
	maxProtectedFileBytes   = 1024 * 1024
)

var (
	crypt32DLL             = syscall.NewLazyDLL("crypt32.dll")
	kernel32DLL            = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtectData   = crypt32DLL.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32DLL.NewProc("CryptUnprotectData")
	procLocalFree          = kernel32DLL.NewProc("LocalFree")
)

type dataBlob struct {
	size uint32
	data *byte
}

type dpapiSecretStore struct {
	service   string
	directory string
}

func NewPlatformSecretStore(options PlatformSecretStoreOptions) (SecretStore, error) {
	if err := validateStoreOptions(options); err != nil {
		return nil, err
	}
	if options.Directory == "" || !filepath.IsAbs(options.Directory) {
		return nil, errors.New("DPAPI credential directory must be absolute")
	}
	localAppData, err := knownWindowsFolder(windows.FOLDERID_LocalAppData)
	if err != nil {
		return nil, fmt.Errorf("resolve current-user DPAPI storage root: %w", err)
	}
	directory := filepath.Clean(options.Directory)
	relative, err := filepath.Rel(localAppData, directory)
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		(len(relative) > 3 && relative[:3] == ".."+string(filepath.Separator)) {
		return nil, errors.New("DPAPI credentials must stay below the current user's LOCALAPPDATA")
	}
	return &dpapiSecretStore{service: options.Service, directory: directory}, nil
}

func blob(value []byte) dataBlob {
	result := dataBlob{size: uint32(len(value))}
	if len(value) > 0 {
		result.data = &value[0]
	}
	return result
}

func dpapiError(operation string, callErr error) error {
	if callErr == nil || callErr == syscall.Errno(0) {
		callErr = syscall.GetLastError()
	}
	return fmt.Errorf("%s YunPin credential with current-user DPAPI: %w", operation, callErr)
}

func localFree(value *byte) {
	if value != nil {
		_, _, _ = procLocalFree.Call(uintptr(unsafe.Pointer(value)))
	}
}

func secureLocalFree(value *byte, length uint32) {
	if value == nil {
		return
	}
	if length > 0 && length <= maxProtectedFileBytes {
		clear(unsafe.Slice(value, int(length)))
		// Keep the foreign pointer live until after the final explicit store.
		runtime.KeepAlive(value)
	}
	localFree(value)
}

func (store *dpapiSecretStore) entropy(profile string) [sha256.Size]byte {
	return sha256.Sum256([]byte("yunpin-desktopagent-dpapi-v1\x00" + store.service + "\x00" + profile))
}

func (store *dpapiSecretStore) protect(profile string, value []byte) ([]byte, error) {
	entropy := store.entropy(profile)
	input := blob(value)
	optional := blob(entropy[:])
	var output dataBlob
	description, err := syscall.UTF16PtrFromString("YunPin sync credentials")
	if err != nil {
		return nil, err
	}
	result, _, callErr := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&input)), uintptr(unsafe.Pointer(description)),
		uintptr(unsafe.Pointer(&optional)), 0, 0, cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&output)),
	)
	if result == 0 {
		return nil, dpapiError("protect", callErr)
	}
	defer secureLocalFree(output.data, output.size)
	if output.size < 1 || output.size > maxProtectedFileBytes || output.data == nil {
		return nil, errors.New("DPAPI returned an invalid protected credential")
	}
	return append([]byte(nil), unsafe.Slice(output.data, int(output.size))...), nil
}

func (store *dpapiSecretStore) unprotect(profile string, value []byte) ([]byte, error) {
	entropy := store.entropy(profile)
	input := blob(value)
	optional := blob(entropy[:])
	var output dataBlob
	result, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&input)), 0, uintptr(unsafe.Pointer(&optional)),
		0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&output)),
	)
	if result == 0 {
		return nil, dpapiError("unprotect", callErr)
	}
	defer secureLocalFree(output.data, output.size)
	if output.size < 1 || output.size > maxCredentialBlobBytes || output.data == nil {
		return nil, errors.New("DPAPI returned an invalid plaintext credential")
	}
	return append([]byte(nil), unsafe.Slice(output.data, int(output.size))...), nil
}

func (store *dpapiSecretStore) path(profile string) (string, error) {
	if err := validateProfile(profile); err != nil {
		return "", err
	}
	return filepath.Join(store.directory, "credentials-"+profile+".v1.dpapi"), nil
}

func moveReplace(source, destination string) error {
	if err := replaceFile(source, destination); err != nil {
		return fmt.Errorf("atomically replace DPAPI credential: %w", err)
	}
	return nil
}

func (store *dpapiSecretStore) Save(ctx context.Context, profile string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(value) < 1 || len(value) > maxCredentialBlobBytes {
		return errors.New("credential value length is invalid")
	}
	path, err := store.path(profile)
	if err != nil {
		return err
	}
	protected, err := store.protect(profile, value)
	if err != nil {
		return err
	}
	defer zeroBytes(protected)
	if err := ensurePrivateDirectory(store.directory); err != nil {
		return fmt.Errorf("create DPAPI credential directory: %w", err)
	}
	temporary, err := os.CreateTemp(store.directory, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary DPAPI credential: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = protectPrivateFile(temporary); err == nil {
		_, err = temporary.Write(protected)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return fmt.Errorf("write DPAPI credential: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close DPAPI credential: %w", closeErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return moveReplace(temporaryPath, path)
}

func (store *dpapiSecretStore) Load(ctx context.Context, profile string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := store.path(profile)
	if err != nil {
		return nil, err
	}
	directoryInfo, err := os.Lstat(store.directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 ||
		!privateDirectoryPermissionsOK(store.directory, directoryInfo) {
		return nil, errors.New("DPAPI credential directory is not protected by the current-user DACL")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("inspect DPAPI credential: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !privateFilePermissionsOK(path, info) ||
		info.Size() < 1 || info.Size() > maxProtectedFileBytes {
		return nil, errors.New("DPAPI credential must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open DPAPI credential: %w", err)
	}
	defer file.Close()
	if !openedPrivateFilePermissionsOK(path, file, false) {
		return nil, errors.New("DPAPI credential path and opened handle do not identify the same private file")
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() < 1 || openedInfo.Size() > maxProtectedFileBytes {
		return nil, errors.New("DPAPI credential changed during validated open")
	}
	var protected bytes.Buffer
	if _, err := protected.ReadFrom(io.LimitReader(file, maxProtectedFileBytes+1)); err != nil {
		return nil, fmt.Errorf("read DPAPI credential: %w", err)
	}
	if protected.Len() < 1 || protected.Len() > maxProtectedFileBytes {
		return nil, errors.New("DPAPI credential length changed during read")
	}
	protectedBytes := protected.Bytes()
	defer zeroBytes(protectedBytes)
	plain, err := store.unprotect(profile, protectedBytes)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		zeroBytes(plain)
		return nil, err
	}
	return plain, nil
}

func (store *dpapiSecretStore) Delete(ctx context.Context, profile string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := store.path(profile)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrSecretNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !privateFilePermissionsOK(path, info) {
		return errors.New("refuse to delete an unprotected DPAPI credential path")
	}
	if err := removePrivateFile(path); errors.Is(err, os.ErrNotExist) {
		return ErrSecretNotFound
	} else if err != nil {
		return fmt.Errorf("delete DPAPI credential: %w", err)
	}
	return nil
}
