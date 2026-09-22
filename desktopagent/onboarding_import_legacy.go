// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
)

var (
	ErrLegacyInitialSyncRequired = errors.New("complete an initial account sync before restoring legacy vocabulary")
	ErrLegacyDeletedConflict     = errors.New("legacy import includes an entry already deleted from this account")
	ErrLegacyImportChanged       = errors.New("a completed legacy import was edited later; automatic replay is stopped")
	ErrLegacyBaselineConflict    = errors.New("legacy pronunciation conflicts with a static baseline entry")
	ErrLegacyBackupInvalid       = errors.New("legacy backup name or content is invalid")
)

type LegacyImportResult struct {
	Applied           int    `json:"applied"`
	Unchanged         int    `json:"unchanged"`
	StaticRowsSkipped int    `json:"static_rows_skipped"`
	BackupName        string `json:"backup_name"`
	Completed         bool   `json:"completed"`
	// Import stores official mutations only. Normal sync must publish/reload
	// afterward, including when the last attempt was interrupted between rows.
	SnapshotPending bool `json:"snapshot_pending"`
}

// OnboardingImportErrorCode is safe to show in GUI messages. Callers must not
// expose raw filesystem, credential-provider or encrypted-store error strings.
func OnboardingImportErrorCode(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, ErrAlreadyRunning):
		return "busy"
	case errors.Is(err, ErrBaselineConfirmationRequired):
		return "baseline_confirmation_required"
	case errors.Is(err, ErrBaselineChanged):
		return "baseline_changed"
	case errors.Is(err, ErrGeneratedSnapshotImport):
		return "generated_snapshot"
	case errors.Is(err, ErrLegacyInitialSyncRequired):
		return "initial_sync_required"
	case errors.Is(err, ErrLegacyDeletedConflict):
		return "deleted_entry_conflict"
	case errors.Is(err, ErrLegacyImportChanged):
		return "legacy_import_changed"
	case errors.Is(err, ErrLegacyBaselineConflict):
		return "legacy_baseline_conflict"
	case errors.Is(err, ErrLegacyBackupInvalid):
		return "invalid_backup"
	default:
		return "import_validation_failed"
	}
}

// ImportLegacyVocabulary preserves unique entries from an old five-column
// private.tsv through official SaveExplicit transactions, after the account's
// initial sync has brought in deletion records. Count floors and pin union never
// downgrade existing records. Every row is preflighted before any Store write.
// It is resumable after a partial I/O failure: matching rows make no mutation or
// outbox event. A completed import cannot replay over a later unpin/deletion.
// The standard process lock is acquired internally; no native or network call
// is made. The caller follows success with a normal sync.
func (agent Agent) ImportLegacyVocabulary(ctx context.Context, paths Paths, contents []byte) (result LegacyImportResult, err error) {
	if err := validateOnboardingPaths(paths); err != nil {
		return result, err
	}
	if agent.BaselinePath != paths.BaselinePath || agent.DatabasePath != paths.DatabasePath ||
		paths.DatabasePath == "" || filepath.Dir(paths.DatabasePath) != filepath.Dir(paths.LockPath) {
		return result, errors.New("legacy import must use the agent's fixed paths")
	}
	rows, err := parseOnboardingStatic(contents, false)
	if err != nil {
		return result, err
	}
	contents = bytes.Clone(contents)
	err = WithProcessLock(paths.LockPath, func() error {
		if err := prepareOnboardingDirectory(paths); err != nil {
			return err
		}
		baselineBytes, err := readBoundedRegular(paths.BaselinePath, MaxBaselineImportBytes)
		if err != nil {
			return err
		}
		baseline, err := parseOnboardingStatic(baselineBytes, true)
		if err != nil {
			return err
		}
		baseIDs, basePhrases := map[string]bool{}, map[string]bool{}
		for _, row := range baseline {
			baseIDs[snapshotKey(row)] = true
			basePhrases[protocol.CanonicalPhrase(row.Phrase)] = true
		}
		wanted := make([]localstore.Phrase, 0)
		for _, row := range rows {
			if baseIDs[snapshotKey(row)] {
				result.StaticRowsSkipped++
				continue
			}
			if basePhrases[protocol.CanonicalPhrase(row.Phrase)] {
				return ErrLegacyBaselineConflict
			}
			wanted = append(wanted, localstore.Phrase{Text: row.Phrase, Pinyin: row.Pinyin, Source: row.Source, UseCount: row.UseCount, Pinned: row.Pinned})
		}
		if len(wanted) > 4096 {
			return errors.New("legacy import has too many unique entries for one reviewed operation")
		}
		return agent.withPrivateStore(ctx, func(store *localstore.Store) error {
			state, err := store.LoadSyncState(ctx)
			if err != nil {
				return err
			}
			health, err := store.LoadSyncHealth(ctx)
			// Cursor zero is valid for a genuinely empty account. A successful
			// completed round, rather than cursor > 0, proves the initial pull.
			if err != nil || state.Prepared != nil || health.LastSuccessAt <= 0 ||
				health.LastEventCode != "sync_complete" || health.LastFailureClass != localstore.SyncFailureNone || health.Cursor != state.Cursor {
				return ErrLegacyInitialSyncRequired
			}
			snapshot, err := store.Snapshot(ctx)
			if err != nil {
				return err
			}
			existing := make(map[string]localstore.Phrase, len(snapshot.Phrases))
			for _, phrase := range snapshot.Phrases {
				existing[onboardingPhraseKey(phrase)] = phrase
			}
			marker := filepath.Join(filepath.Dir(paths.BaselinePath), "onboarding-backups", "legacy-imported-"+onboardingDigest(contents)+".complete")
			markerBytes := legacyCompletionMarker(onboardingDigest(contents))
			previous, readErr := readBoundedRegular(marker, 128)
			completed := readErr == nil
			if completed && !bytes.Equal(previous, markerBytes) {
				return ErrLegacyBackupInvalid
			}
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return readErr
			}
			updates := make([]localstore.Phrase, 0, len(wanted))
			for _, phrase := range wanted {
				old, found := existing[onboardingPhraseKey(phrase)]
				if found && old.Deleted {
					return ErrLegacyDeletedConflict
				}
				needsWrite := !found || old.UseCount < phrase.UseCount || (phrase.Pinned && !old.Pinned)
				if completed && needsWrite {
					return ErrLegacyImportChanged
				}
				if !needsWrite {
					result.Unchanged++
					continue
				}
				if found {
					count, pin := phrase.UseCount, phrase.Pinned
					phrase = old // Preserve source, spelling, recency and existing CRDT.
					if count > phrase.UseCount {
						phrase.UseCount = count
					}
					phrase.Pinned = phrase.Pinned || pin
					if phrase.Source == "" || strings.TrimSpace(phrase.Source) != phrase.Source {
						return errors.New("existing source cannot be preserved by official import")
					}
				}
				updates = append(updates, phrase)
			}
			result.BackupName, err = backupOnboardingBytes(paths, "legacy-private", contents)
			if err != nil {
				return err
			}
			for _, phrase := range updates {
				if err := store.SaveExplicit(ctx, phrase); err != nil {
					return err
				}
				result.Applied++
			}
			if !completed {
				if err := publishOnboardingNew(marker, markerBytes); err != nil {
					return err
				}
			}
			result.Completed, result.SnapshotPending = true, true
			return nil
		})
	})
	return result, err
}

func onboardingPhraseKey(phrase localstore.Phrase) string {
	return protocol.CanonicalPhrase(phrase.Text) + "\x00" + protocol.CanonicalPinyin(phrase.Pinyin)
}

func legacyCompletionMarker(digest string) []byte {
	return []byte("yunpin-legacy-import-v1\n" + digest + "\n")
}

func legacyBackupDigest(name string) (string, bool) {
	const prefix, suffix = "legacy-private-", ".tsv"
	if len(name) != len(prefix)+64+len(suffix) || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	valid := strings.IndexFunc(digest, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) < 0
	return digest, valid
}

// ImportSavedLegacyVocabulary accepts only a content-addressed backup basename
// created by ImportBaseline; there is no GUI-controlled absolute-path read.
func (agent Agent) ImportSavedLegacyVocabulary(ctx context.Context, paths Paths, backupName string) (LegacyImportResult, error) {
	if err := validateOnboardingPaths(paths); err != nil {
		return LegacyImportResult{}, err
	}
	digest, valid := legacyBackupDigest(backupName)
	if !valid {
		return LegacyImportResult{}, ErrLegacyBackupInvalid
	}
	directory := filepath.Join(filepath.Dir(paths.BaselinePath), "onboarding-backups")
	if !bridgePathComponentsOK(directory, true) {
		return LegacyImportResult{}, ErrLegacyBackupInvalid
	}
	contents, err := readBoundedRegular(filepath.Join(directory, backupName), MaxBaselineImportBytes)
	if err != nil || onboardingDigest(contents) != digest {
		return LegacyImportResult{}, ErrLegacyBackupInvalid
	}
	return agent.ImportLegacyVocabulary(ctx, paths, contents)
}
