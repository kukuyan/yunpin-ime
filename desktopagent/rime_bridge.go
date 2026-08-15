// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxRimeInstallationBytes = 64 << 10
	maxRimeSyncEntries       = 4096
	rimeSnapshotStableDelay  = 100 * time.Millisecond
)

// ErrRimeMaintenanceBusy is a transient, authenticated response from the
// platform input-method host. It means at least one live Rime session still
// owns an uncommitted composition, chord, or operation, so maintenance must be
// deferred without treating it as a synchronization failure.
var ErrRimeMaintenanceBusy = errors.New("Rime host deferred maintenance while input is active")

// RimeBridgePaths are derived from platform-owned YunPin locations. None of
// these paths may be supplied by a maintenance process or looked up on PATH.
type RimeBridgePaths struct {
	InstallationPath string
	SyncDirectory    string
	StagingPath      string
	BackupPath       string
	AckPath          string
}

func DefaultRimeBridgePaths(paths Paths) (RimeBridgePaths, error) {
	if paths.StateDirectory == "" || !filepath.IsAbs(paths.StateDirectory) ||
		paths.BaselinePath == "" || !filepath.IsAbs(paths.BaselinePath) {
		return RimeBridgePaths{}, errors.New("fixed YunPin state and Rime paths are required")
	}
	rimeRoot := filepath.Dir(filepath.Dir(filepath.Clean(paths.BaselinePath)))
	result := RimeBridgePaths{
		InstallationPath: filepath.Join(rimeRoot, "installation.yaml"),
		SyncDirectory:    filepath.Join(paths.StateDirectory, "rime-sync"),
		StagingPath:      filepath.Join(paths.StateDirectory, "rime-userdb.snapshot"),
		BackupPath:       filepath.Join(paths.StateDirectory, "rime-installation.pre-bridge.yaml"),
		AckPath:          filepath.Join(paths.StateDirectory, "rime-maintenance.ack"),
	}
	for _, value := range []string{result.InstallationPath, result.SyncDirectory, result.StagingPath, result.BackupPath, result.AckPath} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return RimeBridgePaths{}, errors.New("Rime bridge path is not a normalized absolute path")
		}
	}
	return result, nil
}

type rimeInstallation struct {
	ID      string
	SyncDir string
	Raw     []byte
}

func safeRimeInstallationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func parseStrictYAMLScalar(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty scalar")
	}
	if value[0] == '"' {
		if len(value) < 2 || value[len(value)-1] != '"' {
			return "", errors.New("invalid double-quoted scalar")
		}
		value = value[1 : len(value)-1]
		if strings.ContainsAny(value, "\\\"\r\n\t") {
			return "", errors.New("escaped double-quoted scalars are not allowed in the fixed Rime bridge fields")
		}
	} else if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("invalid single-quoted scalar")
		}
		value = value[1 : len(value)-1]
		if strings.ContainsRune(value, '\'') {
			return "", errors.New("escaped single-quoted scalars are not allowed in the fixed Rime bridge fields")
		}
	}
	if strings.ContainsAny(value, "#&*!{}[]\r\n\t") {
		return "", errors.New("structured scalars are not allowed in the fixed Rime bridge fields")
	}
	return value, nil
}

func parseRimeInstallation(contents []byte) (rimeInstallation, error) {
	if len(contents) == 0 || len(contents) > maxRimeInstallationBytes ||
		!utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return rimeInstallation{}, errors.New("Rime installation configuration is not bounded UTF-8 text")
	}
	var result rimeInstallation
	result.Raw = append([]byte(nil), contents...)
	installations := 0
	syncDirectories := 0
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n") {
		if rawLine == "" || rawLine[0] == ' ' || rawLine[0] == '\t' || rawLine[0] == '#' {
			continue
		}
		colon := strings.IndexByte(rawLine, ':')
		if colon <= 0 {
			continue
		}
		key := strings.TrimSpace(rawLine[:colon])
		if key != rawLine[:colon] {
			continue
		}
		scalar, err := parseStrictYAMLScalar(rawLine[colon+1:])
		if err != nil && (key == "installation_id" || key == "sync_dir") {
			return rimeInstallation{}, errors.New("Rime bridge configuration contains an invalid required scalar")
		}
		switch key {
		case "installation_id":
			installations++
			result.ID = scalar
		case "sync_dir":
			syncDirectories++
			result.SyncDir = scalar
		}
	}
	if installations != 1 || !safeRimeInstallationID(result.ID) {
		return rimeInstallation{}, errors.New("Rime installation configuration must contain one safe top-level installation_id")
	}
	if syncDirectories > 1 {
		return rimeInstallation{}, errors.New("Rime installation configuration contains duplicate top-level sync_dir values")
	}
	return result, nil
}

func renderRimeSyncDirectory(contents []byte, directory string) ([]byte, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || strings.ContainsRune(directory, '\x00') {
		return nil, errors.New("Rime sync directory must be a normalized absolute path")
	}
	if strings.ContainsAny(directory, "'\"\r\n\t") {
		return nil, errors.New("Rime sync directory cannot be encoded as a fixed YAML scalar")
	}
	encodedDirectory := "'" + directory + "'"
	lines := strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n")
	replaced := false
	for index, line := range lines {
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		if colon := strings.IndexByte(line, ':'); colon > 0 && line[:colon] == "sync_dir" {
			if replaced {
				return nil, errors.New("Rime installation configuration contains duplicate top-level sync_dir values")
			}
			lines[index] = "sync_dir: " + encodedDirectory
			replaced = true
		}
	}
	if !replaced {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "sync_dir: "+encodedDirectory)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func readAndProtectRimeConfig(path string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("Rime installation path must be absolute")
	}
	if !bridgePathComponentsOK(path, false) {
		return nil, errors.New("Rime installation path contains a symlink or unsafe component")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > maxRimeInstallationBytes {
		return nil, errors.New("Rime installation configuration must be a bounded regular file")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("open Rime installation configuration")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("Rime installation configuration changed during validated open")
	}
	if err := protectPrivateFile(file); err != nil || !openedPrivateFilePermissionsOK(path, file, false) {
		return nil, errors.New("Rime installation configuration could not be protected")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxRimeInstallationBytes+1))
	if err != nil || len(contents) > maxRimeInstallationBytes {
		return nil, errors.New("read bounded Rime installation configuration")
	}
	return contents, nil
}

// ConfigureRimeBridge changes only Rime's local sync output directory. It
// creates an immutable private first-state backup before the atomic update.
func ConfigureRimeBridge(paths RimeBridgePaths) error {
	if err := ensurePrivateDirectory(filepath.Dir(paths.BackupPath)); err != nil {
		return fmt.Errorf("prepare private Rime bridge state: %w", err)
	}
	if err := ensurePrivateDirectory(paths.SyncDirectory); err != nil {
		return fmt.Errorf("prepare private Rime maintenance output: %w", err)
	}
	contents, err := readAndProtectRimeConfig(paths.InstallationPath)
	if err != nil {
		return err
	}
	configuration, err := parseRimeInstallation(contents)
	if err != nil {
		return err
	}
	alreadyConfigured := filepath.Clean(configuration.SyncDir) == filepath.Clean(paths.SyncDirectory) &&
		filepath.IsAbs(configuration.SyncDir)
	if backup, backupErr := readBoundedRegular(paths.BackupPath, maxRimeInstallationBytes); backupErr == nil {
		backupConfiguration, parseErr := parseRimeInstallation(backup)
		if parseErr != nil || backupConfiguration.ID != configuration.ID {
			return errors.New("Rime bridge backup does not match the configured installation identity")
		}
		if !alreadyConfigured && !bytes.Equal(backup, contents) {
			return errors.New("Rime bridge backup and current installation configuration diverged")
		}
	} else if errors.Is(backupErr, os.ErrNotExist) {
		if _, err := writeAtomicPrivateFile(paths.BackupPath, contents); err != nil {
			return fmt.Errorf("persist private Rime installation backup: %w", err)
		}
	} else {
		return fmt.Errorf("validate private Rime installation backup: %w", backupErr)
	}
	if alreadyConfigured {
		return nil
	}
	rewritten, err := renderRimeSyncDirectory(contents, paths.SyncDirectory)
	if err != nil {
		return err
	}
	if _, err := writeAtomicPrivateFile(paths.InstallationPath, rewritten); err != nil {
		return fmt.Errorf("atomically configure Rime maintenance output: %w", err)
	}
	return nil
}

func priorSnapshot(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Rime maintenance snapshot path is unsafe")
	}
	if !bridgePathComponentsOK(path, false) {
		return nil, errors.New("Rime maintenance snapshot path contains an unsafe component")
	}
	return info, nil
}

func validateRimeSyncTreeBeforeMaintenance(root, installationID string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 ||
		!privateDirectoryPermissionsOK(root, rootInfo) || !bridgePathComponentsOK(root, true) {
		return errors.New("dedicated Rime maintenance directory is not an exact private directory")
	}
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		return errors.New("read dedicated Rime maintenance directory before host invocation")
	}
	if len(rootEntries) == 0 {
		return nil
	}
	if len(rootEntries) != 1 || rootEntries[0].Name() != installationID ||
		rootEntries[0].Type()&os.ModeSymlink != 0 || !rootEntries[0].IsDir() {
		return errors.New("dedicated Rime maintenance directory contains an unexpected device before host invocation")
	}
	deviceDirectory := filepath.Join(root, installationID)
	deviceInfo, err := os.Lstat(deviceDirectory)
	if err != nil || !deviceInfo.IsDir() || deviceInfo.Mode()&os.ModeSymlink != 0 ||
		!bridgePathComponentsOK(deviceDirectory, true) {
		return errors.New("Rime device snapshot directory is unsafe before host invocation")
	}
	entries, err := os.ReadDir(deviceDirectory)
	if err != nil || len(entries) > maxRimeSyncEntries {
		return errors.New("Rime device snapshot directory has an invalid preflight entry count")
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return errors.New("Rime device snapshot directory contains a non-regular preflight entry")
		}
		path := filepath.Join(deviceDirectory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			!bridgePathComponentsOK(path, false) {
			return errors.New("Rime device snapshot directory contains unsafe preflight state")
		}
		paths = append(paths, path)
	}
	// A host may have completed its backup and then been terminated before the
	// post-maintenance hardening step. Recover only the expected, owned objects
	// through no-follow, identity-bound handles; never invoke the host if an
	// unknown device, link, foreign owner, or non-regular entry was observed.
	if err := hardenRimeBridgePreflightPath(deviceDirectory, true); err != nil {
		return errors.New("Rime device snapshot directory could not be safely recovered before host invocation")
	}
	for _, path := range paths {
		if err := hardenRimeBridgePreflightPath(path, false); err != nil {
			return errors.New("Rime device snapshot file could not be safely recovered before host invocation")
		}
	}
	return nil
}

func validateRimeSyncTree(root, installationID string) error {
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		return errors.New("read dedicated Rime maintenance directory")
	}
	if len(rootEntries) != 1 || rootEntries[0].Name() != installationID || rootEntries[0].Type()&os.ModeSymlink != 0 || !rootEntries[0].IsDir() {
		return errors.New("dedicated Rime maintenance directory must contain only the configured device")
	}
	deviceDirectory := filepath.Join(root, installationID)
	if err := hardenExistingPrivateDirectory(deviceDirectory); err != nil {
		return errors.New("protect dedicated Rime device snapshot directory")
	}
	entries, err := os.ReadDir(deviceDirectory)
	if err != nil || len(entries) == 0 || len(entries) > maxRimeSyncEntries {
		return errors.New("Rime device snapshot directory has an invalid entry count")
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return errors.New("Rime device snapshot directory contains a non-regular entry")
		}
		path := filepath.Join(deviceDirectory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Rime device snapshot directory contains an unsafe entry")
		}
		if !bridgePathComponentsOK(path, false) {
			return errors.New("Rime device snapshot path contains an unsafe component")
		}
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return errors.New("open Rime maintenance output for protection")
		}
		protectErr := protectPrivateFile(file)
		closeErr := file.Close()
		if protectErr != nil || closeErr != nil {
			return errors.New("protect Rime maintenance output")
		}
	}
	return nil
}

func snapshotIsFreshAndStable(ctx context.Context, path string, before os.FileInfo) error {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		first, err := priorSnapshot(path)
		fresh := err == nil && first != nil && (before == nil || !os.SameFile(before, first) ||
			first.ModTime().After(before.ModTime()) || first.Size() != before.Size())
		if fresh {
			timer := time.NewTimer(rimeSnapshotStableDelay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			case <-timer.C:
			}
			second, secondErr := priorSnapshot(path)
			if secondErr == nil && second != nil && os.SameFile(first, second) &&
				first.Size() == second.Size() && first.ModTime().Equal(second.ModTime()) {
				return nil
			}
		}
		poll := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !poll.Stop() {
				<-poll.C
			}
			return ctx.Err()
		case <-deadline.C:
			if !poll.Stop() {
				<-poll.C
			}
			return errors.New("Rime maintenance did not produce a fresh stable snapshot")
		case <-poll.C:
		}
	}
}

type fixedRimeMaintenanceInvoker struct {
	Invoke      func(context.Context, string) error
	RequiresAck bool
}

func maintenanceNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("generate Rime maintenance request nonce")
	}
	return hex.EncodeToString(value), nil
}

func safeMaintenanceNonce(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}

func waitForMaintenanceAck(ctx context.Context, path, nonce string) error {
	if !safeMaintenanceNonce(nonce) {
		return errors.New("Rime maintenance acknowledgement nonce is invalid")
	}
	wanted := []byte(nonce + "\n")
	busy := []byte("busy:" + nonce + "\n")
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		var contents []byte
		var err error
		if _, inspectErr := os.Lstat(path); inspectErr == nil && !bridgePathComponentsOK(path, false) {
			return errors.New("Rime maintenance acknowledgement path contains an unsafe component")
		} else if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
			return errors.New("inspect Rime maintenance acknowledgement")
		}
		contents, err = readBoundedRegular(path, 128)
		if err == nil && (bytes.Equal(contents, wanted) || bytes.Equal(contents, busy)) {
			deferred := bytes.Equal(contents, busy)
			if err := removePrivateFile(path); err != nil {
				return errors.New("remove consumed Rime maintenance acknowledgement")
			}
			if deferred {
				return ErrRimeMaintenanceBusy
			}
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("Rime maintenance acknowledgement is not a private regular file")
		}
		poll := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !poll.Stop() {
				<-poll.C
			}
			return ctx.Err()
		case <-deadline.C:
			if !poll.Stop() {
				<-poll.C
			}
			return errors.New("Rime host maintenance acknowledgement timed out")
		case <-poll.C:
		}
	}
}

func refreshRimeUserDB(ctx context.Context, paths RimeBridgePaths, invoker fixedRimeMaintenanceInvoker) error {
	if invoker.Invoke == nil {
		return errors.New("fixed Rime maintenance invocation is required")
	}
	contents, err := readAndProtectRimeConfig(paths.InstallationPath)
	if err != nil {
		return err
	}
	configuration, err := parseRimeInstallation(contents)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(configuration.SyncDir) || filepath.Clean(configuration.SyncDir) != filepath.Clean(paths.SyncDirectory) {
		return errors.New("Rime bridge is not configured for the dedicated maintenance directory")
	}
	if err := validateRimeSyncTreeBeforeMaintenance(paths.SyncDirectory, configuration.ID); err != nil {
		return err
	}
	deviceDirectory := filepath.Join(paths.SyncDirectory, configuration.ID)
	source := filepath.Join(deviceDirectory, "rime_ice.userdb.txt")
	before, err := priorSnapshot(source)
	if err != nil {
		return err
	}
	nonce, err := maintenanceNonce()
	if err != nil {
		return err
	}
	if invoker.RequiresAck {
		if existing, inspectErr := os.Lstat(paths.AckPath); inspectErr == nil {
			if !existing.Mode().IsRegular() || existing.Mode()&os.ModeSymlink != 0 ||
				!privateFilePermissionsOK(paths.AckPath, existing) {
				return errors.New("existing Rime maintenance acknowledgement is unsafe")
			}
			if !bridgePathComponentsOK(paths.AckPath, false) || removePrivateFile(paths.AckPath) != nil {
				return errors.New("existing Rime maintenance acknowledgement could not be safely cleared")
			}
		} else if !errors.Is(inspectErr, os.ErrNotExist) {
			return errors.New("inspect Rime maintenance acknowledgement")
		}
	}
	if err := invoker.Invoke(ctx, nonce); err != nil {
		return fmt.Errorf("fixed Rime host maintenance failed: %w", err)
	}
	if invoker.RequiresAck {
		if err := waitForMaintenanceAck(ctx, paths.AckPath, nonce); err != nil {
			return err
		}
	}
	if err := validateRimeSyncTree(paths.SyncDirectory, configuration.ID); err != nil {
		return err
	}
	if err := snapshotIsFreshAndStable(ctx, source, before); err != nil {
		return err
	}
	snapshot, err := readBoundedRegular(source, maxRimeUserDBExportBytes)
	if err != nil {
		return errors.New("read validated Rime maintenance snapshot")
	}
	if _, err := parseRimeUserDBExportBytes(snapshot, nil); err != nil {
		return err
	}
	if _, err := writeAtomicPrivateFile(paths.StagingPath, snapshot); err != nil {
		return fmt.Errorf("commit private Rime userdb staging snapshot: %w", err)
	}
	return nil
}

func NewDefaultRimeUserDBRefresh(paths RimeBridgePaths) (func(context.Context) error, error) {
	invoker, err := newFixedRimeMaintenanceInvoker()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return refreshRimeUserDB(ctx, paths, invoker) }, nil
}
