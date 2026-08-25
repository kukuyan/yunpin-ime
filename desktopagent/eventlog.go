// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
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

// OpenEventLog opens (creating if needed) the bounded run-event log.
func OpenEventLog(defaults Paths) (*EventLog, error) {
	path := EventLogPath(defaults)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
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
	// A failed rename must not leave the log closed and silent; fall through to
	// reopening the same path in that case.
	_ = os.Rename(log.path, log.path+".1")
	file, err := os.OpenFile(log.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.file = nil
		return
	}
	log.file = file
	log.written = 0
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
