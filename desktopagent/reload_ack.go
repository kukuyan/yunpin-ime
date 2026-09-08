// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package desktopagent

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	snapshotReloadRequestName = "snapshot-reload.request"
	snapshotReloadAckName     = "snapshot-reload.ack"
)

// The private file carries the digest, never argv or public health/log output.
// Only the unpredictable nonce crosses the host's notification boundary.
func reloadSnapshotWithAck(ctx context.Context, directory string, request snapshotReloadRequest,
	invoke func(context.Context, string) error) (returnErr error) {
	if !filepath.IsAbs(directory) || !bridgePathComponentsOK(directory, true) {
		return errors.New("snapshot acknowledgement directory is unsafe")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("snapshot acknowledgement directory is not private")
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	private := openedPrivateFilePermissionsOK(directory, directoryHandle, true)
	_ = directoryHandle.Close()
	if !private {
		return errors.New("snapshot acknowledgement directory ownership is invalid")
	}
	nonce, err := maintenanceNonce()
	if err != nil {
		return err
	}
	requestPath := filepath.Join(directory, snapshotReloadRequestName)
	ackPath := filepath.Join(directory, snapshotReloadAckName)
	if _, err := os.Lstat(ackPath); err == nil {
		if _, err := readBoundedRegular(ackPath, 512); err != nil {
			return errors.New("existing snapshot acknowledgement is unsafe")
		}
		if err := removePrivateFile(ackPath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	identity := "v1\t" + nonce + "\t" + strconv.FormatUint(request.Generation, 10) + "\t" + hex.EncodeToString(request.Digest[:])
	if _, err := writeAtomicPrivateFile(requestPath, []byte(identity+"\n")); err != nil {
		return err
	}
	defer func() {
		if err := removePrivateFile(requestPath); err != nil {
			returnErr = errors.Join(returnErr, errors.New("snapshot reload request could not be safely removed"))
		}
	}()
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := invoke(bounded, nonce); err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := bounded.Err(); err != nil {
			return fmt.Errorf("Rime snapshot application was not acknowledged: %w", err)
		}
		contents, err := readBoundedRegular(ackPath, 512)
		if err == nil {
			fields := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\t")
			// A stale/late response must match all three request coordinates.
			if len(fields) == 7 && strings.Join(fields[:4], "\t") == identity {
				if err := bounded.Err(); err != nil {
					return err
				}
				pid, pidErr := strconv.ParseUint(fields[5], 10, 31)
				if !safeMaintenanceNonce(fields[4]) || pidErr != nil || pid == 0 {
					return errors.New("snapshot acknowledgement host identity is invalid")
				}
				if err := removePrivateFile(ackPath); err != nil {
					return err
				}
				switch fields[6] {
				case "applied":
					return nil
				case "busy":
					return ErrRimeMaintenanceBusy
				case "failed":
					return errors.New("Rime engine rejected snapshot activation")
				default:
					return errors.New("snapshot acknowledgement result is invalid")
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("snapshot acknowledgement is not a private regular file")
		}
		select {
		case <-bounded.Done():
			return fmt.Errorf("Rime snapshot application was not acknowledged: %w", bounded.Err())
		case <-ticker.C:
		}
	}
}
