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
	protocolVersion  = 1
	paddingBucket    = 512
	maxBodyBytes     = 1 << 20
	maxCiphertext    = 524816    // protocol.MaxEnvelopeCiphertext (512 KiB canonical CBOR payload)
	maxSealedBoxWire = 256 << 10 // protocol.MaxSealedBoxWireSize
	maxUploadBatch   = 256
	defaultSyncLimit = 256
	maximumSyncLimit = 256
	maxDownloadBytes = maxCiphertext
	// The first production slice is deliberately the Mac plus R0W.  Keep the
	// active trust roster bounded until signed roster-chain propagation exists.
	maxActiveDevices     = 2
	pairingLifetime      = 10 * time.Minute
	pairingClaimLifetime = 24 * time.Hour
	// Provisioning is crash-resumable from an OS-protected local journal. A
	// seven-day remote window avoids stranding that journal during an outage;
	// normal account APIs remain blocked until the account is explicitly sealed.
	provisioningLife  = 7 * 24 * time.Hour
	rateWindow        = time.Minute
	requestsPerWindow = 240
)

var canonicalCBOR cbor.EncMode

// Recovery remains protocol-reserved but is not exposed in the two-device
// preview: its recovery package does not yet carry the signed two-peer roster.
// This is a variable (rather than a dead-code build flag) so the retained
// decoder remains compiled and fuzzable while production stays fail-closed.
var twoDeviceRecoveryEnabled = false
var twoDeviceRevocationEnabled = false

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
	db                 *sql.DB
	logger             *log.Logger
	now                func() time.Time
	limiter            *ipLimiter
	authLimiter        *ipLimiter
	handler            http.Handler
	pairingLifecycleMu sync.Mutex
}

type deviceIdentity struct {
	ID            string
	AccountID     string
	Ed25519Key    ed25519.PublicKey
	X25519Key     []byte
	CreatedUnix   int64
	AccountSealed bool
}

type contextKey string

const (
	identityKey     contextKey = "yunpin-device"
	userIdentityKey contextKey = "yunpin-user"
)

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
	// Immediate write transactions take SQLite's reserved lock at BeginTx,
	// avoiding a deferred read-to-write upgrade race when two relay processes
	// concurrently cancel the same pairing tuple.
	dsn += separator + "_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
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
		authLimiter: &ipLimiter{
			entries: make(map[string]rateEntry),
			limit:   maxAuthAttempts,
			window:  10 * time.Minute,
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
	case path == "/v1/auth/register" && r.Method == http.MethodPost:
		s.registerUser(w, r)
	case path == "/v1/auth/login" && r.Method == http.MethodPost:
		s.loginUser(w, r)
	case path == "/v1/auth/logout" && r.Method == http.MethodPost:
		s.logoutUser(w, r)
	case path == "/v1/accounts" && r.Method == http.MethodPost:
		s.requireUserAuth(s.createAccount)(w, r)
	case strings.HasPrefix(path, "/v1/accounts/") && strings.HasSuffix(path, "/claim") && r.Method == http.MethodPost:
		parts := strings.Split(path, "/")
		if len(parts) != 5 {
			notFound(w)
			return
		}
		s.requireUserAuth(func(w http.ResponseWriter, r *http.Request) { s.claimAccount(w, r, parts[3]) })(w, r)
	case strings.HasPrefix(path, "/v1/accounts/") && r.Method == http.MethodDelete:
		parts := strings.Split(path, "/")
		if len(parts) != 4 {
			notFound(w)
			return
		}
		s.rollbackAccount(w, r, parts[3])
	case strings.HasPrefix(path, "/v1/accounts/") && strings.HasSuffix(path, "/seal") && r.Method == http.MethodPost:
		parts := strings.Split(path, "/")
		if len(parts) != 5 {
			notFound(w)
			return
		}
		s.requireProvisioningAuth(func(w http.ResponseWriter, r *http.Request) { s.sealAccount(w, r, parts[3]) })(w, r)
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
	case path == "/v1/devices/current" && r.Method == http.MethodDelete:
		s.rollbackCurrentDevice(w, r)
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
		s.requireProvisioningAuth(s.putKeyring)(w, r)
	default:
		notFound(w)
	}
}

// rollbackAccount removes only a newly provisioned, otherwise-unused account.
// It exists so the first desktop client can undo the remote write if its local
// Keychain/DPAPI or encrypted-SQLite commit fails. The short-lived dedicated
// rollback capability, rather than the long-term device token, authorizes the
// single conditional
// DELETE keeps the safety checks and the cascade in one SQLite statement: the
// authenticated device must be the account's only device, and no pairing or
// vocabulary envelope may ever have been created.  Recovery keyrings are
// allowed because provisioning stores epoch one before committing locally.
func (s *Server) rollbackAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	if !validID(accountID) {
		writeError(w, http.StatusNotFound, "account_not_found")
		return
	}
	rollbackToken, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "rollback_capability_required")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	var tombstone int
	err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM account_rollback_tombstones
		WHERE account_id = ? AND rollback_hash = ?`, accountID, digest(rollbackToken)).Scan(&tombstone)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if tombstone == 1 {
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	result, err := tx.ExecContext(r.Context(), `DELETE FROM accounts
		WHERE id = ?
		  AND provisioning_sealed_at IS NULL
		  AND provisioning_expires_at > ?
		  AND provisioning_rollback_hash = ?
		  AND (SELECT COUNT(*) FROM devices WHERE account_id = ?) = 1
		  AND EXISTS (SELECT 1 FROM devices WHERE account_id = ? AND revoked_at IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM pairings WHERE account_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM envelopes WHERE account_id = ?)`,
		accountID, s.now().UnixMilli(), digest(rollbackToken), accountID, accountID, accountID, accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusConflict, "account_rollback_not_safe")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO account_rollback_tombstones(
		account_id, rollback_hash) VALUES(?, ?)`, accountID, digest(rollbackToken)); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sealAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	if !validID(accountID) {
		writeError(w, http.StatusNotFound, "account_not_found")
		return
	}
	identity := mustIdentity(r)
	if identity.AccountID != accountID {
		writeError(w, http.StatusNotFound, "account_not_found")
		return
	}
	result, err := s.db.ExecContext(r.Context(), `UPDATE accounts
		SET provisioning_sealed_at = COALESCE(provisioning_sealed_at, ?),
		    provisioning_rollback_hash = NULL, provisioning_expires_at = NULL
		WHERE id = ? AND EXISTS (SELECT 1 FROM keyrings WHERE account_id = ? AND epoch = 1)`,
		s.now().UnixMilli(), accountID, accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusConflict, "account_not_ready")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rollbackCurrentDevice consumes the joining client's dedicated rollback
// capability. Before claim there is no device bearer to authenticate, so the
// exact account/device/pairing tuple plus the capability hash are the complete
// authorization. Joined and approved reservations are removed directly;
// claimed reservations additionally remove the still-quarantined device. A
// durable ready acknowledgement is a one-way boundary and is never rolled
// back. Every successful path leaves a hash-only tombstone so response-loss
// retries are idempotent and neither the pairing nor device identity can be
// resurrected.
func (s *Server) rollbackCurrentDevice(w http.ResponseWriter, r *http.Request) {
	rollbackToken, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "device_rollback_capability_required")
		return
	}
	if _, err := decodeCanonicalSecret(rollbackToken, 32); err != nil {
		writeError(w, http.StatusUnauthorized, "device_rollback_capability_required")
		return
	}
	accountID := r.URL.Query().Get("account_id")
	deviceID := r.URL.Query().Get("device_id")
	pairingID := r.URL.Query().Get("pairing_id")
	if !validID(accountID) || !validID(deviceID) || !validID(pairingID) {
		writeError(w, http.StatusBadRequest, "invalid_device_rollback_identity")
		return
	}
	s.pairingLifecycleMu.Lock()
	defer s.pairingLifecycleMu.Unlock()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	rollbackHash := digest(rollbackToken)
	tombstoneFound, tombstoneMatches, err := deviceRollbackTombstoneStatus(
		r.Context(), tx, accountID, deviceID, pairingID, rollbackHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if tombstoneFound && !tombstoneMatches {
		writeError(w, http.StatusConflict, "device_rollback_not_safe")
		return
	}
	var storedAccountID, storedDeviceID, state string
	var storedRollbackHash []byte
	var readyAt, finalizedAt sql.NullInt64
	err = tx.QueryRowContext(r.Context(), `SELECT account_id, COALESCE(new_device_id, ''), state,
		rollback_hash, ready_at, finalized_at FROM pairings WHERE id = ?`, pairingID).
		Scan(&storedAccountID, &storedDeviceID, &state, &storedRollbackHash, &readyAt, &finalizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if tombstoneFound && tombstoneMatches {
			var resurrectedDevice int
			if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM devices
				WHERE account_id = ? AND id = ?`, accountID, deviceID).Scan(&resurrectedDevice); err != nil {
				writeError(w, http.StatusInternalServerError, "database_error")
				return
			}
			if resurrectedDevice != 0 {
				writeError(w, http.StatusConflict, "device_rollback_not_safe")
				return
			}
			if err := tx.Commit(); err != nil {
				writeError(w, http.StatusInternalServerError, "database_error")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusConflict, "device_rollback_not_safe")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if storedAccountID != accountID || storedDeviceID != deviceID ||
		!constantTimeEqual(storedRollbackHash, rollbackHash) {
		writeError(w, http.StatusConflict, "device_rollback_not_safe")
		return
	}
	if readyAt.Valid || finalizedAt.Valid {
		writeError(w, http.StatusConflict, "device_rollback_after_ready")
		return
	}

	switch state {
	case "joined", "approved":
		var existingDevice int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM devices WHERE id = ?`, deviceID).
			Scan(&existingDevice); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if existingDevice != 0 {
			writeError(w, http.StatusConflict, "device_rollback_not_safe")
			return
		}
	case "claimed":
		var activePeerCount, claimedMappings, unsafeWrites int
		if err := tx.QueryRowContext(r.Context(), `SELECT
			(SELECT COUNT(*) FROM devices WHERE account_id = ? AND id <> ? AND revoked_at IS NULL),
			(SELECT COUNT(*) FROM pairings WHERE account_id = ? AND new_device_id = ? AND state = 'claimed'),
			(SELECT COUNT(*) FROM envelopes WHERE account_id = ? AND device_id = ?) +
			(SELECT COUNT(*) FROM keyrings WHERE account_id = ? AND writer_device_id = ?) +
			(SELECT COUNT(*) FROM pairings WHERE account_id = ? AND creator_device_id = ?)`,
			accountID, deviceID, accountID, deviceID,
			accountID, deviceID, accountID, deviceID, accountID, deviceID).
			Scan(&activePeerCount, &claimedMappings, &unsafeWrites); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if activePeerCount == 0 || claimedMappings != 1 || unsafeWrites != 0 {
			writeError(w, http.StatusConflict, "device_rollback_not_safe")
			return
		}
	default:
		// A created invitation has no authenticated joining tuple. Unknown or
		// absent state must never turn an arbitrary capability into success.
		writeError(w, http.StatusConflict, "device_rollback_not_safe")
		return
	}

	if !tombstoneFound {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO device_rollback_tombstones(
			account_id, device_id, pairing_id, rollback_hash) VALUES(?, ?, ?, ?)`,
			accountID, deviceID, pairingID, rollbackHash); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
	}
	result, err := tx.ExecContext(r.Context(), `DELETE FROM pairings
		WHERE id = ? AND account_id = ? AND new_device_id = ? AND state = ? AND rollback_hash = ?
		  AND ready_at IS NULL AND finalized_at IS NULL`, pairingID, accountID, deviceID, state, rollbackHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusConflict, "device_rollback_not_safe")
		return
	}
	if state == "claimed" {
		result, err = tx.ExecContext(r.Context(), `DELETE FROM devices
			WHERE id = ? AND account_id = ? AND revoked_at IS NULL`, deviceID, accountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			writeError(w, http.StatusConflict, "device_rollback_not_safe")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deviceRollbackTombstoneStatus authenticates the tombstone hash in constant
// time after selecting by the public tuple. Selecting with the hash in SQL
// would make a conflicting capability observably different from an absent
// tuple and would not provide a constant-time secret comparison.
func deviceRollbackTombstoneStatus(ctx context.Context, tx *sql.Tx, accountID, deviceID, pairingID string, expectedHash []byte) (bool, bool, error) {
	var storedHash []byte
	err := tx.QueryRowContext(ctx, `SELECT rollback_hash FROM device_rollback_tombstones
		WHERE account_id = ? AND device_id = ? AND pairing_id = ?`, accountID, deviceID, pairingID).
		Scan(&storedHash)
	switch {
	case err == nil:
		return true, constantTimeEqual(storedHash, expectedHash), nil
	case errors.Is(err, sql.ErrNoRows):
		return false, false, nil
	default:
		return false, false, err
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
	if len(parts) == 5 && parts[4] == "ready" && r.Method == http.MethodPost {
		s.readyPairing(w, r, parts[3])
		return
	}
	if len(parts) == 5 && parts[4] == "finalize" && r.Method == http.MethodPost {
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) { s.finalizePairing(w, r, parts[3]) })(w, r)
		return
	}
	if len(parts) == 4 && r.Method == http.MethodDelete {
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) { s.cancelPairing(w, r, parts[3]) })(w, r)
		return
	}
	notFound(w)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.cleanupExpiredProvisioning(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	if err := s.db.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) cleanupExpiredProvisioning(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now()
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO account_rollback_tombstones(
		account_id, rollback_hash)
		SELECT id, provisioning_rollback_hash FROM accounts
		WHERE provisioning_sealed_at IS NULL
		  AND provisioning_expires_at <= ?
		  AND provisioning_rollback_hash IS NOT NULL
		  AND (SELECT COUNT(*) FROM devices WHERE account_id = accounts.id) = 1
		  AND NOT EXISTS (SELECT 1 FROM pairings WHERE account_id = accounts.id)
		  AND NOT EXISTS (SELECT 1 FROM envelopes WHERE account_id = accounts.id)`,
		now.UnixMilli())
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM accounts
		WHERE provisioning_sealed_at IS NULL
		  AND provisioning_expires_at <= ?
		  AND (SELECT COUNT(*) FROM devices WHERE account_id = accounts.id) = 1
		  AND NOT EXISTS (SELECT 1 FROM pairings WHERE account_id = accounts.id)
		  AND NOT EXISTS (SELECT 1 FROM envelopes WHERE account_id = accounts.id)`, now.UnixMilli())
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

type deviceRegistration struct {
	DeviceNameCiphertext string `json:"device_name_ciphertext"`
	Ed25519PublicKey     string `json:"ed25519_public_key"`
	X25519PublicKey      string `json:"x25519_public_key"`
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.cleanupExpiredProvisioning(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	var input struct {
		AccountID              string `json:"account_id"`
		DeviceID               string `json:"device_id"`
		DeviceToken            string `json:"device_token"`
		RollbackToken          string `json:"rollback_token"`
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
	deviceToken, err := validateProvisioningIdentity(input.AccountID, input.DeviceID, input.DeviceToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_provisioning_identity")
		return
	}
	if _, err := decodeCanonicalSecret(input.RollbackToken, 32); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_rollback_capability")
		return
	}
	user := mustUserIdentity(r)
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	var rollbackTombstone int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM account_rollback_tombstones
		WHERE account_id = ?`, input.AccountID).Scan(&rollbackTombstone); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if rollbackTombstone != 0 {
		writeError(w, http.StatusConflict, "provisioning_identity_retired")
		return
	}
	accountHash := digestBytes(recoveryAuthentication)
	var existingAccountHash []byte
	err = tx.QueryRowContext(r.Context(), "SELECT recovery_authentication_hash FROM accounts WHERE id = ?", input.AccountID).Scan(&existingAccountHash)
	accountCreated := errors.Is(err, sql.ErrNoRows)
	if accountCreated {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO accounts(
			id, recovery_authentication_hash, created_at, provisioning_rollback_hash, provisioning_expires_at, user_id)
			VALUES(?, ?, ?, ?, ?, ?)`, input.AccountID, accountHash, now, digest(input.RollbackToken), now+provisioningLife.Milliseconds(), user.ID)
	} else if err == nil && !constantTimeEqual(existingAccountHash, accountHash) {
		writeError(w, http.StatusConflict, "provisioning_identity_conflict")
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if !accountCreated {
		var rollbackHash []byte
		var sealed sql.NullInt64
		var expires sql.NullInt64
		var accountUser sql.NullString
		if err := tx.QueryRowContext(r.Context(), `SELECT provisioning_rollback_hash,
			provisioning_expires_at, provisioning_sealed_at, user_id FROM accounts WHERE id = ?`, input.AccountID).
			Scan(&rollbackHash, &expires, &sealed, &accountUser); err != nil || sealed.Valid || !expires.Valid ||
			expires.Int64 <= now || !constantTimeEqual(rollbackHash, digest(input.RollbackToken)) {
			writeError(w, http.StatusConflict, "provisioning_identity_conflict")
			return
		}
		if !accountUser.Valid || accountUser.String != user.ID {
			writeError(w, http.StatusConflict, "provisioning_identity_conflict")
			return
		}
	}
	var existingAccountID string
	var existingName, existingTokenHash, existingEdKey, existingXKey []byte
	var revoked sql.NullInt64
	err = tx.QueryRowContext(r.Context(), `SELECT account_id, name_ciphertext, token_hash,
		ed25519_public_key, x25519_public_key, revoked_at FROM devices WHERE id = ?`, input.DeviceID).
		Scan(&existingAccountID, &existingName, &existingTokenHash, &existingEdKey, &existingXKey, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		if !accountCreated {
			writeError(w, http.StatusConflict, "provisioning_identity_conflict")
			return
		}
		_, err = tx.ExecContext(r.Context(), `INSERT INTO devices(id, account_id, name_ciphertext, token_hash, ed25519_public_key, x25519_public_key, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)`, input.DeviceID, input.AccountID, nameCiphertext, digest(input.DeviceToken), edKey, xKey, now)
	} else if err == nil {
		if existingAccountID != input.AccountID || revoked.Valid || !constantTimeEqual(existingName, nameCiphertext) ||
			!constantTimeEqual(existingTokenHash, digest(input.DeviceToken)) || !constantTimeEqual(existingEdKey, edKey) ||
			!constantTimeEqual(existingXKey, xKey) {
			writeError(w, http.StatusConflict, "provisioning_identity_conflict")
			return
		}
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"account_id": input.AccountID, "device_id": input.DeviceID, "device_token": deviceToken,
	})
}

func (s *Server) recoverAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	if !validID(accountID) {
		writeError(w, http.StatusNotFound, "account_not_found")
		return
	}
	// Recovery cannot safely establish the second peer until the recovery box
	// carries and verifies the same signed two-device roster as pairing v2.
	// Keeping the endpoint fail-closed prevents a relay roster from becoming a
	// trust root. The parser below remains for the next protocol revision.
	if !twoDeviceRecoveryEnabled {
		writeError(w, http.StatusConflict, "recovery_not_available_in_two_device_preview")
		return
	}

	var input struct {
		DeviceID               string `json:"device_id"`
		DeviceToken            string `json:"device_token"`
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
	if err := s.db.QueryRowContext(r.Context(), `SELECT recovery_authentication_hash FROM accounts
		WHERE id = ? AND provisioning_sealed_at IS NOT NULL`, accountID).Scan(&expected); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_recovery_authentication")
		return
	}
	got := digestBytes(recoveryAuthentication)
	if len(expected) != len(got) || subtle.ConstantTimeCompare(expected, got) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_recovery_authentication")
		return
	}
	deviceToken, err := validateProvisioningIdentity(accountID, input.DeviceID, input.DeviceToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_provisioning_identity")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	var existingAccountID string
	var existingName, existingTokenHash, existingEdKey, existingXKey []byte
	var revoked sql.NullInt64
	err = tx.QueryRowContext(r.Context(), `SELECT account_id, name_ciphertext, token_hash,
		ed25519_public_key, x25519_public_key, revoked_at FROM devices WHERE id = ?`, input.DeviceID).
		Scan(&existingAccountID, &existingName, &existingTokenHash, &existingEdKey, &existingXKey, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		var result sql.Result
		result, err = tx.ExecContext(r.Context(), `INSERT INTO devices(id, account_id, name_ciphertext, token_hash,
			ed25519_public_key, x25519_public_key, created_at)
			SELECT ?, ?, ?, ?, ?, ?, ?
			WHERE (SELECT COUNT(*) FROM devices WHERE account_id = ? AND revoked_at IS NULL) < ?`,
			input.DeviceID, accountID, nameCiphertext, digest(input.DeviceToken), edKey, xKey,
			s.now().UnixMilli(), accountID, maxActiveDevices)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected != 1 {
				writeError(w, http.StatusConflict, "device_limit_reached")
				return
			}
		}
	} else if err == nil {
		if existingAccountID != accountID || revoked.Valid || !constantTimeEqual(existingName, nameCiphertext) ||
			!constantTimeEqual(existingTokenHash, digest(input.DeviceToken)) || !constantTimeEqual(existingEdKey, edKey) ||
			!constantTimeEqual(existingXKey, xKey) {
			writeError(w, http.StatusConflict, "provisioning_identity_conflict")
			return
		}
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"account_id": accountID, "device_id": input.DeviceID, "device_token": deviceToken})
}

func (s *Server) createPairing(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r)
	var input struct {
		PairingID       string `json:"pairing_id"`
		PairingVerifier string `json:"pairing_verifier"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	verifier, err := decodeCanonicalSecret(input.PairingVerifier, 32)
	if !validID(input.PairingID) || input.PairingID == strings.Repeat("0", 32) || err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pairing_invitation")
		return
	}
	s.pairingLifecycleMu.Lock()
	defer s.pairingLifecycleMu.Unlock()
	now := s.now()
	expiresAt := now.Add(pairingLifetime).UnixMilli()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	// Expired joined/approved reservations already contain an authenticated
	// joining tuple. Retire that tuple before freeing the live reservation;
	// only an untouched created invitation may disappear without a tombstone.
	var expiredPairingID, expiredDeviceID string
	var expiredRollbackHash []byte
	err = tx.QueryRowContext(r.Context(), `SELECT id, COALESCE(new_device_id, ''), rollback_hash
		FROM pairings WHERE account_id = ? AND (
			(state = 'joined' AND expires_at <= ?) OR
			(state = 'approved' AND claim_expires_at IS NOT NULL AND claim_expires_at <= ?))
		LIMIT 1`, identity.AccountID, now.UnixMilli(), now.UnixMilli()).
		Scan(&expiredPairingID, &expiredDeviceID, &expiredRollbackHash)
	if err == nil {
		if !validID(expiredDeviceID) || len(expiredRollbackHash) != sha256.Size {
			writeError(w, http.StatusConflict, "pairing_invitation_conflict")
			return
		}
		var existingDevice int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM devices WHERE id = ?`, expiredDeviceID).
			Scan(&existingDevice); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if existingDevice != 0 {
			writeError(w, http.StatusConflict, "pairing_invitation_conflict")
			return
		}
		found, matches, tombstoneErr := deviceRollbackTombstoneStatus(r.Context(), tx,
			identity.AccountID, expiredDeviceID, expiredPairingID, expiredRollbackHash)
		if tombstoneErr != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if found && !matches {
			writeError(w, http.StatusConflict, "pairing_invitation_conflict")
			return
		}
		if !found {
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO device_rollback_tombstones(
				account_id, device_id, pairing_id, rollback_hash) VALUES(?, ?, ?, ?)`,
				identity.AccountID, expiredDeviceID, expiredPairingID, expiredRollbackHash); err != nil {
				writeError(w, http.StatusInternalServerError, "database_error")
				return
			}
		}
		result, err := tx.ExecContext(r.Context(), `DELETE FROM pairings
			WHERE id = ? AND account_id = ? AND new_device_id = ? AND rollback_hash = ?
			  AND state IN ('joined', 'approved') AND ready_at IS NULL AND finalized_at IS NULL`,
			expiredPairingID, identity.AccountID, expiredDeviceID, expiredRollbackHash)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			writeError(w, http.StatusConflict, "pairing_invitation_conflict")
			return
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM pairings
		WHERE account_id = ? AND state = 'created' AND expires_at <= ?`,
		identity.AccountID, now.UnixMilli()); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	var retiredPairingID int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM device_rollback_tombstones
		WHERE pairing_id = ?`, input.PairingID).Scan(&retiredPairingID); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if retiredPairingID != 0 {
		writeError(w, http.StatusConflict, "pairing_invitation_conflict")
		return
	}
	result, err := tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO pairings(
		id, account_id, creator_device_id, secret_hash, state, expires_at, created_at)
		SELECT ?, ?, ?, ?, 'created', ?, ?
		WHERE (SELECT COUNT(*) FROM devices WHERE account_id = ? AND revoked_at IS NULL) = 1
		AND NOT EXISTS (SELECT 1 FROM pairings WHERE account_id = ? AND state IN ('created', 'joined', 'approved'))`,
		input.PairingID, identity.AccountID, identity.ID, verifier, expiresAt, now.UnixMilli(),
		identity.AccountID, identity.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var accountID, creatorID string
		var existingVerifier []byte
		var existingExpiry int64
		var existingState string
		if err := tx.QueryRowContext(r.Context(), `SELECT account_id, creator_device_id, secret_hash, expires_at, state
			FROM pairings WHERE id = ?`, input.PairingID).Scan(&accountID, &creatorID, &existingVerifier, &existingExpiry, &existingState); err != nil ||
			accountID != identity.AccountID || creatorID != identity.ID || !constantTimeEqual(existingVerifier, verifier) ||
			existingExpiry <= now.UnixMilli() || (existingState != "created" && existingState != "joined" && existingState != "approved") {
			writeError(w, http.StatusConflict, "pairing_invitation_conflict")
			return
		}
		expiresAt = existingExpiry
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"pairing_id": input.PairingID,
		"expires_at": time.UnixMilli(expiresAt).UTC(),
	})
}

func (s *Server) joinPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	var input struct {
		PairingVerifier string `json:"pairing_verifier"`
		DeviceID        string `json:"device_id"`
		JoinProof       string `json:"join_proof"`
		RollbackToken   string `json:"rollback_token"`
		deviceRegistration
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	verifier, verifierErr := decodeCanonicalSecret(input.PairingVerifier, 32)
	joinProof, proofErr := decodeCanonicalSecret(input.JoinProof, 32)
	_, rollbackErr := decodeCanonicalSecret(input.RollbackToken, 32)
	nameCiphertext, edKey, xKey, err := validateRegistration(input.deviceRegistration)
	if !validID(pairingID) || !validID(input.DeviceID) || input.DeviceID == strings.Repeat("0", 32) ||
		verifierErr != nil || proofErr != nil || rollbackErr != nil || err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pairing_join")
		return
	}
	s.pairingLifecycleMu.Lock()
	defer s.pairingLifecycleMu.Unlock()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	var accountID, state, existingDeviceID string
	var secretHash, existingName, existingEdKey, existingXKey, existingProof, existingRollback []byte
	var expiresAt int64
	err = tx.QueryRowContext(r.Context(), `SELECT account_id, state, secret_hash, COALESCE(new_device_id, ''), pending_name_ciphertext,
		pending_ed25519_public_key, pending_x25519_public_key, pending_join_proof, rollback_hash, expires_at FROM pairings WHERE id = ?`, pairingID).
		Scan(&accountID, &state, &secretHash, &existingDeviceID, &existingName, &existingEdKey, &existingXKey, &existingProof, &existingRollback, &expiresAt)
	if err != nil || (state == "created" && expiresAt <= s.now().UnixMilli()) || !constantTimeEqual(secretHash, verifier) {
		writeError(w, http.StatusUnauthorized, "invalid_or_expired_pairing")
		return
	}
	var retiredDeviceID int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM device_rollback_tombstones
		WHERE account_id = ? AND device_id = ?`, accountID, input.DeviceID).Scan(&retiredDeviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if retiredDeviceID != 0 {
		writeError(w, http.StatusConflict, "pairing_join_conflict")
		return
	}
	if state == "created" {
		var result sql.Result
		result, err = tx.ExecContext(r.Context(), `UPDATE pairings SET state = 'joined', new_device_id = ?,
			pending_name_ciphertext = ?, pending_ed25519_public_key = ?, pending_x25519_public_key = ?,
			pending_join_proof = ?, rollback_hash = ? WHERE id = ? AND state = 'created'
			AND (SELECT COUNT(*) FROM devices WHERE account_id = pairings.account_id AND revoked_at IS NULL) = 1
			AND NOT EXISTS (SELECT 1 FROM devices WHERE id = ?)`,
			input.DeviceID, nameCiphertext, edKey, xKey, joinProof, digest(input.RollbackToken), pairingID, input.DeviceID)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected != 1 {
				writeError(w, http.StatusConflict, "device_limit_reached")
				return
			}
		}
	} else if state == "joined" || state == "approved" || state == "claimed" {
		if existingDeviceID != input.DeviceID || !constantTimeEqual(existingName, nameCiphertext) ||
			!constantTimeEqual(existingEdKey, edKey) || !constantTimeEqual(existingXKey, xKey) ||
			!constantTimeEqual(existingProof, joinProof) || !constantTimeEqual(existingRollback, digest(input.RollbackToken)) {
			writeError(w, http.StatusConflict, "pairing_join_conflict")
			return
		}
	} else {
		writeError(w, http.StatusConflict, "pairing_not_joinable")
		return
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "joined"})
}

func (s *Server) getPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	identity := mustIdentity(r)
	var state, newDeviceID string
	var nameCiphertext, ed25519Key, x25519Key, joinProof []byte
	var expiresAt, claimExpiresAt, readyExpiresAt int64
	var readyAt, finalizedAt sql.NullInt64
	err := s.db.QueryRowContext(r.Context(), `SELECT state, COALESCE(new_device_id, ''), pending_name_ciphertext, pending_ed25519_public_key,
		pending_x25519_public_key, pending_join_proof, expires_at, COALESCE(claim_expires_at, 0),
		ready_at, COALESCE(ready_expires_at, 0), finalized_at FROM pairings
		WHERE id = ? AND account_id = ? AND creator_device_id = ?`, pairingID, identity.AccountID, identity.ID).
		Scan(&state, &newDeviceID, &nameCiphertext, &ed25519Key, &x25519Key, &joinProof,
			&expiresAt, &claimExpiresAt, &readyAt, &readyExpiresAt, &finalizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "pairing_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	visibleState := state
	if state == "claimed" {
		switch {
		case finalizedAt.Valid:
			visibleState = "finalized"
		case readyAt.Valid:
			visibleState = "ready"
		}
	}
	nowMillis := s.now().UnixMilli()
	stageExpired := false
	switch visibleState {
	case "created", "joined":
		stageExpired = expiresAt <= nowMillis
	case "approved":
		stageExpired = claimExpiresAt <= nowMillis
	case "claimed":
		stageExpired = readyExpiresAt <= nowMillis
	case "ready", "finalized":
		// A durable ready acknowledgement and finalization are terminal progress,
		// not continuations of any earlier invitation/claim deadline.
		stageExpired = false
	}
	response := map[string]any{
		"pairing_id": pairingID,
		"state":      visibleState,
		"expires_at": time.UnixMilli(expiresAt).UTC(),
		"expired":    stageExpired,
	}
	if claimExpiresAt != 0 {
		response["claim_expires_at"] = time.UnixMilli(claimExpiresAt).UTC()
	}
	if readyExpiresAt != 0 {
		response["ready_expires_at"] = time.UnixMilli(readyExpiresAt).UTC()
	}
	if len(nameCiphertext) != 0 {
		response["device_id"] = newDeviceID
		response["device_name_ciphertext"] = base64.RawURLEncoding.EncodeToString(nameCiphertext)
		response["ed25519_public_key"] = base64.RawURLEncoding.EncodeToString(ed25519Key)
		response["x25519_public_key"] = base64.RawURLEncoding.EncodeToString(x25519Key)
		response["join_proof"] = base64.RawURLEncoding.EncodeToString(joinProof)
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
	s.pairingLifecycleMu.Lock()
	defer s.pairingLifecycleMu.Unlock()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	var state, newDeviceID string
	var existingKeyring []byte
	var expiresAt, claimExpiresAt int64
	err = tx.QueryRowContext(r.Context(), `SELECT state, COALESCE(new_device_id, ''), encrypted_keyring, expires_at,
		COALESCE(claim_expires_at, 0) FROM pairings
		WHERE id = ? AND account_id = ? AND creator_device_id = ?`, pairingID, identity.AccountID, identity.ID).
		Scan(&state, &newDeviceID, &existingKeyring, &expiresAt, &claimExpiresAt)
	if err != nil || (state == "joined" && expiresAt <= s.now().UnixMilli()) || !validID(newDeviceID) {
		writeError(w, http.StatusConflict, "pairing_not_ready")
		return
	}
	if state == "joined" {
		claimExpiresAt = s.now().Add(pairingClaimLifetime).UnixMilli()
		var result sql.Result
		result, err = tx.ExecContext(r.Context(), `UPDATE pairings SET state = 'approved', encrypted_keyring = ?, claim_expires_at = ?
			WHERE id = ? AND state = 'joined'
			AND (SELECT COUNT(*) FROM devices WHERE account_id = pairings.account_id AND revoked_at IS NULL) = 1`,
			keyring, claimExpiresAt, pairingID)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected != 1 {
				writeError(w, http.StatusConflict, "device_limit_reached")
				return
			}
		}
	} else if state == "approved" || state == "claimed" {
		if !constantTimeEqual(existingKeyring, keyring) {
			writeError(w, http.StatusConflict, "pairing_approval_conflict")
			return
		}
	} else {
		writeError(w, http.StatusConflict, "pairing_not_ready")
		return
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "approved"})
}

func (s *Server) claimPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	var input struct {
		PairingVerifier string `json:"pairing_verifier"`
		DeviceToken     string `json:"device_token"`
		ClaimProof      string `json:"claim_proof"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	s.pairingLifecycleMu.Lock()
	defer s.pairingLifecycleMu.Unlock()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	verifier, verifierErr := decodeCanonicalSecret(input.PairingVerifier, 32)
	claimProof, proofErr := decodeCanonicalSecret(input.ClaimProof, ed25519.SignatureSize)
	if _, err := decodeCanonicalSecret(input.DeviceToken, 32); err != nil || verifierErr != nil || proofErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_device_token")
		return
	}
	var accountID, deviceID, creatorDeviceID, state string
	var nameCiphertext, expected, edKey, xKey, creatorEdKey, creatorXKey, encryptedKeyring []byte
	var expiresAt, claimExpiresAt int64
	err = tx.QueryRowContext(r.Context(), `SELECT p.account_id, p.new_device_id, p.creator_device_id,
		p.pending_name_ciphertext, p.secret_hash, p.pending_ed25519_public_key, p.pending_x25519_public_key,
		d.ed25519_public_key, d.x25519_public_key, p.encrypted_keyring, p.expires_at,
		COALESCE(p.claim_expires_at, 0), p.state
		FROM pairings p JOIN devices d ON d.id = p.creator_device_id AND d.account_id = p.account_id
		WHERE p.id = ? AND p.state IN ('approved', 'claimed')`, pairingID).
		Scan(&accountID, &deviceID, &creatorDeviceID, &nameCiphertext, &expected, &edKey, &xKey,
			&creatorEdKey, &creatorXKey, &encryptedKeyring, &expiresAt, &claimExpiresAt, &state)
	if err != nil || !constantTimeEqual(expected, verifier) {
		writeError(w, http.StatusUnauthorized, "invalid_or_expired_pairing")
		return
	}
	if state == "approved" && (claimExpiresAt == 0 || claimExpiresAt <= s.now().UnixMilli()) {
		writeError(w, http.StatusUnauthorized, "invalid_or_expired_pairing")
		return
	}
	claimMessage, err := canonicalPairingClaimMessage(pairingID, accountID, creatorDeviceID, deviceID,
		creatorEdKey, edKey, creatorXKey, xKey, input.DeviceToken)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(edKey), claimMessage, claimProof) {
		writeError(w, http.StatusUnauthorized, "invalid_pairing_claim_proof")
		return
	}
	if state == "approved" {
		var result sql.Result
		result, err = tx.ExecContext(r.Context(), `INSERT INTO devices(id, account_id, name_ciphertext, token_hash,
			ed25519_public_key, x25519_public_key, created_at)
			SELECT ?, ?, ?, ?, ?, ?, ?
			WHERE (SELECT COUNT(*) FROM devices WHERE account_id = ? AND revoked_at IS NULL) = 1`,
			deviceID, accountID, nameCiphertext, digest(input.DeviceToken), edKey, xKey,
			s.now().UnixMilli(), accountID)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected != 1 {
				writeError(w, http.StatusConflict, "device_limit_reached")
				return
			}
			now := s.now()
			_, err = tx.ExecContext(r.Context(), `UPDATE pairings SET state = 'claimed', claimed_at = ?, ready_expires_at = ?
				WHERE id = ? AND state = 'approved'`, now.UnixMilli(), now.Add(pairingClaimLifetime).UnixMilli(), pairingID)
		}
	} else {
		var tokenHash []byte
		err = tx.QueryRowContext(r.Context(), `SELECT token_hash FROM devices
			WHERE id = ? AND account_id = ? AND revoked_at IS NULL`, deviceID, accountID).Scan(&tokenHash)
		if err == nil && !constantTimeEqual(tokenHash, digest(input.DeviceToken)) {
			writeError(w, http.StatusConflict, "pairing_claim_conflict")
			return
		}
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, http.StatusConflict, "pairing_already_claimed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"account_id": accountID, "device_id": deviceID, "device_token": input.DeviceToken,
		"encrypted_keyring": base64.RawURLEncoding.EncodeToString(encryptedKeyring),
	})
}

// readyPairing is the joining device's durable-local-commit acknowledgement.
// The ordinary device bearer remains blocked from every normal account API
// until the creator subsequently finalizes the signed roster.
func (s *Server) readyPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	if !validID(pairingID) {
		writeError(w, http.StatusNotFound, "pairing_not_found")
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	s.pairingLifecycleMu.Lock()
	defer s.pairingLifecycleMu.Unlock()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	var state string
	var readyAt, finalizedAt sql.NullInt64
	var readyExpiresAt int64
	err = tx.QueryRowContext(r.Context(), `SELECT p.state, p.ready_at, p.finalized_at,
		COALESCE(p.ready_expires_at, 0) FROM pairings p
		JOIN devices d ON d.id = p.new_device_id AND d.account_id = p.account_id
		WHERE p.id = ? AND p.state = 'claimed' AND d.revoked_at IS NULL AND d.token_hash = ?`,
		pairingID, digest(token)).Scan(&state, &readyAt, &finalizedAt, &readyExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_device_token")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if finalizedAt.Valid {
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"state": "finalized"})
		return
	}
	if !readyAt.Valid {
		if readyExpiresAt == 0 || readyExpiresAt <= s.now().UnixMilli() {
			writeError(w, http.StatusConflict, "pairing_ready_window_expired")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE pairings SET ready_at = ?
			WHERE id = ? AND state = 'claimed' AND ready_at IS NULL AND finalized_at IS NULL`,
			s.now().UnixMilli(), pairingID); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "ready"})
}

func (s *Server) finalizePairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	identity := mustIdentity(r)
	if !validID(pairingID) {
		writeError(w, http.StatusNotFound, "pairing_not_found")
		return
	}
	s.pairingLifecycleMu.Lock()
	defer s.pairingLifecycleMu.Unlock()
	result, err := s.db.ExecContext(r.Context(), `UPDATE pairings SET finalized_at = COALESCE(finalized_at, ?)
		WHERE id = ? AND account_id = ? AND creator_device_id = ? AND state = 'claimed' AND ready_at IS NOT NULL`,
		s.now().UnixMilli(), pairingID, identity.AccountID, identity.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusConflict, "pairing_not_ready_to_finalize")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "finalized"})
}

// cancelPairing keeps the creator's trust roster self-only. It is allowed
// before the joining device reports a durable local commit; after ready, only
// creator finalization or the joining rollback capability may progress state.
func (s *Server) cancelPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	identity := mustIdentity(r)
	if !validID(pairingID) {
		writeError(w, http.StatusNotFound, "pairing_not_found")
		return
	}
	s.pairingLifecycleMu.Lock()
	defer s.pairingLifecycleMu.Unlock()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	var state, deviceID string
	var rollbackHash []byte
	var joinMaterialBytes int
	var readyAt, finalizedAt sql.NullInt64
	err = tx.QueryRowContext(r.Context(), `SELECT state, COALESCE(new_device_id, ''), rollback_hash,
		COALESCE(length(pending_name_ciphertext), 0) + COALESCE(length(pending_ed25519_public_key), 0) +
		COALESCE(length(pending_x25519_public_key), 0) + COALESCE(length(pending_join_proof), 0) +
		COALESCE(length(encrypted_keyring), 0), ready_at, finalized_at
		FROM pairings WHERE id = ? AND account_id = ? AND creator_device_id = ?`,
		pairingID, identity.AccountID, identity.ID).
		Scan(&state, &deviceID, &rollbackHash, &joinMaterialBytes, &readyAt, &finalizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if readyAt.Valid || finalizedAt.Valid {
		writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
		return
	}
	needsTombstone := state == "joined" || state == "approved" || state == "claimed"
	switch state {
	case "created":
		if deviceID != "" || len(rollbackHash) != 0 || joinMaterialBytes != 0 {
			writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
			return
		}
	case "joined", "approved":
		if !validID(deviceID) || len(rollbackHash) != sha256.Size {
			writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
			return
		}
		var existingDevice int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM devices WHERE id = ?`, deviceID).
			Scan(&existingDevice); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if existingDevice != 0 {
			writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
			return
		}
	case "claimed":
		if !validID(deviceID) || len(rollbackHash) != sha256.Size {
			writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
			return
		}
		var unsafeWrites int
		if err := tx.QueryRowContext(r.Context(), `SELECT
			(SELECT COUNT(*) FROM envelopes WHERE account_id = ? AND device_id = ?) +
			(SELECT COUNT(*) FROM keyrings WHERE account_id = ? AND writer_device_id = ?) +
			(SELECT COUNT(*) FROM pairings WHERE account_id = ? AND creator_device_id = ?)`,
			identity.AccountID, deviceID, identity.AccountID, deviceID,
			identity.AccountID, deviceID).Scan(&unsafeWrites); err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if unsafeWrites != 0 {
			writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
			return
		}
	default:
		writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
		return
	}
	if needsTombstone {
		found, matches, tombstoneErr := deviceRollbackTombstoneStatus(r.Context(), tx,
			identity.AccountID, deviceID, pairingID, rollbackHash)
		if tombstoneErr != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if found && !matches {
			writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
			return
		}
		if !found {
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO device_rollback_tombstones(
				account_id, device_id, pairing_id, rollback_hash) VALUES(?, ?, ?, ?)`,
				identity.AccountID, deviceID, pairingID, rollbackHash); err != nil {
				writeError(w, http.StatusInternalServerError, "database_error")
				return
			}
		}
	}
	var result sql.Result
	if state == "created" {
		result, err = tx.ExecContext(r.Context(), `DELETE FROM pairings
			WHERE id = ? AND account_id = ? AND creator_device_id = ? AND state = 'created'
			  AND new_device_id IS NULL AND rollback_hash IS NULL AND ready_at IS NULL AND finalized_at IS NULL`,
			pairingID, identity.AccountID, identity.ID)
	} else {
		result, err = tx.ExecContext(r.Context(), `DELETE FROM pairings
			WHERE id = ? AND account_id = ? AND creator_device_id = ? AND state = ?
			  AND new_device_id = ? AND rollback_hash = ? AND ready_at IS NULL AND finalized_at IS NULL`,
			pairingID, identity.AccountID, identity.ID, state, deviceID, rollbackHash)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
		return
	}
	if state == "claimed" {
		result, err = tx.ExecContext(r.Context(), `DELETE FROM devices
			WHERE id = ? AND account_id = ? AND revoked_at IS NULL`, deviceID, identity.AccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database_error")
			return
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			writeError(w, http.StatusConflict, "pairing_cancel_not_safe")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

type pairingTranscript struct {
	PairingID               []byte `cbor:"1,keyasint"`
	AccountID               []byte `cbor:"2,keyasint"`
	CreatorDeviceID         []byte `cbor:"3,keyasint"`
	JoiningDeviceID         []byte `cbor:"4,keyasint"`
	CreatorEd25519PublicKey []byte `cbor:"5,keyasint"`
	JoiningEd25519PublicKey []byte `cbor:"6,keyasint"`
	CreatorX25519PublicKey  []byte `cbor:"7,keyasint"`
	JoiningX25519PublicKey  []byte `cbor:"8,keyasint"`
}

func canonicalPairingClaimMessage(pairingID, accountID, creatorDeviceID, joiningDeviceID string,
	creatorEdKey, joiningEdKey, creatorXKey, joiningXKey []byte, deviceToken string) ([]byte, error) {
	decodeID := func(value string) ([]byte, error) {
		if !validID(value) {
			return nil, errors.New("invalid pairing transcript identifier")
		}
		return hex.DecodeString(value)
	}
	pairingBytes, err := decodeID(pairingID)
	if err != nil {
		return nil, err
	}
	accountBytes, err := decodeID(accountID)
	if err != nil {
		return nil, err
	}
	creatorBytes, err := decodeID(creatorDeviceID)
	if err != nil {
		return nil, err
	}
	joiningBytes, err := decodeID(joiningDeviceID)
	if err != nil {
		return nil, err
	}
	if len(creatorEdKey) != ed25519.PublicKeySize || len(joiningEdKey) != ed25519.PublicKeySize ||
		len(creatorXKey) != 32 || len(joiningXKey) != 32 {
		return nil, errors.New("invalid pairing transcript key")
	}
	encoded, err := canonicalCBOR.Marshal(pairingTranscript{
		PairingID: pairingBytes, AccountID: accountBytes, CreatorDeviceID: creatorBytes, JoiningDeviceID: joiningBytes,
		CreatorEd25519PublicKey: creatorEdKey, JoiningEd25519PublicKey: joiningEdKey,
		CreatorX25519PublicKey: creatorXKey, JoiningX25519PublicKey: joiningXKey,
	})
	if err != nil {
		return nil, err
	}
	tokenHash := sha256.Sum256([]byte(deviceToken))
	message := make([]byte, 0, len(encoded)+len(tokenHash)+32)
	message = append(message, []byte("yunpin-pairing-claim-v2\x00")...)
	message = append(message, encoded...)
	message = append(message, tokenHash[:]...)
	return message, nil
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
	// General roster mutation is not exposed until signed roster replacement is
	// synchronized end to end. Failed, not-yet-finalized joins use the dedicated
	// rollback capability instead.
	if !twoDeviceRevocationEnabled {
		writeError(w, http.StatusConflict, "device_revocation_not_available_in_two_device_preview")
		return
	}

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
	if err != nil || input.Epoch < 1 || (!identity.AccountSealed && input.Epoch != 1) {
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
	return s.authenticateDevice(false, next)
}

// requireProvisioningAuth permits a still-unsealed first device only for the
// keyring and seal steps. Every normal sync, roster, and pairing endpoint uses
// requireAuth and therefore rejects an incomplete account.
func (s *Server) requireProvisioningAuth(next http.HandlerFunc) http.HandlerFunc {
	return s.authenticateDevice(true, next)
}

func (s *Server) authenticateDevice(allowUnsealed bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		var identity deviceIdentity
		var edKey, xKey []byte
		var sealed, expires sql.NullInt64
		var pairingFinalized bool
		err := s.db.QueryRowContext(r.Context(), `SELECT d.id, d.account_id, d.ed25519_public_key,
			d.x25519_public_key, d.created_at, a.provisioning_sealed_at, a.provisioning_expires_at,
			(NOT EXISTS (SELECT 1 FROM pairings p WHERE p.account_id = d.account_id AND p.new_device_id = d.id AND p.state = 'claimed')
			 OR EXISTS (SELECT 1 FROM pairings p WHERE p.account_id = d.account_id AND p.new_device_id = d.id
			             AND p.state = 'claimed' AND p.finalized_at IS NOT NULL))
			FROM devices d JOIN accounts a ON a.id = d.account_id
			WHERE d.token_hash = ? AND d.revoked_at IS NULL`, digest(token)).
			Scan(&identity.ID, &identity.AccountID, &edKey, &xKey, &identity.CreatedUnix, &sealed, &expires, &pairingFinalized)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_device_token")
			return
		}
		identity.AccountSealed = sealed.Valid
		if !identity.AccountSealed && (!allowUnsealed || !expires.Valid || expires.Int64 <= s.now().UnixMilli()) {
			writeError(w, http.StatusConflict, "account_provisioning_incomplete")
			return
		}
		if !pairingFinalized {
			writeError(w, http.StatusConflict, "pairing_finalization_pending")
			return
		}
		identity.Ed25519Key = ed25519.PublicKey(edKey)
		identity.X25519Key = xKey
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey, identity)))
	}
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := header[len("Bearer "):]
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
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

func constantTimeEqual(left, right []byte) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare(left, right) == 1
}

func decodeCanonicalSecret(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("invalid secret")
	}
	return decoded, nil
}

func validateProvisioningIdentity(accountID, deviceID, deviceToken string) (string, error) {
	if !validID(accountID) || !validID(deviceID) || accountID == deviceID ||
		accountID == strings.Repeat("0", 32) || deviceID == strings.Repeat("0", 32) {
		return "", errors.New("invalid provisioning identifier")
	}
	if _, err := decodeCanonicalSecret(deviceToken, 32); err != nil {
		return "", err
	}
	return deviceToken, nil
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
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
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
	case strings.HasPrefix(path, "/v1/auth/"):
		return "/v1/auth"
	case strings.HasPrefix(path, "/v1/accounts/") && strings.HasSuffix(path, "/claim"):
		return "/v1/accounts/:id/claim"
	case path == "/v1/accounts":
		return "/v1/accounts"
	case strings.HasPrefix(path, "/v1/accounts/") && strings.HasSuffix(path, "/recover"):
		return "/v1/accounts/:id/recover"
	case strings.HasPrefix(path, "/v1/accounts/"):
		return "/v1/accounts/:id"
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
