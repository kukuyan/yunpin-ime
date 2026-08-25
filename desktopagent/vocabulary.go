// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kukuyan/yunpin-ime/localstore"
)

// The local store has supported explicit vocabulary edits since it was written
// -- SaveExplicit, Delete and the pinned flag all exist and all go through the
// same mutation-plus-outbox transaction -- but nothing exposed them. A user who
// saw a wrong candidate had no supported way to pin, remove or add a phrase;
// the only reachable lever was hand-editing yunpin/private.tsv, which is a
// generated snapshot that the next rebuild overwrites.
//
// These operations close that loop. They deliberately reuse the existing
// storage path rather than adding a second one, so a manual edit converges
// across devices exactly like a learned one.

const (
	// A phrase and its reading are user-visible input, not a data channel.
	// Bounding them keeps a malformed paste from reaching the snapshot writer.
	maxVocabularyTextRunes   = 32
	maxVocabularyPinyinRunes = 96
	// The listing is capped so a single command cannot dump an entire personal
	// vocabulary by accident.
	maxVocabularyListEntries = 200
)

// VocabularyChange reports the outcome of one explicit edit.
//
// It deliberately does not echo the phrase back. The caller already knows what
// it asked for, and keeping the result free of vocabulary means it stays safe
// to print, pipe and paste into a bug report.
type VocabularyChange struct {
	Applied          bool   `json:"applied"`
	Pinned           bool   `json:"pinned"`
	UseCount         uint64 `json:"use_count"`
	SnapshotRows     int    `json:"snapshot_rows"`
	SnapshotChanged  bool   `json:"snapshot_changed"`
	SnapshotReloaded bool   `json:"snapshot_reloaded"`
}

// VocabularyQuery bounds a listing.
type VocabularyQuery struct {
	Limit      int
	PinnedOnly bool
	// IncludeText is the explicit opt-in that puts phrases and readings in the
	// result. Without it the listing is counts only. This is the single place
	// where personal vocabulary can leave the store through this API, so it is
	// a parameter rather than a default.
	IncludeText bool
}

// VocabularyEntry is one listed phrase. Text and Pinyin are populated only when
// the query opted in.
type VocabularyEntry struct {
	Text     string `json:"text,omitempty"`
	Pinyin   string `json:"pinyin,omitempty"`
	Source   string `json:"source"`
	UseCount uint64 `json:"use_count"`
	Pinned   bool   `json:"pinned"`
}

// VocabularySummary is counts first, entries only on request.
type VocabularySummary struct {
	Total        int               `json:"total"`
	Pinned       int               `json:"pinned"`
	BySource     map[string]int    `json:"by_source"`
	TextIncluded bool              `json:"text_included"`
	Entries      []VocabularyEntry `json:"entries,omitempty"`
}

// ErrPhraseNotFound reports an edit against a phrase the store does not hold.
var ErrPhraseNotFound = errors.New("phrase is not in the local vocabulary")

func validateVocabularyInput(text, pinyin string) (string, string, error) {
	text = strings.TrimSpace(text)
	pinyin = strings.TrimSpace(pinyin)
	if text == "" || pinyin == "" {
		return "", "", errors.New("phrase text and Pinyin are required")
	}
	if !utf8.ValidString(text) || !utf8.ValidString(pinyin) {
		return "", "", errors.New("phrase text and Pinyin must be valid UTF-8")
	}
	if utf8.RuneCountInString(text) > maxVocabularyTextRunes {
		return "", "", fmt.Errorf("phrase text exceeds %d characters", maxVocabularyTextRunes)
	}
	if utf8.RuneCountInString(pinyin) > maxVocabularyPinyinRunes {
		return "", "", fmt.Errorf("phrase Pinyin exceeds %d characters", maxVocabularyPinyinRunes)
	}
	for _, r := range text {
		// A control character in a phrase would survive into the snapshot's
		// tab-separated rows and corrupt the file the input method reads.
		if unicode.IsControl(r) || r == '\t' {
			return "", "", errors.New("phrase text must not contain control characters")
		}
	}
	for _, r := range pinyin {
		if !(r >= 'a' && r <= 'z') && r != ' ' && r != '\'' {
			return "", "", errors.New("phrase Pinyin must be lowercase letters, spaces or apostrophes")
		}
	}
	return text, pinyin, nil
}

// withPrivateStore runs fn against the opened encrypted store, applying the
// same file checks and permission repair SyncOnce uses. The database is the
// only place personal vocabulary lives, so nothing may open it more loosely
// than the synchronizing path does.
func (agent Agent) withPrivateStore(
	ctx context.Context, fn func(*localstore.Store) error,
) (returnErr error) {
	bundle, err := agent.loadBundle(ctx)
	if err != nil {
		return err
	}
	defer bundle.Zero()
	if agent.DatabasePath == "" || !filepath.IsAbs(agent.DatabasePath) {
		return errors.New("encrypted local database path must be absolute")
	}
	info, err := os.Lstat(agent.DatabasePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!privateFilePermissionsOK(agent.DatabasePath, info) {
		return errors.New("encrypted local database must be an existing regular file")
	}
	if err := verifyPrivateDatabaseFiles(agent.DatabasePath); err != nil {
		return fmt.Errorf("verify encrypted local database files: %w", err)
	}
	store, err := localstore.OpenForDevice(
		ctx, agent.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex(),
	)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := store.Close()
		permissionErr := protectPrivateDatabaseFiles(agent.DatabasePath)
		if returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
		if returnErr == nil && permissionErr != nil {
			returnErr = fmt.Errorf("protect encrypted local database and sidecar permissions: %w", permissionErr)
		}
	}()
	if err := protectPrivateDatabaseFiles(agent.DatabasePath); err != nil {
		return fmt.Errorf("protect opened encrypted local database and sidecars: %w", err)
	}
	return fn(store)
}

// republishSnapshot regenerates the immutable snapshot and reloads the host if
// the content changed.
//
// Without this an explicit edit would sit in the database until the next
// synchronization round, so the user would see no effect and reasonably
// conclude the command did nothing.
func (agent Agent) republishSnapshot(
	ctx context.Context, store *localstore.Store, change *VocabularyChange,
) error {
	if agent.SnapshotPath == "" {
		return nil
	}
	rebuilt, err := rebuildPrivateSnapshot(ctx, store, agent.BaselinePath, agent.SnapshotPath)
	if err != nil {
		return err
	}
	change.SnapshotRows = rebuilt.TotalRows
	change.SnapshotChanged = rebuilt.Changed
	pending, err := snapshotReloadPending(agent.SnapshotStatePath, rebuilt.digest)
	if err != nil {
		return err
	}
	if !pending {
		return nil
	}
	if agent.Reload == nil {
		return errors.New("private snapshot is pending reload but no platform reload hook is available")
	}
	if err := agent.Reload(ctx); err != nil {
		return fmt.Errorf("reload Rime after atomic snapshot replacement: %w", err)
	}
	if err := markSnapshotReloaded(agent.SnapshotStatePath, rebuilt.digest); err != nil {
		return fmt.Errorf("commit Rime snapshot reload state: %w", err)
	}
	change.SnapshotReloaded = true
	return nil
}

// AddPhrase records a user-reviewed phrase and republishes the snapshot.
//
// Pinning is what makes a phrase outrank ordinary candidates, so it is an
// explicit argument rather than something inferred from the edit.
func (agent Agent) AddPhrase(ctx context.Context, text, pinyin string, pinned bool) (VocabularyChange, error) {
	text, pinyin, err := validateVocabularyInput(text, pinyin)
	if err != nil {
		return VocabularyChange{}, err
	}
	var change VocabularyChange
	err = agent.withPrivateStore(ctx, func(store *localstore.Store) error {
		// A use count of zero is filtered out of the generated snapshot
		// (mergeSnapshotRows), so an explicit add must carry at least one --
		// otherwise the phrase would sit in the database while the user saw no
		// change in the candidate window at all.
		if err := store.SaveExplicit(ctx, localstore.Phrase{
			Text: text, Pinyin: pinyin, Source: "manual", Pinned: pinned, UseCount: 1,
		}); err != nil {
			return err
		}
		stored, err := findPhrase(ctx, store, text, pinyin)
		if err != nil {
			return err
		}
		change.Applied = true
		change.Pinned = stored.Pinned
		change.UseCount = stored.UseCount
		return agent.republishSnapshot(ctx, store, &change)
	})
	return change, err
}

// SetPhrasePinned pins or unpins a phrase the store already holds.
func (agent Agent) SetPhrasePinned(ctx context.Context, text, pinyin string, pinned bool) (VocabularyChange, error) {
	text, pinyin, err := validateVocabularyInput(text, pinyin)
	if err != nil {
		return VocabularyChange{}, err
	}
	var change VocabularyChange
	err = agent.withPrivateStore(ctx, func(store *localstore.Store) error {
		existing, err := findPhrase(ctx, store, text, pinyin)
		if err != nil {
			return err
		}
		// Pinning goes through SaveExplicit so the CRDT clock advances exactly
		// as it does for any other explicit edit, keeping the cross-device
		// merge rules identical. The count is carried over rather than left at
		// zero: SaveExplicit only ever raises it, and a zero count would keep
		// the phrase out of the snapshot no matter how it is pinned.
		count := existing.UseCount
		if count == 0 {
			count = 1
		}
		if err := store.SaveExplicit(ctx, localstore.Phrase{
			Text: text, Pinyin: pinyin, Source: existing.Source, Pinned: pinned, UseCount: count,
		}); err != nil {
			return err
		}
		stored, err := findPhrase(ctx, store, text, pinyin)
		if err != nil {
			return err
		}
		change.Applied = true
		change.Pinned = stored.Pinned
		change.UseCount = stored.UseCount
		return agent.republishSnapshot(ctx, store, &change)
	})
	return change, err
}

// RemovePhrase deletes a phrase and republishes the snapshot.
//
// Deletion is remove-wins in the CRDT, so it converges on the other devices
// rather than being resurrected by their copies.
func (agent Agent) RemovePhrase(ctx context.Context, text, pinyin string) (VocabularyChange, error) {
	text, pinyin, err := validateVocabularyInput(text, pinyin)
	if err != nil {
		return VocabularyChange{}, err
	}
	var change VocabularyChange
	err = agent.withPrivateStore(ctx, func(store *localstore.Store) error {
		if _, err := findPhrase(ctx, store, text, pinyin); err != nil {
			return err
		}
		if err := store.Delete(ctx, text, pinyin); err != nil {
			return err
		}
		change.Applied = true
		return agent.republishSnapshot(ctx, store, &change)
	})
	return change, err
}

// ListVocabulary reports what the local store holds.
//
// Counts always; phrases only when the query explicitly opts in.
func (agent Agent) ListVocabulary(ctx context.Context, query VocabularyQuery) (VocabularySummary, error) {
	if query.Limit <= 0 || query.Limit > maxVocabularyListEntries {
		query.Limit = maxVocabularyListEntries
	}
	summary := VocabularySummary{BySource: map[string]int{}, TextIncluded: query.IncludeText}
	err := agent.withPrivateStore(ctx, func(store *localstore.Store) error {
		snapshot, err := store.Snapshot(ctx)
		if err != nil {
			return err
		}
		live := make([]localstore.Phrase, 0, len(snapshot.Phrases))
		for _, phrase := range snapshot.Phrases {
			if phrase.Deleted {
				continue
			}
			if query.PinnedOnly && !phrase.Pinned {
				continue
			}
			live = append(live, phrase)
			summary.Total++
			if phrase.Pinned {
				summary.Pinned++
			}
			source := phrase.Source
			if source == "" {
				source = "unknown"
			}
			summary.BySource[source]++
		}
		if !query.IncludeText {
			return nil
		}
		// Pinned first, then most used: the phrases a user is most likely to be
		// looking for when they ask to see the list.
		sort.SliceStable(live, func(i, j int) bool {
			if live[i].Pinned != live[j].Pinned {
				return live[i].Pinned
			}
			return live[i].UseCount > live[j].UseCount
		})
		if len(live) > query.Limit {
			live = live[:query.Limit]
		}
		summary.Entries = make([]VocabularyEntry, 0, len(live))
		for _, phrase := range live {
			summary.Entries = append(summary.Entries, VocabularyEntry{
				Text: phrase.Text, Pinyin: phrase.Pinyin, Source: phrase.Source,
				UseCount: phrase.UseCount, Pinned: phrase.Pinned,
			})
		}
		return nil
	})
	return summary, err
}

func findPhrase(ctx context.Context, store *localstore.Store, text, pinyin string) (localstore.Phrase, error) {
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return localstore.Phrase{}, err
	}
	for _, phrase := range snapshot.Phrases {
		if phrase.Text == text && phrase.Pinyin == pinyin && !phrase.Deleted {
			return phrase, nil
		}
	}
	return localstore.Phrase{}, ErrPhraseNotFound
}
