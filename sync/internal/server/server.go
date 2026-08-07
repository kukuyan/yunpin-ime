// SPDX-License-Identifier: Apache-2.0

// Package server implements YunPin's opaque, end-to-end encrypted sync relay.
package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	_ "modernc.org/sqlite"
)

const (
	protocolVersion   = 1
	paddingBucket     = 512
	maxBodyBytes      = 1 << 20
	maxCiphertext     = 524816    // protocol.MaxEnvelopeCiphertext (512 KiB canonical CBOR payload)
	maxSealedBoxWire  = 256 << 10 // protocol.MaxSealedBoxWireSize
	maxUploadBatch    = 256
	defaultSyncLimit  = 256
	maximumSyncLimit  = 256
	maxDownloadBytes  = maxCiphertext
	pairingLifetime   = 10 * time.Minute
	rateWindow        = time.Minute
	requestsPerWindow = 240
)

var canonicalCBOR cbor.EncMode

func init() {
	mode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	canonicalCBOR = mode
}

//go:embed migrations/*.sql
var migrations embed.FS

// Server is both the HTTP handler and owner of the SQLite connection pool.
type Server struct {
	db      *sql.DB
	logger  *log.Logger
	now     func() time.Time
	limiter *ipLimiter
	handler http.Handler
}

type deviceIdentity struct {
	ID          string
	AccountID   string
	Ed25519Key  ed25519.PublicKey
	X25519Key   []byte
	CreatedUnix int64
}

type contextKey string

const identityKey contextKey = "yunpin-device"

type ipLimiter struct {
	mu        sync.Mutex
	entries   map[string]rateEntry
	limit     int
	window    time.Duration
	lastSweep time.Time
}

type rateEntry struct {
	start time.Time
	count int
}

// New opens the database, enables WAL mode, applies migrations, and builds the handler.
func New(ctx context.Context, databasePath string, logOutput io.Writer) (*Server, error) {
	if databasePath == "" {
		return nil, errors.New("database path is required")
	}
	if logOutput == nil {
		logOutput = io.Discard
	}
	dsn := databasePath
	if databasePath != ":memory:" && !strings.HasPrefix(databasePath, "file:") {
		dsn = "file:" + databasePath
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	// modernc applies each _pragma to every connection opened by database/sql.
	dsn += separator + "_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Server{
		db:     db,
		logger: log.New(logOutput, "yunpin-sync ", log.LstdFlags|log.LUTC),
		now:    time.Now,
		limiter: &ipLimiter{
			entries: make(map[string]rateEntry),
			limit:   requestsPerWindow,
			window:  rateWindow,
		},
	}
	if err := s.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s.handler = s.withLogging(s.withRateLimit(http.HandlerFunc(s.route)))
	return s, nil
}

func (s *Server) initialize(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, statement := range pragmas {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  name TEXT PRIMARY KEY,
  checksum TEXT NOT NULL CHECK(length(checksum) = 64),
  applied_at INTEGER NOT NULL
)`); err != nil {
		return fmt.Errorf("initialize migration ledger: %w", err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		checksumBytes := sha256.Sum256(body)
		checksum := hex.EncodeToString(checksumBytes[:])
		var recorded string
		err = s.db.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE name = ?", entry.Name()).Scan(&recorded)
		if err == nil {
			if subtle.ConstantTimeCompare([]byte(recorded), []byte(checksum)) != 1 {
				return fmt.Errorf("migration %s checksum mismatch", entry.Name())
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read migration ledger %s: %w", entry.Name(), err)
		}
		transaction, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = transaction.ExecContext(ctx, string(body)); err == nil {
			_, err = transaction.ExecContext(ctx, "INSERT INTO schema_migrations(name, checksum, applied_at) VALUES(?, ?, ?)", entry.Name(), checksum, s.now().UnixMilli())
		}
		if err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// Close releases the SQLite connection pool.
func (s *Server) Close() error { return s.db.Close() }

// DB exposes the database only to operational checks and package tests.
func (s *Server) DB() *sql.DB { return s.db }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	switch {
	case path == "/healthz" && r.Method == http.MethodGet:
		s.health(w, r)
	case path == "/v1/accounts" && r.Method == http.MethodPost:
		s.createAccount(w, r)
	case strings.HasPrefix(path, "/v1/accounts/") && strings.HasSuffix(path, "/recover") && r.Method == http.MethodPost:
		parts := strings.Split(path, "/")
		if len(parts) != 5 {
			notFound(w)
			return
		}
		s.recoverAccount(w, r, parts[3])
	case path == "/v1/pairings" && r.Method == http.MethodPost:
		s.requireAuth(s.createPairing)(w, r)
	case strings.HasPrefix(path, "/v1/pairings/"):
		s.routePairing(w, r, path)
	case path == "/v1/sync" && r.Method == http.MethodPost:
		s.requireAuth(s.syncEnvelopes)(w, r)
	case path == "/v1/devices" && r.Method == http.MethodGet:
		s.requireAuth(s.listDevices)(w, r)
	case strings.HasPrefix(path, "/v1/devices/") && r.Method == http.MethodDelete:
		parts := strings.Split(path, "/")
		if len(parts) != 4 {
			notFound(w)
			return
		}
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) { s.revokeDevice(w, r, parts[3]) })(w, r)
	case path == "/v1/keyring" && r.Method == http.MethodGet:
		s.requireAuth(s.getKeyring)(w, r)
	case path == "/v1/keyring" && r.Method == http.MethodPut:
		s.requireAuth(s.putKeyring)(w, r)
	default:
		notFound(w)
	}
}

func (s *Server) routePairing(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(path, "/")
	if len(parts) == 4 && r.Method == http.MethodGet {
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) { s.getPairing(w, r, parts[3]) })(w, r)
		return
	}
	if len(parts) == 4 && r.Method == http.MethodPut {
		s.joinPairing(w, r, parts[3])
		return
	}
	if len(parts) == 5 && parts[4] == "approve" && r.Method == http.MethodPost {
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) { s.approvePairing(w, r, parts[3]) })(w, r)
		return
	}
	if len(parts) == 5 && parts[4] == "claim" && r.Method == http.MethodPost {
		s.claimPairing(w, r, parts[3])
		return
	}
	notFound(w)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type deviceRegistration struct {
	DeviceNameCiphertext string `json:"device_name_ciphertext"`
	Ed25519PublicKey     string `json:"ed25519_public_key"`
	X25519PublicKey      string `json:"x25519_public_key"`
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RecoveryAuthentication string `json:"recovery_authentication"`
		deviceRegistration
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	nameCiphertext, edKey, xKey, err := validateRegistration(input.deviceRegistration)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	recoveryAuthentication, err := decodeSized(input.RecoveryAuthentication, 32, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_recovery_authentication")
		return
	}
	accountID, err := randomID(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	deviceID, err := randomID(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	deviceToken, err := randomSecret(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO accounts(id, recovery_authentication_hash, created_at) VALUES(?, ?, ?)", accountID, digestBytes(recoveryAuthentication), now); err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO devices(id, account_id, name_ciphertext, token_hash, ed25519_public_key, x25519_public_key, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)`, deviceID, accountID, nameCiphertext, digest(deviceToken), edKey, xKey, now)
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"account_id": accountID, "device_id": deviceID, "device_token": deviceToken,
	})
}

func (s *Server) recoverAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	if !validID(accountID) {
		writeError(w, http.StatusNotFound, "account_not_found")
		return
	}
	var input struct {
		RecoveryAuthentication string `json:"recovery_authentication"`
		deviceRegistration
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	nameCiphertext, edKey, xKey, err := validateRegistration(input.deviceRegistration)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	recoveryAuthentication, err := decodeSized(input.RecoveryAuthentication, 32, 32)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_recovery_authentication")
		return
	}
	var expected []byte
	if err := s.db.QueryRowContext(r.Context(), "SELECT recovery_authentication_hash FROM accounts WHERE id = ?", accountID).Scan(&expected); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_recovery_authentication")
		return
	}
	got := digestBytes(recoveryAuthentication)
	if len(expected) != len(got) || subtle.ConstantTimeCompare(expected, got) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_recovery_authentication")
		return
	}
	deviceID, err := randomID(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	deviceToken, err := randomSecret(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO devices(id, account_id, name_ciphertext, token_hash, ed25519_public_key, x25519_public_key, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, deviceID, accountID, nameCiphertext, digest(deviceToken), edKey, xKey, s.now().UnixMilli())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"account_id": accountID, "device_id": deviceID, "device_token": deviceToken})
}

func (s *Server) createPairing(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r)
	pairingID, err := randomID(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	pairingSecret, err := randomSecret(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	now := s.now()
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO pairings(id, account_id, creator_device_id, secret_hash, state, expires_at, created_at)
		VALUES(?, ?, ?, ?, 'created', ?, ?)`, pairingID, identity.AccountID, identity.ID, digest(pairingSecret), now.Add(pairingLifetime).UnixMilli(), now.UnixMilli())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"pairing_id": pairingID, "pairing_secret": pairingSecret,
		"creator_x25519_public_key": base64.RawURLEncoding.EncodeToString(identity.X25519Key),
		"expires_at":                now.Add(pairingLifetime).UTC().Format(time.RFC3339),
	})
}

func (s *Server) joinPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	var input struct {
		PairingSecret string `json:"pairing_secret"`
		deviceRegistration
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	nameCiphertext, edKey, xKey, err := validateRegistration(input.deviceRegistration)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.db.ExecContext(r.Context(), `UPDATE pairings SET state = 'joined', pending_name_ciphertext = ?, pending_ed25519_public_key = ?, pending_x25519_public_key = ?
		WHERE id = ? AND secret_hash = ? AND state = 'created' AND expires_at > ?`, nameCiphertext, edKey, xKey, pairingID, digest(input.PairingSecret), s.now().UnixMilli())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_or_expired_pairing")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "joined"})
}

func (s *Server) getPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	identity := mustIdentity(r)
	var state string
	var nameCiphertext, ed25519Key, x25519Key []byte
	var expiresAt int64
	err := s.db.QueryRowContext(r.Context(), `SELECT state, pending_name_ciphertext, pending_ed25519_public_key,
		pending_x25519_public_key, expires_at FROM pairings
		WHERE id = ? AND account_id = ? AND creator_device_id = ?`, pairingID, identity.AccountID, identity.ID).
		Scan(&state, &nameCiphertext, &ed25519Key, &x25519Key, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "pairing_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	response := map[string]any{
		"pairing_id": pairingID,
		"state":      state,
		"expires_at": time.UnixMilli(expiresAt).UTC(),
		"expired":    expiresAt <= s.now().UnixMilli(),
	}
	if len(nameCiphertext) != 0 {
		response["device_name_ciphertext"] = base64.RawURLEncoding.EncodeToString(nameCiphertext)
		response["ed25519_public_key"] = base64.RawURLEncoding.EncodeToString(ed25519Key)
		response["x25519_public_key"] = base64.RawURLEncoding.EncodeToString(x25519Key)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) approvePairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	identity := mustIdentity(r)
	var input struct {
		EncryptedKeyring string `json:"encrypted_keyring"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	keyring, err := decodeSealedBoxWire(input.EncryptedKeyring)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encrypted_keyring")
		return
	}
	deviceID, err := randomID(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	result, err := s.db.ExecContext(r.Context(), `UPDATE pairings SET state = 'approved', new_device_id = ?, encrypted_keyring = ?
		WHERE id = ? AND account_id = ? AND creator_device_id = ? AND state = 'joined' AND expires_at > ?`,
		deviceID, keyring, pairingID, identity.AccountID, identity.ID, s.now().UnixMilli())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusConflict, "pairing_not_ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "approved"})
}

func (s *Server) claimPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	var input struct {
		PairingSecret string `json:"pairing_secret"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	var accountID, deviceID string
	var nameCiphertext, expected, edKey, xKey, encryptedKeyring []byte
	var expiresAt int64
	err = tx.QueryRowContext(r.Context(), `SELECT account_id, new_device_id, pending_name_ciphertext, secret_hash, pending_ed25519_public_key,
		pending_x25519_public_key, encrypted_keyring, expires_at FROM pairings WHERE id = ? AND state = 'approved'`, pairingID).
		Scan(&accountID, &deviceID, &nameCiphertext, &expected, &edKey, &xKey, &encryptedKeyring, &expiresAt)
	if err != nil || expiresAt <= s.now().UnixMilli() || subtle.ConstantTimeCompare(expected, digest(input.PairingSecret)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_or_expired_pairing")
		return
	}
	deviceToken, err := randomSecret(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_source_unavailable")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO devices(id, account_id, name_ciphertext, token_hash, ed25519_public_key, x25519_public_key, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, deviceID, accountID, nameCiphertext, digest(deviceToken), edKey, xKey, s.now().UnixMilli()); err == nil {
		_, err = tx.ExecContext(r.Context(), "UPDATE pairings SET state = 'claimed', claimed_at = ? WHERE id = ? AND state = 'approved'", s.now().UnixMilli(), pairingID)
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, http.StatusConflict, "pairing_already_claimed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"account_id": accountID, "device_id": deviceID, "device_token": deviceToken,
		"encrypted_keyring": base64.RawURLEncoding.EncodeToString(encryptedKeyring),
	})
}

// Envelope is the JSON transport form of protocol.Header plus opaque payload.
// AccountID and the upload DeviceID are inferred from bearer authentication.
type Envelope struct {
	Version      uint64 `json:"version"`
	DeviceID     string `json:"device_id,omitempty"`
	DeviceSeq    uint64 `json:"device_seq"`
	ObjectID     string `json:"object_id"`
	KeyEpoch     uint64 `json:"key_epoch"`
	PreviousHash string `json:"previous_hash,omitempty"`
	Nonce        string `json:"nonce"`
	Ciphertext   string `json:"ciphertext"`
	Signature    string `json:"signature"`
	Cursor       int64  `json:"cursor,omitempty"`
}

// Header mirrors protocol.Header. Integer CBOR keys are part of protocol v1.
type Header struct {
	Version      uint64 `cbor:"1,keyasint"`
	AccountID    []byte `cbor:"2,keyasint"`
	ObjectID     []byte `cbor:"3,keyasint"`
	KeyEpoch     uint64 `cbor:"4,keyasint"`
	DeviceID     []byte `cbor:"5,keyasint"`
	DeviceSeq    uint64 `cbor:"6,keyasint"`
	PreviousHash []byte `cbor:"7,keyasint,omitempty"`
	Nonce        []byte `cbor:"8,keyasint"`
}

type decodedEnvelope struct {
	Envelope
	objectID, previousHash, nonce, ciphertext, signature []byte
	recordHash                                           []byte
}

type syncRejection struct {
	DeviceSeq uint64 `json:"device_seq"`
	Code      string `json:"code"`
}

func (s *Server) syncEnvelopes(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r)
	var input struct {
		Cursor    int64      `json:"cursor"`
		AckCursor int64      `json:"ack_cursor"`
		Limit     int        `json:"limit,omitempty"`
		Envelopes []Envelope `json:"envelopes,omitempty"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Cursor < 0 || input.AckCursor < 0 || len(input.Envelopes) > maxUploadBatch {
		writeError(w, http.StatusBadRequest, "invalid_sync_batch")
		return
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultSyncLimit
	}
	if limit < 1 || limit > maximumSyncLimit {
		writeError(w, http.StatusBadRequest, "invalid_limit")
		return
	}
	decoded := make([]decodedEnvelope, 0, len(input.Envelopes))
	for _, envelope := range input.Envelopes {
		decodedEnvelope, err := validateEnvelope(identity, envelope)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		decoded = append(decoded, decodedEnvelope)
	}
	sort.SliceStable(decoded, func(left, right int) bool { return decoded[left].DeviceSeq < decoded[right].DeviceSeq })
	batchRejected := make([]syncRejection, 0)
	unique := make([]decodedEnvelope, 0, len(decoded))
	for start := 0; start < len(decoded); {
		end := start + 1
		conflict := false
		for end < len(decoded) && decoded[end].DeviceSeq == decoded[start].DeviceSeq {
			if !bytes.Equal(decoded[end].recordHash, decoded[start].recordHash) {
				conflict = true
			}
			end++
		}
		if conflict {
			batchRejected = append(batchRejected, syncRejection{DeviceSeq: decoded[start].DeviceSeq, Code: "sequence_conflict"})
		} else {
			unique = append(unique, decoded[start])
		}
		start = end
	}
	decoded = unique

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	accepted := make([]uint64, 0, len(decoded))
	rejected := batchRejected
	for _, envelope := range decoded {
		var existingHash []byte
		err := tx.QueryRowContext(r.Context(), `SELECT record_hash FROM envelopes
			WHERE account_id = ? AND device_id = ? AND device_seq = ?`, identity.AccountID, identity.ID, envelope.DeviceSeq).Scan(&existingHash)
		if err == nil {
			if bytes.Equal(existingHash, envelope.recordHash) {
				accepted = append(accepted, envelope.DeviceSeq)
			} else {
				rejected = append(rejected, syncRejection{DeviceSeq: envelope.DeviceSeq, Code: "sequence_conflict"})
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}

		var lastSequence uint64
		var lastHash []byte
		err = tx.QueryRowContext(r.Context(), `SELECT device_seq, record_hash FROM envelopes
			WHERE account_id = ? AND device_id = ? ORDER BY device_seq DESC LIMIT 1`, identity.AccountID, identity.ID).Scan(&lastSequence, &lastHash)
		if errors.Is(err, sql.ErrNoRows) {
			lastSequence = 0
			lastHash = nil
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if envelope.DeviceSeq != lastSequence+1 {
			code := "sequence_gap"
			if envelope.DeviceSeq <= lastSequence {
				code = "sequence_conflict"
			}
			rejected = append(rejected, syncRejection{DeviceSeq: envelope.DeviceSeq, Code: code})
			continue
		}
		if !bytes.Equal(envelope.previousHash, lastHash) {
			rejected = append(rejected, syncRejection{DeviceSeq: envelope.DeviceSeq, Code: "previous_hash_mismatch"})
			continue
		}
		_, err = tx.ExecContext(r.Context(), `INSERT INTO envelopes
			(account_id, device_id, device_seq, version, object_id, key_epoch, previous_hash, nonce, ciphertext, signature, record_hash, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, identity.AccountID, identity.ID, envelope.DeviceSeq,
			envelope.Version, envelope.objectID, envelope.KeyEpoch, nullableBytes(envelope.previousHash), envelope.nonce,
			envelope.ciphertext, envelope.signature, envelope.recordHash, s.now().UnixMilli())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		accepted = append(accepted, envelope.DeviceSeq)
	}
	var accountMaxCursor int64
	if err := tx.QueryRowContext(r.Context(), "SELECT COALESCE(MAX(cursor), 0) FROM envelopes WHERE account_id = ?", identity.AccountID).Scan(&accountMaxCursor); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if input.Cursor > accountMaxCursor || input.AckCursor > accountMaxCursor {
		writeError(w, http.StatusBadRequest, "invalid_cursor")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE devices SET ack_cursor = CASE WHEN ack_cursor < ? THEN ? ELSE ack_cursor END
		WHERE id = ? AND account_id = ?`, input.AckCursor, input.AckCursor, identity.ID, identity.AccountID); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}

	rows, err := tx.QueryContext(r.Context(), `SELECT cursor, device_id, device_seq, version, object_id, key_epoch,
		previous_hash, nonce, ciphertext, signature FROM envelopes
		WHERE account_id = ? AND cursor > ? ORDER BY cursor LIMIT ?`, identity.AccountID, input.Cursor, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	outgoing := make([]Envelope, 0, limit)
	downloadBytes := 0
	hasMore := false
	for rows.Next() {
		var envelope Envelope
		var objectID, previousHash, nonce, ciphertext, signature []byte
		if err := rows.Scan(&envelope.Cursor, &envelope.DeviceID, &envelope.DeviceSeq, &envelope.Version, &objectID,
			&envelope.KeyEpoch, &previousHash, &nonce, &ciphertext, &signature); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if len(ciphertext) < paddingBucket+16 || len(ciphertext) > maxCiphertext || (len(ciphertext)-16)%paddingBucket != 0 {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if len(outgoing) >= limit || (len(outgoing) > 0 && downloadBytes+len(ciphertext) > maxDownloadBytes) {
			hasMore = true
			break
		}
		envelope.ObjectID = hex.EncodeToString(objectID)
		if len(previousHash) != 0 {
			envelope.PreviousHash = base64.RawURLEncoding.EncodeToString(previousHash)
		}
		envelope.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
		envelope.Ciphertext = base64.RawURLEncoding.EncodeToString(ciphertext)
		envelope.Signature = base64.RawURLEncoding.EncodeToString(signature)
		outgoing = append(outgoing, envelope)
		downloadBytes += len(ciphertext)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	nextCursor := input.Cursor
	if len(outgoing) > 0 {
		nextCursor = outgoing[len(outgoing)-1].Cursor
	}
	var currentKeyEpoch int64
	if err := tx.QueryRowContext(r.Context(), "SELECT COALESCE(MAX(epoch), 0) FROM keyrings WHERE account_id = ?", identity.AccountID).Scan(&currentKeyEpoch); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted_sequences": accepted,
		"rejected_sequences": rejected,
		"envelopes":          outgoing,
		"next_cursor":        nextCursor,
		"has_more":           hasMore,
		"current_key_epoch":  currentKeyEpoch,
	})
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, name_ciphertext, ed25519_public_key, x25519_public_key, created_at, revoked_at
		FROM devices WHERE account_id = ? ORDER BY created_at`, identity.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer rows.Close()
	devices := make([]map[string]any, 0)
	for rows.Next() {
		var id string
		var nameCiphertext, ed25519Key, x25519Key []byte
		var created int64
		var revoked sql.NullInt64
		if err := rows.Scan(&id, &nameCiphertext, &ed25519Key, &x25519Key, &created, &revoked); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		item := map[string]any{
			"id": id, "name_ciphertext": base64.RawURLEncoding.EncodeToString(nameCiphertext),
			"ed25519_public_key": base64.RawURLEncoding.EncodeToString(ed25519Key),
			"x25519_public_key":  base64.RawURLEncoding.EncodeToString(x25519Key),
			"created_at":         time.UnixMilli(created).UTC(), "current": id == identity.ID, "revoked": revoked.Valid,
		}
		if revoked.Valid {
			item["revoked_at"] = time.UnixMilli(revoked.Int64).UTC()
		}
		devices = append(devices, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request, deviceID string) {
	identity := mustIdentity(r)
	if deviceID == identity.ID {
		writeError(w, http.StatusConflict, "cannot_revoke_current_device")
		return
	}
	result, err := s.db.ExecContext(r.Context(), `UPDATE devices SET revoked_at = ? WHERE id = ? AND account_id = ? AND revoked_at IS NULL`, s.now().UnixMilli(), deviceID, identity.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusNotFound, "device_not_found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getKeyring(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r)
	rows, err := s.db.QueryContext(r.Context(), `SELECT epoch, ciphertext, writer_device_id, created_at FROM keyrings WHERE account_id = ? ORDER BY epoch`, identity.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var epoch, created int64
		var ciphertext []byte
		var writer string
		if err := rows.Scan(&epoch, &ciphertext, &writer, &created); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		items = append(items, map[string]any{"epoch": epoch, "ciphertext": base64.RawURLEncoding.EncodeToString(ciphertext), "writer_device_id": writer, "created_at": time.UnixMilli(created).UTC()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keyrings": items})
}

func (s *Server) putKeyring(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r)
	var input struct {
		Epoch      int64  `json:"epoch"`
		Ciphertext string `json:"ciphertext"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	ciphertext, err := decodeSealedBoxWire(input.Ciphertext)
	if err != nil || input.Epoch < 1 {
		writeError(w, http.StatusBadRequest, "invalid_keyring")
		return
	}
	result, err := s.db.ExecContext(r.Context(), `INSERT OR IGNORE INTO keyrings(account_id, epoch, ciphertext, writer_device_id, created_at)
		VALUES(?, ?, ?, ?, ?)`, identity.AccountID, input.Epoch, ciphertext, identity.ID, s.now().UnixMilli())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var existing []byte
		if err := s.db.QueryRowContext(r.Context(), "SELECT ciphertext FROM keyrings WHERE account_id = ? AND epoch = ?", identity.AccountID, input.Epoch).Scan(&existing); err != nil || subtle.ConstantTimeCompare(existing, ciphertext) != 1 {
			writeError(w, http.StatusConflict, "keyring_epoch_conflict")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int64{"epoch": input.Epoch})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || strings.Contains(header[7:], " ") {
			writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		token := strings.TrimSpace(header[7:])
		if token == "" {
			writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		var identity deviceIdentity
		var edKey, xKey []byte
		err := s.db.QueryRowContext(r.Context(), `SELECT id, account_id, ed25519_public_key, x25519_public_key, created_at
			FROM devices WHERE token_hash = ? AND revoked_at IS NULL`, digest(token)).
			Scan(&identity.ID, &identity.AccountID, &edKey, &xKey, &identity.CreatedUnix)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_device_token")
			return
		}
		identity.Ed25519Key = ed25519.PublicKey(edKey)
		identity.X25519Key = xKey
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey, identity)))
	}
}

func mustIdentity(r *http.Request) deviceIdentity {
	return r.Context().Value(identityKey).(deviceIdentity)
}

func validateRegistration(input deviceRegistration) ([]byte, []byte, []byte, error) {
	nameCiphertext, err := decodeSized(input.DeviceNameCiphertext, 16, 512)
	if err != nil {
		return nil, nil, nil, errors.New("invalid_device_name_ciphertext")
	}
	edKey, err := decodeSized(input.Ed25519PublicKey, ed25519.PublicKeySize, ed25519.PublicKeySize)
	if err != nil {
		return nil, nil, nil, errors.New("invalid_ed25519_public_key")
	}
	xKey, err := decodeSized(input.X25519PublicKey, 32, 32)
	if err != nil {
		return nil, nil, nil, errors.New("invalid_x25519_public_key")
	}
	return nameCiphertext, edKey, xKey, nil
}

func validateEnvelope(identity deviceIdentity, envelope Envelope) (decodedEnvelope, error) {
	if envelope.DeviceID != "" || envelope.Cursor != 0 || envelope.Version != protocolVersion || envelope.DeviceSeq < 1 || envelope.DeviceSeq > math.MaxInt64 || envelope.KeyEpoch < 1 || envelope.KeyEpoch > math.MaxInt64 {
		return decodedEnvelope{}, errors.New("invalid_envelope_metadata")
	}
	objectID, err := hex.DecodeString(envelope.ObjectID)
	if err != nil || len(objectID) != 16 {
		return decodedEnvelope{}, errors.New("invalid_envelope_object_id")
	}
	var previousHash []byte
	if envelope.PreviousHash != "" {
		previousHash, err = decodeSized(envelope.PreviousHash, sha256.Size, sha256.Size)
		if err != nil {
			return decodedEnvelope{}, errors.New("invalid_envelope_previous_hash")
		}
	}
	nonce, err := decodeSized(envelope.Nonce, 24, 24)
	if err != nil {
		return decodedEnvelope{}, errors.New("invalid_envelope_nonce")
	}
	ciphertext, err := decodeSized(envelope.Ciphertext, paddingBucket+16, maxCiphertext)
	if err != nil || (len(ciphertext)-16)%paddingBucket != 0 {
		return decodedEnvelope{}, errors.New("invalid_envelope_ciphertext")
	}
	signature, err := decodeSized(envelope.Signature, ed25519.SignatureSize, ed25519.SignatureSize)
	if err != nil {
		return decodedEnvelope{}, errors.New("invalid_envelope_signature")
	}
	headerBytes, err := CanonicalHeader(identity.AccountID, identity.ID, envelope, previousHash, nonce)
	if err != nil {
		return decodedEnvelope{}, errors.New("invalid_envelope_metadata")
	}
	signedBytes := make([]byte, 0, len(headerBytes)+len(ciphertext))
	signedBytes = append(signedBytes, headerBytes...)
	signedBytes = append(signedBytes, ciphertext...)
	if !ed25519.Verify(identity.Ed25519Key, signedBytes, signature) {
		return decodedEnvelope{}, errors.New("invalid_envelope_signature")
	}
	recordDigest := sha256.New()
	_, _ = recordDigest.Write(signedBytes)
	_, _ = recordDigest.Write(signature)
	return decodedEnvelope{
		Envelope: envelope, objectID: objectID, previousHash: previousHash, nonce: nonce,
		ciphertext: ciphertext, signature: signature, recordHash: recordDigest.Sum(nil),
	}, nil
}

// CanonicalHeader returns the protocol v1 canonical-CBOR header bytes. These
// bytes are the XChaCha20-Poly1305 AAD and the prefix of the Ed25519 message.
func CanonicalHeader(accountID, deviceID string, envelope Envelope, previousHash, nonce []byte) ([]byte, error) {
	accountBytes, err := hex.DecodeString(accountID)
	if err != nil || len(accountBytes) != 16 {
		return nil, errors.New("invalid account ID")
	}
	deviceBytes, err := hex.DecodeString(deviceID)
	if err != nil || len(deviceBytes) != 16 {
		return nil, errors.New("invalid device ID")
	}
	objectBytes, err := hex.DecodeString(envelope.ObjectID)
	if err != nil || len(objectBytes) != 16 {
		return nil, errors.New("invalid object ID")
	}
	if len(previousHash) != 0 && len(previousHash) != sha256.Size {
		return nil, errors.New("invalid previous hash")
	}
	if len(nonce) != 24 {
		return nil, errors.New("invalid nonce")
	}
	return canonicalCBOR.Marshal(Header{
		Version: envelope.Version, AccountID: accountBytes, ObjectID: objectBytes, KeyEpoch: envelope.KeyEpoch,
		DeviceID: deviceBytes, DeviceSeq: envelope.DeviceSeq, PreviousHash: previousHash, Nonce: nonce,
	})
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func decodeSized(value string, min, max int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) < min || len(decoded) > max {
		return nil, errors.New("invalid encoded value")
	}
	return decoded, nil
}

// decodeSealedBoxWire validates only the public YPBX framing. The relay never
// opens the ciphertext or receives the client key.
func decodeSealedBoxWire(value string) ([]byte, error) {
	const headerSize = 4 + 1 + 4 + 24
	decoded, err := decodeSized(value, headerSize+16, maxSealedBoxWire)
	if err != nil || !bytes.Equal(decoded[:4], []byte("YPBX")) || decoded[4] != 1 {
		return nil, errors.New("invalid sealed-box wire")
	}
	ciphertextLength := uint64(binary.BigEndian.Uint32(decoded[5:9]))
	if ciphertextLength < 16 || ciphertextLength > uint64(maxSealedBoxWire-headerSize) || uint64(len(decoded)) != uint64(headerSize)+ciphertextLength {
		return nil, errors.New("invalid sealed-box wire")
	}
	return decoded, nil
}

func digest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func digestBytes(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

func randomID(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func randomSecret(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func notFound(w http.ResponseWriter) { writeError(w, http.StatusNotFound, "not_found") }

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		// Deliberately excludes headers, query strings, and request/response bodies.
		s.logger.Printf("method=%s path=%s status=%d duration_ms=%d", r.Method, routeLabel(r.URL.Path), recorder.status, s.now().Sub(start).Milliseconds())
	})
}

func routeLabel(path string) string {
	switch {
	case path == "/healthz":
		return "/healthz"
	case path == "/v1/accounts":
		return "/v1/accounts"
	case strings.HasPrefix(path, "/v1/accounts/"):
		return "/v1/accounts/:id/recover"
	case path == "/v1/pairings":
		return "/v1/pairings"
	case strings.HasPrefix(path, "/v1/pairings/") && strings.HasSuffix(path, "/approve"):
		return "/v1/pairings/:id/approve"
	case strings.HasPrefix(path, "/v1/pairings/") && strings.HasSuffix(path, "/claim"):
		return "/v1/pairings/:id/claim"
	case strings.HasPrefix(path, "/v1/pairings/"):
		return "/v1/pairings/:id"
	case path == "/v1/sync":
		return "/v1/sync"
	case path == "/v1/devices":
		return "/v1/devices"
	case strings.HasPrefix(path, "/v1/devices/"):
		return "/v1/devices/:id"
	case path == "/v1/keyring":
		return "/v1/keyring"
	default:
		return "unmatched"
	}
}

func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && !s.limiter.allow(clientIP(r), s.now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *ipLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastSweep.IsZero() || now.Before(l.lastSweep) || now.Sub(l.lastSweep) >= l.window {
		for candidate, entry := range l.entries {
			if now.Before(entry.start) || now.Sub(entry.start) >= l.window {
				delete(l.entries, candidate)
			}
		}
		l.lastSweep = now
	}
	entry := l.entries[ip]
	if entry.start.IsZero() || now.Before(entry.start) || now.Sub(entry.start) >= l.window {
		l.entries[ip] = rateEntry{start: now, count: 1}
		return true
	}
	entry.count++
	l.entries[ip] = entry
	return entry.count <= l.limit
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
