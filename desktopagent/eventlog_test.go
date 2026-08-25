// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestEventLog(t *testing.T, maxBytes int64) (*EventLog, Paths) {
	t.Helper()
	paths := Paths{StateDirectory: t.TempDir()}
	log, err := OpenEventLog(paths)
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	log.maxBytes = maxBytes
	log.clock = func() time.Time { return time.Unix(1700000000, 0).UTC() }
	t.Cleanup(func() { _ = log.Close() })
	return log, paths
}

func TestEventLogWritesOneRedactedLinePerEvent(t *testing.T) {
	log, paths := newTestEventLog(t, eventLogMaxBytes)
	log.Write(RunEvent{Code: "sync_complete", FailureClass: "none", Successful: true, Summary: SyncSummary{
		Rounds: 1, Uploaded: 2, Downloaded: 3, Cursor: 44,
	}})
	log.Write(RunEvent{Code: "sync_failed", FailureClass: "network"})

	content, err := os.ReadFile(EventLogPath(paths))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected one line per event, got %d: %q", len(lines), content)
	}
	var first struct {
		Time       string      `json:"time"`
		Code       string      `json:"code"`
		Failure    string      `json:"failure_class"`
		Successful bool        `json:"successful"`
		Summary    SyncSummary `json:"summary"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if first.Code != "sync_complete" || first.Failure != "none" || !first.Successful || first.Summary.Cursor != 44 {
		t.Fatalf("unexpected event payload: %+v", first)
	}
	if first.Time != "2023-11-14T22:13:20Z" {
		t.Fatalf("unexpected timestamp: %q", first.Time)
	}
}

// The log must stay a numeric health record. If a future change ever widens
// RunEvent, this catches the first field that could carry user text.
func TestEventLogCarriesNoFreeText(t *testing.T) {
	log, paths := newTestEventLog(t, eventLogMaxBytes)
	log.Write(RunEvent{Code: "sync_failed", FailureClass: "network", Summary: SyncSummary{Rounds: 1}})

	content, err := os.ReadFile(EventLogPath(paths))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(content))), &decoded); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	summary, ok := decoded["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary is missing or not an object: %v", decoded["summary"])
	}
	// Counters and flags are fine; a string is not. Free text is how a phrase,
	// a pinyin reading, an endpoint or an identifier would first appear here.
	for name, value := range summary {
		if text, isText := value.(string); isText {
			t.Fatalf("summary field %q carries free text (%q); the log must stay a numeric health record", name, text)
		}
	}
	// "time" and "code" are the only strings, and both are generated here.
	for name, value := range decoded {
		if name == "time" || name == "code" || name == "failure_class" || name == "successful" || name == "summary" {
			continue
		}
		t.Fatalf("unexpected top-level field %q=%v in a redacted event log", name, value)
	}
}

func TestEventLogRejectsUnboundedFailureClass(t *testing.T) {
	log, paths := newTestEventLog(t, eventLogMaxBytes)
	log.Write(RunEvent{Code: "sync_failed", FailureClass: "dial tcp private.example"})
	content, err := os.ReadFile(EventLogPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatalf("unbounded failure detail reached the event log: %q", content)
	}
}

func TestEventLogRotatesAndStaysBounded(t *testing.T) {
	log, paths := newTestEventLog(t, 512)
	for index := 0; index < 200; index++ {
		log.Write(RunEvent{Code: "sync_complete", FailureClass: "none", Successful: true, Summary: SyncSummary{Rounds: index}})
	}
	current, err := os.Stat(EventLogPath(paths))
	if err != nil {
		t.Fatalf("stat current log: %v", err)
	}
	previous, err := os.Stat(EventLogPath(paths) + ".1")
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	// One rotation is kept, so the total is bounded by roughly twice the limit
	// plus the final line that crossed it.
	total := current.Size() + previous.Size()
	if total > 4*512 {
		t.Fatalf("log grew past its bound: %d bytes across two generations", total)
	}
	if current.Size() == 0 {
		t.Fatal("expected the current generation to keep the newest events")
	}
	entries, err := os.ReadDir(filepath.Dir(EventLogPath(paths)))
	if err != nil {
		t.Fatalf("read log directory: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected exactly two generations, found %d", len(entries))
	}
}

func TestEventLogWriteAfterCloseIsDropped(t *testing.T) {
	log, paths := newTestEventLog(t, eventLogMaxBytes)
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	log.Write(RunEvent{Code: "sync_complete", FailureClass: "none"})
	content, err := os.ReadFile(EventLogPath(paths))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(content) != 0 {
		t.Fatalf("write after close reached the file: %q", content)
	}
}

func TestEventLogAvailabilityRequiresPrivateRegularGenerations(t *testing.T) {
	log, paths := newTestEventLog(t, eventLogMaxBytes)
	if !EventLogAvailable(paths) {
		t.Fatal("a freshly opened private event log was reported unavailable")
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	makePrivateTestDirectory(t, EventLogPath(paths)+".1")
	if EventLogAvailable(paths) {
		t.Fatal("a directory in place of the rotated generation was accepted")
	}
	if _, err := OpenEventLog(paths); err == nil {
		t.Fatal("OpenEventLog accepted a non-regular rotated generation")
	}
}

func TestEventLogRejectsDirectoryAtCurrentPath(t *testing.T) {
	paths := Paths{StateDirectory: privateTestPath(t, "state")}
	makePrivateTestDirectory(t, paths.StateDirectory)
	makePrivateTestDirectory(t, filepath.Dir(EventLogPath(paths)))
	makePrivateTestDirectory(t, EventLogPath(paths))
	if _, err := OpenEventLog(paths); err == nil {
		t.Fatal("OpenEventLog accepted a directory in place of the log file")
	}
}

func TestEventLogRejectsPreplacedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ordinary Windows CI users cannot create symlinks; reparse-point rejection is covered by the ACL path tests")
	}
	paths := Paths{StateDirectory: privateTestPath(t, "state")}
	makePrivateTestDirectory(t, paths.StateDirectory)
	makePrivateTestDirectory(t, filepath.Dir(EventLogPath(paths)))
	target := privateTestPath(t, "target.log")
	writePrivateTestFile(t, target, []byte("unchanged"))
	if err := os.Symlink(target, EventLogPath(paths)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEventLog(paths); err == nil {
		t.Fatal("OpenEventLog followed a preplaced symlink")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "unchanged" {
		t.Fatalf("symlink target changed: content=%q err=%v", content, err)
	}
}

func TestEventLogRejectsNonPrivateExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows exact-DACL negatives are covered by permissions_windows_test.go")
	}
	paths := Paths{StateDirectory: privateTestPath(t, "state")}
	makePrivateTestDirectory(t, paths.StateDirectory)
	makePrivateTestDirectory(t, filepath.Dir(EventLogPath(paths)))
	writePrivateTestFile(t, EventLogPath(paths), nil)
	if err := os.Chmod(EventLogPath(paths), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEventLog(paths); err == nil {
		t.Fatal("OpenEventLog accepted a non-private existing file")
	}
}
