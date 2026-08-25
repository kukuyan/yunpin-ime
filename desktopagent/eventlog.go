// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Background synchronization previously discarded every run event: the macOS
// LaunchAgent sent stdout and stderr to /dev/null, and the Windows scheduled
// task had nowhere to send them at all. A crash loop and a healthy agent
// therefore looked identical from outside, which is what made "the process is
// running" get mistaken for "synchronization is working".
//
// EventLog is the bounded sink that fixes that. It only ever accepts RunEvent
// values, which carry a stable code and a numeric summary and nothing else, so
// the file cannot accumulate phrases, pinyin, ciphertext, endpoints or account
// and device identifiers no matter how long it runs.
const (
	// One rotation keeps the on-disk cost at twice this, which is enough
	// history to see a failure pattern without becoming a data store.
	eventLogMaxBytes  = 256 * 1024
	eventLogDirectory = "logs"
	eventLogName      = "agent.log"
)

// EventLog is an append-only, size-bounded sink for redacted run events.
type EventLog struct {
	mutex    sync.Mutex
	path     string
	file     *os.File
	written  int64
	maxBytes int64
	// clock is injectable so rotation and formatting stay testable without
	// waiting on the wall clock. Nil means time.Now.
	clock func() time.Time
}

// EventLogPath reports where the bounded run-event log lives for these paths.
// It sits inside the state directory so it inherits that directory's
// restrictive ACL rather than needing one of its own.
func EventLogPath(defaults Paths) string {
	return filepath.Join(defaults.StateDirectory, eventLogDirectory, eventLogName)
}

func openExistingPrivateLog(path string, flags int) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		!privateFilePermissionsOK(path, before) {
		return nil, nil, errors.New("event log must be a private regular file")
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, statErr := file.Stat()
	after, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) ||
		!openedPrivateFilePermissionsOK(path, file, false) {
		_ = file.Close()
		return nil, nil, errors.New("event log changed during validated open")
	}
	return file, opened, nil
}

func createPrivateLog(path string) (*os.File, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := protectPrivateFile(file); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil, err
	}
	opened, statErr := file.Stat()
	after, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, after) || !privateFilePermissionsOK(path, after) ||
		!openedPrivateFilePermissionsOK(path, file, false) {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil, errors.New("new event log could not be verified")
	}
	return file, opened, nil
}

func validateOptionalLogGeneration(path string) error {
	file, _, err := openExistingPrivateLog(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

// EventLogAvailable reports whether the current and optional rotated
// generation are private regular files in a private real directory. Missing,
// linked or permission-invalid state is explicit rather than confused with an
// empty healthy log.
func EventLogAvailable(defaults Paths) bool {
	path := EventLogPath(defaults)
	if defaults.StateDirectory == "" || !filepath.IsAbs(path) {
		return false
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!privateDirectoryPermissionsOK(directory, info) {
		return false
	}
	file, _, err := openExistingPrivateLog(path, os.O_RDONLY)
	if err != nil {
		return false
	}
	if err := file.Close(); err != nil {
		return false
	}
	return validateOptionalLogGeneration(path+".1") == nil
}

// OpenEventLog opens (creating if needed) the bounded run-event log.
func OpenEventLog(defaults Paths) (*EventLog, error) {
	path := EventLogPath(defaults)
	if defaults.StateDirectory == "" || !filepath.IsAbs(path) {
		return nil, errors.New("event log path must be absolute")
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := validateOptionalLogGeneration(path + ".1"); err != nil {
		return nil, err
	}
	file, info, err := openExistingPrivateLog(path, os.O_WRONLY|os.O_APPEND)
	if errors.Is(err, os.ErrNotExist) {
		file, info, err = createPrivateLog(path)
	}
	if err != nil {
		return nil, err
	}
	return &EventLog{
		path:     path,
		file:     file,
		written:  info.Size(),
		maxBytes: eventLogMaxBytes,
	}, nil
}

// Write appends one redacted event, rotating first if the file is full.
//
// Logging is diagnostics, never a reason to stop synchronizing: a failure to
// write is dropped rather than propagated into the sync loop.
func (log *EventLog) Write(event RunEvent) {
	if log == nil {
		return
	}
	log.mutex.Lock()
	defer log.mutex.Unlock()
	if log.file == nil {
		return
	}
	if log.written >= log.maxBytes {
		log.rotateLocked()
		if log.file == nil {
			return
		}
	}
	written, err := WriteRunEvent(log.file, event, log.now())
	if err != nil {
		return
	}
	log.written += int64(written)
}

func (log *EventLog) now() time.Time {
	if log.clock != nil {
		return log.clock()
	}
	return time.Now()
}

// rotateLocked replaces the current file with an empty one, keeping a single
// previous generation. The caller holds the mutex.
func (log *EventLog) rotateLocked() {
	if err := log.file.Close(); err != nil {
		log.file = nil
		return
	}
	log.file = nil
	previous := log.path + ".1"
	if _, err := os.Lstat(previous); err == nil {
		if err := validateOptionalLogGeneration(previous); err != nil || removePrivateFile(previous) != nil {
			log.reopenCurrentLocked()
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.reopenCurrentLocked()
		return
	}
	if err := os.Rename(log.path, previous); err != nil {
		// Keep the current generation bounded: reopen it so the next event can
		// retry rotation, but do not append while it remains over the limit.
		log.reopenCurrentLocked()
		return
	}
	file, _, err := createPrivateLog(log.path)
	if err != nil {
		return
	}
	log.file = file
	log.written = 0
}

func (log *EventLog) reopenCurrentLocked() {
	file, info, err := openExistingPrivateLog(log.path, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		log.file = nil
		return
	}
	log.file = file
	log.written = info.Size()
}

// Close releases the underlying file. Further writes are discarded.
func (log *EventLog) Close() error {
	if log == nil {
		return nil
	}
	log.mutex.Lock()
	defer log.mutex.Unlock()
	if log.file == nil {
		return nil
	}
	err := log.file.Close()
	log.file = nil
	return err
}
