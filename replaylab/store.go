// SPDX-License-Identifier: Apache-2.0
package replaylab

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	manifestVersion = "yunpin.replay.lab.v1"
	sessionVersion  = "yunpin.replay.session.v1"
	manifestName    = "lab.json"
	activeName      = "active.json"
)

type LabManifest struct {
	Version     string `json:"version"`
	LabID       string `json:"lab_id"`
	Root        string `json:"root"`
	CreatedUTC  string `json:"created_utc"`
	FsyncPolicy string `json:"fsync_policy"`
}

type SessionMetadata struct {
	Version         string `json:"version"`
	SessionID       string `json:"session_id,omitempty"`
	State           string `json:"state"`
	StartedUTC      string `json:"started_utc,omitempty"`
	UpdatedUTC      string `json:"updated_utc"`
	EventFile       string `json:"event_file,omitempty"`
	LastEpisodeID   string `json:"last_episode_id,omitempty"`
	LastSeq         uint64 `json:"last_seq"`
	LastMonotonicUS uint64 `json:"last_monotonic_us"`
	LastUTC         string `json:"last_utc,omitempty"`
	EpisodeClosed   bool   `json:"episode_closed"`
}

// Store is a single-writer object. A persistent native sidecar should keep one
// instance and feed it in sequence; separate CLI invocations reopen and verify
// the append-only log before making a change.
type Store struct {
	root           string
	manifest       LabManifest
	cachedMetadata *SessionMetadata
	cachedSequence SequenceState
}

func DefaultRoot() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("LOCALAPPDATA is not set")
		}
		return filepath.Join(base, "YunPin", "ReplayLab"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "YunPin", "ReplayLab"), nil
	default:
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(base, "yunpin", "replaylab"), nil
	}
}

// Init creates an inert lab. It does not start capture and opens no network
// connection. The fsync policy is deliberately fixed for v1: every accepted
// event is durable before metadata advances.
func Init(root string, now time.Time) (*Store, error) {
	abs, err := safeRoot(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(abs, manifestName)); err == nil {
		return nil, errors.New("Replay Lab is already initialized at this root")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect lab manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "sessions"), 0o700); err != nil {
		return nil, fmt.Errorf("create lab root: %w", err)
	}
	labID, err := NewID()
	if err != nil {
		return nil, err
	}
	manifest := LabManifest{
		Version:     manifestVersion,
		LabID:       labID,
		Root:        abs,
		CreatedUTC:  canonicalUTC(now),
		FsyncPolicy: "each_event",
	}
	if err := atomicJSON(filepath.Join(abs, manifestName), manifest); err != nil {
		return nil, err
	}
	metadata := SessionMetadata{
		Version:    sessionVersion,
		State:      "disabled",
		UpdatedUTC: canonicalUTC(now),
	}
	if err := atomicJSON(filepath.Join(abs, activeName), metadata); err != nil {
		return nil, err
	}
	return &Store{root: abs, manifest: manifest}, nil
}

func Open(root string) (*Store, error) {
	abs, err := safeRoot(root)
	if err != nil {
		return nil, err
	}
	var manifest LabManifest
	if err := readStrictJSON(filepath.Join(abs, manifestName), &manifest); err != nil {
		return nil, fmt.Errorf("open lab manifest: %w", err)
	}
	if manifest.Version != manifestVersion || !validID(manifest.LabID) || manifest.Root != abs || manifest.FsyncPolicy != "each_event" || !isCanonicalUTC(manifest.CreatedUTC) {
		return nil, errors.New("lab manifest identity or version is invalid")
	}
	return &Store{root: abs, manifest: manifest}, nil
}

func (store *Store) Root() string { return store.root }

func (store *Store) Status() (SessionMetadata, error) {
	if store.cachedMetadata != nil {
		return *store.cachedMetadata, nil
	}
	metadata, err := store.loadMetadata()
	if err != nil {
		return SessionMetadata{}, err
	}
	if metadata.SessionID == "" {
		store.cachedMetadata = &metadata
		return metadata, nil
	}
	state, err := store.scanSession(metadata.EventFile)
	if err != nil {
		return SessionMetadata{}, err
	}
	if state.SessionID != "" && state.SessionID != metadata.SessionID {
		return SessionMetadata{}, errors.New("session metadata does not match append-only event log")
	}
	if state.LastSeq < metadata.LastSeq {
		return SessionMetadata{}, errors.New("append-only event log is behind session metadata")
	}
	// The event log is the source of truth only in the safe crash window where
	// an append reached fsync but the following atomic metadata replacement did
	// not. A log that moved backwards or disagrees at the same sequence is an
	// integrity error, not something to repair silently.
	if state.LastSeq > metadata.LastSeq {
		metadata.LastSeq = state.LastSeq
		metadata.LastEpisodeID = state.LastEpisodeID
		metadata.LastMonotonicUS = state.LastMonotonicUS
		metadata.LastUTC = canonicalUTC(state.LastUTC)
		metadata.EpisodeClosed = state.EpisodeClosed
		if state.Paused {
			metadata.State = "paused"
		} else {
			metadata.State = "running"
		}
		metadata.UpdatedUTC = canonicalUTC(time.Now())
		if err := store.saveMetadata(metadata); err != nil {
			return SessionMetadata{}, fmt.Errorf("repair session metadata: %w", err)
		}
	} else if state.LastSeq > 0 {
		stateName := "running"
		if state.Paused {
			stateName = "paused"
		}
		if state.LastEpisodeID != metadata.LastEpisodeID ||
			state.LastMonotonicUS != metadata.LastMonotonicUS ||
			canonicalUTC(state.LastUTC) != metadata.LastUTC ||
			state.EpisodeClosed != metadata.EpisodeClosed ||
			stateName != metadata.State {
			return SessionMetadata{}, errors.New("session metadata disagrees with append-only event log")
		}
	}
	store.cachedMetadata = &metadata
	store.cachedSequence = state
	return metadata, nil
}

func (store *Store) Start(now time.Time) (SessionMetadata, error) {
	current, err := store.loadMetadata()
	if err != nil {
		return SessionMetadata{}, err
	}
	if current.State != "disabled" {
		return SessionMetadata{}, fmt.Errorf("lab state is %q; clear the lab to begin a new session", current.State)
	}
	sessionID, err := NewID()
	if err != nil {
		return SessionMetadata{}, err
	}
	episodeID, err := NewID()
	if err != nil {
		return SessionMetadata{}, err
	}
	metadata := SessionMetadata{
		Version:    sessionVersion,
		SessionID:  sessionID,
		State:      "running",
		StartedUTC: canonicalUTC(now),
		UpdatedUTC: canonicalUTC(now),
		EventFile:  filepath.Join("sessions", sessionID+".yunpinreplay"),
	}
	event := EventV1{
		Version:     EventVersionV1,
		SessionID:   sessionID,
		EpisodeID:   episodeID,
		Seq:         1,
		MonotonicUS: 0,
		UTC:         canonicalUTC(now),
		Type:        EventResume,
	}
	eventPath, err := store.sessionPath(metadata.EventFile)
	if err != nil {
		return SessionMetadata{}, err
	}
	if err := appendEventFile(eventPath, event); err != nil {
		return SessionMetadata{}, err
	}
	metadata.LastEpisodeID = episodeID
	metadata.LastSeq = 1
	metadata.LastUTC = event.UTC
	metadata.EpisodeClosed = false
	if err := store.saveMetadata(metadata); err != nil {
		return SessionMetadata{}, err
	}
	var state SequenceState
	if err := state.Accept(event); err != nil {
		return SessionMetadata{}, fmt.Errorf("initialize session sequence: %w", err)
	}
	store.cachedMetadata = &metadata
	store.cachedSequence = state
	return store.Status()
}

func (store *Store) Pause(now time.Time) (SessionMetadata, error) {
	metadata, err := store.Status()
	if err != nil {
		return SessionMetadata{}, err
	}
	if metadata.State != "running" {
		return SessionMetadata{}, fmt.Errorf("cannot pause lab in state %q", metadata.State)
	}
	event := store.controlEvent(metadata, EventPause, metadata.LastEpisodeID, now)
	if err := store.Append(event); err != nil {
		return SessionMetadata{}, err
	}
	return store.Status()
}

func (store *Store) Resume(now time.Time) (SessionMetadata, error) {
	metadata, err := store.Status()
	if err != nil {
		return SessionMetadata{}, err
	}
	if metadata.State != "paused" {
		return SessionMetadata{}, fmt.Errorf("cannot resume lab in state %q", metadata.State)
	}
	episodeID, err := NewID()
	if err != nil {
		return SessionMetadata{}, err
	}
	event := store.controlEvent(metadata, EventResume, episodeID, now)
	if err := store.Append(event); err != nil {
		return SessionMetadata{}, err
	}
	return store.Status()
}

func (store *Store) controlEvent(metadata SessionMetadata, kind EventType, episodeID string, now time.Time) EventV1 {
	mono := metadata.LastMonotonicUS + 1
	if started, err := time.Parse(time.RFC3339Nano, metadata.StartedUTC); err == nil && now.After(started) {
		elapsed := uint64(now.Sub(started).Microseconds())
		if elapsed > mono {
			mono = elapsed
		}
	}
	return EventV1{
		Version:     EventVersionV1,
		SessionID:   metadata.SessionID,
		EpisodeID:   episodeID,
		Seq:         metadata.LastSeq + 1,
		MonotonicUS: mono,
		UTC:         canonicalUTC(now),
		Type:        kind,
	}
}

func (store *Store) Append(event EventV1) error {
	metadata, err := store.Status()
	if err != nil {
		return err
	}
	if metadata.State == "disabled" {
		return errors.New("lab is disabled; run start before ingest")
	}
	state := store.cachedSequence
	if err := state.Accept(event); err != nil {
		return fmt.Errorf("reject event: %w", err)
	}
	eventPath, err := store.sessionPath(metadata.EventFile)
	if err != nil {
		return err
	}
	if err := appendEventFile(eventPath, event); err != nil {
		return err
	}
	metadata.LastEpisodeID = state.LastEpisodeID
	metadata.LastSeq = state.LastSeq
	metadata.LastMonotonicUS = state.LastMonotonicUS
	metadata.LastUTC = canonicalUTC(state.LastUTC)
	metadata.EpisodeClosed = state.EpisodeClosed
	if state.Paused {
		metadata.State = "paused"
	} else {
		metadata.State = "running"
	}
	metadata.UpdatedUTC = event.UTC
	if err := store.saveMetadata(metadata); err != nil {
		store.cachedMetadata = nil
		store.cachedSequence = SequenceState{}
		return err
	}
	store.cachedMetadata = &metadata
	store.cachedSequence = state
	return nil
}

func (store *Store) Events() ([]EventV1, error) {
	metadata, err := store.Status()
	if err != nil {
		return nil, err
	}
	if metadata.EventFile == "" {
		return nil, nil
	}
	path, err := store.sessionPath(metadata.EventFile)
	if err != nil {
		return nil, err
	}
	return readEvents(path)
}

// Report streams the append-only log through the analyzer. Raw text memory is
// bounded by one event and one active episode; only compact episode IDs are
// retained to reject reuse (up to MaxEpisodes per session).
func (store *Store) Report() (Report, error) {
	metadata, err := store.Status()
	if err != nil {
		return Report{}, err
	}
	analyzer := NewAnalyzer()
	if metadata.EventFile == "" {
		return analyzer.Finish(), nil
	}
	nativePath := filepath.Join(store.root, "native", metadata.SessionID+".native.yunpinreplay")
	if info, statErr := os.Stat(nativePath); statErr == nil && info.Size() > 0 {
		report, _, analyzeErr := analyzeNativeFile(nativePath, metadata.SessionID)
		return report, analyzeErr
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Report{}, fmt.Errorf("inspect native spool: %w", statErr)
	}
	path, err := store.sessionPath(metadata.EventFile)
	if err != nil {
		return Report{}, err
	}
	if err := forEachEvent(path, analyzer.Accept); err != nil {
		return Report{}, err
	}
	return analyzer.Finish(), nil
}

func (store *Store) Export(output string) error {
	metadata, err := store.Status()
	if err != nil {
		return err
	}
	if metadata.EventFile == "" {
		return errors.New("there is no session to export")
	}
	source, err := store.sessionPath(metadata.EventFile)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("resolve export path: %w", err)
	}
	if filepath.Ext(absOutput) != ".yunpinreplay" {
		return errors.New("export output must use the .yunpinreplay extension")
	}
	if insideGitWorktree(absOutput) {
		return errors.New("export output must be outside every Git worktree")
	}
	if strings.HasPrefix(absOutput+string(os.PathSeparator), store.root+string(os.PathSeparator)) {
		return errors.New("export output must be outside the live lab root")
	}
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(absOutput), 0o700); err != nil {
		return err
	}
	outputFile, err := os.OpenFile(absOutput, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create export: %w", err)
	}
	_, copyErr := io.Copy(outputFile, input)
	if copyErr == nil {
		copyErr = outputFile.Sync()
	}
	closeErr := outputFile.Close()
	if copyErr != nil {
		_ = os.Remove(absOutput)
		return fmt.Errorf("write export: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close export: %w", closeErr)
	}
	return nil
}

// Clear removes only an initialized Replay Lab root whose path and manifest
// identity exactly match. The caller must pass confirm=true explicitly.
func Clear(root string, confirm bool) error {
	if !confirm {
		return errors.New("refusing to clear without --confirm")
	}
	abs, err := safeRoot(root)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("inspect lab root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to clear a symlinked lab root")
	}
	store, err := Open(abs)
	if err != nil {
		return fmt.Errorf("refusing to clear an unverified root: %w", err)
	}
	if store.root != abs || store.manifest.Root != abs {
		return errors.New("lab root identity mismatch")
	}
	return os.RemoveAll(abs)
}

func (store *Store) loadMetadata() (SessionMetadata, error) {
	var metadata SessionMetadata
	if err := readStrictJSON(filepath.Join(store.root, activeName), &metadata); err != nil {
		return SessionMetadata{}, fmt.Errorf("open active session metadata: %w", err)
	}
	if metadata.Version != sessionVersion {
		return SessionMetadata{}, errors.New("unsupported session metadata version")
	}
	if !isCanonicalUTC(metadata.UpdatedUTC) {
		return SessionMetadata{}, errors.New("session updated_utc is invalid")
	}
	switch metadata.State {
	case "disabled":
		if metadata.SessionID != "" || metadata.EventFile != "" || metadata.StartedUTC != "" || metadata.LastUTC != "" || metadata.LastSeq != 0 {
			return SessionMetadata{}, errors.New("disabled session metadata contains active state")
		}
	case "running", "paused":
		if !validID(metadata.SessionID) || !validID(metadata.LastEpisodeID) || metadata.LastSeq == 0 || !isCanonicalUTC(metadata.StartedUTC) || !isCanonicalUTC(metadata.LastUTC) || metadata.EventFile != filepath.Join("sessions", metadata.SessionID+".yunpinreplay") {
			return SessionMetadata{}, errors.New("active session metadata identity is invalid")
		}
	default:
		return SessionMetadata{}, fmt.Errorf("unknown lab state %q", metadata.State)
	}
	return metadata, nil
}

func (store *Store) saveMetadata(metadata SessionMetadata) error {
	return atomicJSON(filepath.Join(store.root, activeName), metadata)
}

func (store *Store) scanSession(relative string) (SequenceState, error) {
	if relative == "" {
		return SequenceState{}, nil
	}
	path, err := store.sessionPath(relative)
	if err != nil {
		return SequenceState{}, err
	}
	var state SequenceState
	err = forEachEvent(path, state.Accept)
	if errors.Is(err, os.ErrNotExist) {
		return SequenceState{}, nil
	}
	if err != nil {
		return SequenceState{}, err
	}
	return state, nil
}

func (store *Store) sessionPath(relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return "", errors.New("invalid relative session path")
	}
	abs := filepath.Join(store.root, relative)
	if !strings.HasPrefix(abs+string(os.PathSeparator), store.root+string(os.PathSeparator)) {
		return "", errors.New("session path escapes lab root")
	}
	return abs, nil
}

func readEvents(path string) ([]EventV1, error) {
	var events []EventV1
	err := forEachEvent(path, func(event EventV1) error {
		events = append(events, event)
		return nil
	})
	return events, err
}

func forEachEvent(path string, accept func(EventV1) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), MaxEventBytes+1)
	line := 0
	for scanner.Scan() {
		line++
		event, err := DecodeEventV1(scanner.Bytes())
		if err != nil {
			return fmt.Errorf("event log line %d: %w", line, err)
		}
		if err := accept(event); err != nil {
			return fmt.Errorf("event log line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read event log: %w", err)
	}
	return nil
}

func safeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("lab root must not be empty")
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve lab root: %w", err)
	}
	volume := filepath.VolumeName(abs)
	separatorRoot := volume + string(os.PathSeparator)
	if abs == separatorRoot || abs == volume || abs == "." {
		return "", errors.New("refusing filesystem root as lab root")
	}
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		homeAbs, _ := filepath.Abs(filepath.Clean(home))
		if abs == homeAbs {
			return "", errors.New("refusing the user home directory as lab root")
		}
	}
	trimmed := strings.TrimPrefix(abs, separatorRoot)
	if len(strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == '\\' })) < 2 {
		return "", errors.New("lab root path is too broad")
	}
	if insideGitWorktree(abs) {
		return "", errors.New("lab root must be outside every Git worktree")
	}
	return abs, nil
}

func insideGitWorktree(path string) bool {
	directory := path
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		directory = filepath.Dir(path)
	} else if filepath.Ext(path) != "" {
		directory = filepath.Dir(path)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return false
		}
		directory = parent
	}
}

func atomicJSON(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	random, err := NewID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, "."+filepath.Base(path)+"."+random+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary metadata: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(value)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		if writeErr != nil {
			return fmt.Errorf("write metadata: %w", writeErr)
		}
		return fmt.Errorf("close metadata: %w", closeErr)
	}
	if err := replaceFile(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace metadata: %w", err)
	}
	syncDirectory(directory)
	return nil
}

func syncDirectory(directory string) {
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
}

func readStrictJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("metadata contains trailing JSON value")
		}
		return err
	}
	return nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func appendEventFile(path string, event EventV1) error {
	encoded, err := EncodeEventV1(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	writeErr := writeFull(file, append(encoded, '\n'))
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append event log: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close event log: %w", closeErr)
	}
	syncDirectory(filepath.Dir(path))
	return nil
}

func canonicalUTC(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
