// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kukuyan/yunpin-ime/protocol"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	maxEncryptedLearningEvents = 50000
	MaxHabitReportEntries      = 100000
)

type NativeLearningKind string

const (
	NativeLearningSelection  NativeLearningKind = "selection"
	NativeLearningCorrection NativeLearningKind = "correction"
)

// NativeLearningEvent is encrypted before it enters SQLite. It contains only
// the selected word-level identity and a local date bucket: never surrounding
// text, an application/window identifier, or a protected-context marker.
type NativeLearningEvent struct {
	EventID       string             `json:"event_id"`
	DateBucket    string             `json:"date_bucket"`
	Kind          NativeLearningKind `json:"kind"`
	Phrase        Phrase             `json:"phrase"`
	CorrectedFrom *Phrase            `json:"corrected_from,omitempty"`
}

type NativeCorrection struct {
	EventID       string
	DateBucket    string
	CorrectedFrom Phrase
	Replacement   Phrase
}

type HabitStat struct {
	DateBucket         string `json:"date"`
	Phrase             string `json:"phrase,omitempty"`
	Pinyin             string `json:"pinyin,omitempty"`
	SelectionCount     uint64 `json:"selections"`
	CorrectedFromCount uint64 `json:"corrected_from"`
	ReplacementCount   uint64 `json:"replacements"`
}

func (stat HabitStat) NetCorrectionFeedback() int64 {
	if stat.ReplacementCount >= stat.CorrectedFromCount {
		difference := stat.ReplacementCount - stat.CorrectedFromCount
		if difference > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(difference)
	}
	difference := stat.CorrectedFromCount - stat.ReplacementCount
	if difference > math.MaxInt64 {
		return math.MinInt64
	}
	return -int64(difference)
}

type HabitQuery struct {
	SinceDate       string
	CorrectionsOnly bool
	Limit           int
}

func validDateBucket(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validLearningPhrase(phrase Phrase) bool {
	if phrase.Text == "" || len(phrase.Text) > 512 || !utf8.ValidString(phrase.Text) ||
		phrase.Pinyin == "" || len(phrase.Pinyin) > 256 {
		return false
	}
	for _, character := range phrase.Text {
		if character < 0x20 || character == 0x7f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	canonicalPinyin := protocol.CanonicalPinyin(phrase.Pinyin)
	if canonicalPinyin == "" || len(canonicalPinyin) > 256 {
		return false
	}
	for _, character := range []byte(canonicalPinyin) {
		if (character >= 'a' && character <= 'z') || character == ' ' || character == '\'' {
			continue
		}
		return false
	}
	return true
}

func (store *Store) normalizeLearningEvent(event NativeLearningEvent) (NativeLearningEvent, error) {
	if !validNativeEventID(event.EventID) || !validLearningPhrase(event.Phrase) {
		return NativeLearningEvent{}, errors.New("native learning event is invalid")
	}
	if event.DateBucket == "" {
		event.DateBucket = store.now().Format("2006-01-02")
	}
	if !validDateBucket(event.DateBucket) {
		return NativeLearningEvent{}, errors.New("native learning date is invalid")
	}
	event.Phrase.Text = protocol.CanonicalPhrase(event.Phrase.Text)
	event.Phrase.Pinyin = protocol.CanonicalPinyin(event.Phrase.Pinyin)
	if event.Phrase.Text == "" || event.Phrase.Pinyin == "" {
		return NativeLearningEvent{}, errors.New("native learning phrase cannot be canonicalized")
	}
	switch event.Kind {
	case NativeLearningSelection:
		if event.CorrectedFrom != nil {
			return NativeLearningEvent{}, errors.New("selection event contains correction metadata")
		}
	case NativeLearningCorrection:
		if event.CorrectedFrom == nil || !validLearningPhrase(*event.CorrectedFrom) {
			return NativeLearningEvent{}, errors.New("correction event is incomplete")
		}
		corrected := *event.CorrectedFrom
		corrected.Text = protocol.CanonicalPhrase(corrected.Text)
		corrected.Pinyin = protocol.CanonicalPinyin(corrected.Pinyin)
		if corrected.Text == "" || corrected.Text == event.Phrase.Text ||
			corrected.Pinyin == "" || corrected.Pinyin != event.Phrase.Pinyin {
			return NativeLearningEvent{}, errors.New("correction event identities are invalid")
		}
		event.CorrectedFrom = &corrected
	default:
		return NativeLearningEvent{}, errors.New("native learning event kind is invalid")
	}
	return event, nil
}

func (store *Store) learningEventKey(eventID string) ([]byte, error) {
	salt := sha256.Sum256([]byte(eventID))
	key := make([]byte, chacha20poly1305.KeySize)
	reader := hkdf.New(sha256.New, store.dataKey[:], salt[:], []byte("yunpin-local-learning-event-v1"))
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

func (store *Store) sealLearningEvent(event NativeLearningEvent) (nonce, ciphertext []byte, err error) {
	plain, err := json.Marshal(event)
	if err != nil {
		return nil, nil, err
	}
	key, err := store.learningEventKey(event.EventID)
	if err != nil {
		return nil, nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(store.random, nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plain, []byte(event.EventID)), nil
}

func (store *Store) openLearningEvent(eventID string, nonce, ciphertext []byte) (NativeLearningEvent, error) {
	key, err := store.learningEventKey(eventID)
	if err != nil {
		return NativeLearningEvent{}, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return NativeLearningEvent{}, err
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte(eventID))
	if err != nil {
		return NativeLearningEvent{}, errors.New("local learning event authentication failed")
	}
	var event NativeLearningEvent
	if err := json.Unmarshal(plain, &event); err != nil || event.EventID != eventID {
		return NativeLearningEvent{}, errors.New("invalid encrypted learning event")
	}
	normalized, err := store.normalizeLearningEvent(event)
	if err != nil {
		return NativeLearningEvent{}, errors.New("invalid encrypted learning event")
	}
	return normalized, nil
}

func pruneEncryptedLearningEvents(ctx context.Context, transaction *sql.Tx) error {
	_, err := transaction.ExecContext(ctx, `DELETE FROM encrypted_learning_events
WHERE event_id IN (
  SELECT event_id FROM encrypted_learning_events
  ORDER BY created_at DESC, event_id DESC
  LIMIT -1 OFFSET ?
)`, maxEncryptedLearningEvents)
	return err
}

func nativeLearningEventExists(ctx context.Context, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, eventID string) (bool, error) {
	var found int
	err := database.QueryRowContext(ctx, `SELECT EXISTS(
  SELECT 1 FROM consumed_native_events WHERE event_id = ?
  UNION ALL
  SELECT 1 FROM encrypted_learning_events WHERE event_id = ?
)`, eventID, eventID).Scan(&found)
	return found != 0, err
}

func (store *Store) insertLearningEventTx(ctx context.Context, transaction *sql.Tx, event NativeLearningEvent) error {
	nonce, ciphertext, err := store.sealLearningEvent(event)
	if err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO encrypted_learning_events(event_id, nonce, ciphertext, created_at)
VALUES(?, ?, ?, ?)`, event.EventID, nonce, ciphertext, store.now().UnixMilli()); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO consumed_native_events(event_id, consumed_at)
VALUES(?, ?)`, event.EventID, store.now().UnixMilli()); err != nil {
		return err
	}
	if err := pruneEncryptedLearningEvents(ctx, transaction); err != nil {
		return err
	}
	return pruneConsumedNativeReceipts(ctx, transaction)
}

func (store *Store) recordLearningEventOnly(ctx context.Context, event NativeLearningEvent) (NativeSelectionResult, error) {
	store.mutation.Lock()
	defer store.mutation.Unlock()
	normalized, err := store.normalizeLearningEvent(event)
	if err != nil {
		return NativeSelectionResult{}, err
	}
	found, err := nativeLearningEventExists(ctx, store.db, normalized.EventID)
	if err != nil {
		return NativeSelectionResult{}, err
	}
	if found {
		return NativeSelectionResult{Duplicate: true}, nil
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeSelectionResult{}, err
	}
	defer transaction.Rollback()
	if err := store.insertLearningEventTx(ctx, transaction, normalized); err != nil {
		return NativeSelectionResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return NativeSelectionResult{}, err
	}
	return NativeSelectionResult{}, nil
}

func (store *Store) RecordNativeLocalSelection(ctx context.Context, selection NativeSelection) (NativeSelectionResult, error) {
	return store.recordLearningEventOnly(ctx, NativeLearningEvent{
		EventID: selection.EventID, DateBucket: selection.DateBucket,
		Kind: NativeLearningSelection, Phrase: selection.Phrase,
	})
}

func (store *Store) RecordNativeCorrection(ctx context.Context, correction NativeCorrection) (NativeSelectionResult, error) {
	wrong := correction.CorrectedFrom
	return store.recordLearningEventOnly(ctx, NativeLearningEvent{
		EventID: correction.EventID, DateBucket: correction.DateBucket,
		Kind: NativeLearningCorrection, Phrase: correction.Replacement,
		CorrectedFrom: &wrong,
	})
}

func saturatingIncrement(value *uint64) {
	if *value != math.MaxUint64 {
		(*value)++
	}
}

func (store *Store) QueryHabits(ctx context.Context, query HabitQuery) ([]HabitStat, error) {
	if query.SinceDate != "" && !validDateBucket(query.SinceDate) {
		return nil, errors.New("habit report date is invalid")
	}
	if query.Limit == 0 {
		query.Limit = 100
	}
	if query.Limit < 1 || query.Limit > MaxHabitReportEntries {
		return nil, errors.New("habit report limit is invalid")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT event_id, nonce, ciphertext
FROM encrypted_learning_events ORDER BY created_at, event_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := make(map[string]*HabitStat)
	statFor := func(date string, phrase Phrase) *HabitStat {
		key := date + "\x1f" + phrase.Text + "\x1f" + phrase.Pinyin
		if found := stats[key]; found != nil {
			return found
		}
		stat := &HabitStat{DateBucket: date, Phrase: phrase.Text, Pinyin: phrase.Pinyin}
		stats[key] = stat
		return stat
	}
	for rows.Next() {
		var eventID string
		var nonce, ciphertext []byte
		if err := rows.Scan(&eventID, &nonce, &ciphertext); err != nil {
			return nil, err
		}
		event, err := store.openLearningEvent(eventID, nonce, ciphertext)
		if err != nil {
			return nil, err
		}
		if query.SinceDate != "" && event.DateBucket < query.SinceDate {
			continue
		}
		switch event.Kind {
		case NativeLearningSelection:
			saturatingIncrement(&statFor(event.DateBucket, event.Phrase).SelectionCount)
		case NativeLearningCorrection:
			saturatingIncrement(&statFor(event.DateBucket, *event.CorrectedFrom).CorrectedFromCount)
			saturatingIncrement(&statFor(event.DateBucket, event.Phrase).ReplacementCount)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]HabitStat, 0, len(stats))
	for _, stat := range stats {
		if query.CorrectionsOnly && stat.CorrectedFromCount == 0 && stat.ReplacementCount == 0 {
			continue
		}
		result = append(result, *stat)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].DateBucket != result[right].DateBucket {
			return result[left].DateBucket > result[right].DateBucket
		}
		leftCorrections := result[left].CorrectedFromCount + result[left].ReplacementCount
		rightCorrections := result[right].CorrectedFromCount + result[right].ReplacementCount
		if leftCorrections != rightCorrections {
			return leftCorrections > rightCorrections
		}
		if result[left].SelectionCount != result[right].SelectionCount {
			return result[left].SelectionCount > result[right].SelectionCount
		}
		if result[left].Phrase != result[right].Phrase {
			return result[left].Phrase < result[right].Phrase
		}
		return result[left].Pinyin < result[right].Pinyin
	})
	if len(result) > query.Limit {
		result = result[:query.Limit]
	}
	return result, nil
}

func CorrectionScores(stats []HabitStat) map[string]int32 {
	totals := make(map[string]int64)
	for _, stat := range stats {
		key := protocol.CanonicalPhrase(stat.Phrase) + "\x00" + protocol.CanonicalPinyin(stat.Pinyin)
		feedback := stat.NetCorrectionFeedback()
		if feedback > 0 && totals[key] > math.MaxInt64-feedback {
			totals[key] = math.MaxInt64
		} else if feedback < 0 && totals[key] < math.MinInt64-feedback {
			totals[key] = math.MinInt64
		} else {
			totals[key] += feedback
		}
	}
	result := make(map[string]int32, len(totals))
	for key, value := range totals {
		value = max(int64(-1000000), min(int64(1000000), value))
		result[key] = int32(value)
	}
	return result
}

func RedactHabitText(stats []HabitStat) []HabitStat {
	redacted := make([]HabitStat, len(stats))
	copy(redacted, stats)
	for index := range redacted {
		redacted[index].Phrase = ""
		redacted[index].Pinyin = ""
	}
	return redacted
}

func ValidHabitSince(value string) bool {
	return strings.TrimSpace(value) == "" || validDateBucket(value)
}
