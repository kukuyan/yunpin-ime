// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// BackgroundStatus describes the actual per-user registration, independently
// of the last successful sync. A stale health row cannot enable these flags.
type BackgroundStatus struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Problem   string `json:"problem,omitempty"`
}

func validateBackgroundPaths(paths Paths) error {
	defaults, err := DefaultPaths()
	if err != nil || paths != defaults {
		return errors.New("background control requires the installed user's fixed paths")
	}
	return nil
}

// ReadBackgroundStatus never starts or stops a service.
func ReadBackgroundStatus(ctx context.Context, paths Paths) (BackgroundStatus, error) {
	if err := validateBackgroundPaths(paths); err != nil {
		return BackgroundStatus{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return readPlatformBackgroundStatus(bounded, paths)
}

// EnableBackground reuses the package's standard activation gate. An already
// running registration is left alone; reopening settings never restarts sync.
// Callers must not hold agent.lock: the standard enabler checks resident-ready.
func EnableBackground(ctx context.Context, paths Paths) (BackgroundStatus, error) {
	before, err := ReadBackgroundStatus(ctx, paths)
	if err != nil {
		return before, err
	}
	if !before.Supported || !before.Installed {
		return before, errors.New("the YunPin background component is not installed")
	}
	if before.Enabled && before.Running {
		return before, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := enablePlatformBackground(bounded, paths); err != nil {
		return before, err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		after, readErr := ReadBackgroundStatus(bounded, paths)
		if readErr == nil && after.Enabled && after.Running {
			return after, nil
		}
		select {
		case <-bounded.Done():
			return after, bounded.Err()
		case <-deadline.C:
			return after, errors.New("background activation has not produced a running agent")
		case <-ticker.C:
		}
	}
}

type boundedBackgroundOutput struct{ data []byte }

func (output *boundedBackgroundOutput) Write(data []byte) (int, error) {
	if len(output.data)+len(data) > 32<<10 {
		return 0, errors.New("background command output exceeded limit")
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

func backgroundCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	configureBackgroundCommand(command)
	command.WaitDelay = 2 * time.Second
	output, diagnostic := &boundedBackgroundOutput{}, &boundedBackgroundOutput{}
	command.Stdout, command.Stderr = output, diagnostic
	if err := command.Run(); err != nil {
		// Never include a shell's expanded source, credentials, or arbitrary
		// command output in the settings page or its diagnostics.
		return nil, errors.New("background registration command failed")
	}
	return output.data, nil
}

func withBackgroundScript(paths Paths, suffix string, script []byte, run func(string) error) error {
	if err := ensurePrivateDirectory(paths.StateDirectory); err != nil {
		return err
	}
	file, err := os.CreateTemp(paths.StateDirectory, ".enable-background-*"+suffix)
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := protectPrivateFile(file); err != nil {
		file.Close()
		return err
	}
	_, writeErr := file.Write(script)
	syncErr, closeErr := file.Sync(), file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if filepath.Dir(name) != paths.StateDirectory {
		return errors.New("unexpected activation script location")
	}
	return run(name)
}
