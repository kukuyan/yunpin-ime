// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	passwordSaltSize   = 16
	passwordHashSize   = 32
	passwordIterations = 600_000
	sessionLifetime    = 30 * 24 * time.Hour
	maxAuthAttempts    = 10
)

type userIdentity struct {
	ID       string
	Username string
}

type passwordRecord struct {
	Salt []byte
	Hash []byte
}

func normalizeUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 64 {
		return "", errors.New("invalid_username")
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '-' || character == '_') {
			return "", errors.New("invalid_username")
		}
	}
	return value, nil
}

func validatePassword(value string) error {
	if len(value) < 12 || len(value) > 256 {
		return errors.New("invalid_password")
	}
	return nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordHashSize)
	if err != nil {
		return "", err
	}
	return "pbkdf2-sha256$600000$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(derived), nil
}

func parsePasswordRecord(value string) (passwordRecord, error) {
	parts := strings.Split(value, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
		return passwordRecord{}, errors.New("invalid password record")
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[2])
	hash, hashErr := base64.RawStdEncoding.DecodeString(parts[3])
	if saltErr != nil || hashErr != nil || len(salt) != passwordSaltSize || len(hash) != passwordHashSize {
		return passwordRecord{}, errors.New("invalid password record")
	}
	return passwordRecord{Salt: salt, Hash: hash}, nil
}

func verifyPassword(record, password string) bool {
	parsed, err := parsePasswordRecord(record)
	if err != nil {
		return false
	}
	derived, err := pbkdf2.Key(sha256.New, password, parsed.Salt, passwordIterations, passwordHashSize)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(derived, parsed.Hash) == 1
}

func (s *Server) authAttemptAllowed(r *http.Request) bool {
	return s.authLimiter.allow(clientIP(r), s.now())
}

func (s *Server) registerUser(w http.ResponseWriter, r *http.Request) {
	if !s.authAttemptAllowed(r) {
		w.Header().Set("Retry-After", "600")
		writeError(w, http.StatusTooManyRequests, "login_rate_limited")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	username, err := normalizeUsername(input.Username)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_username")
		return
	}
	if err := validatePassword(input.Password); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_password")
		return
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	userID, err := randomID(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	token, err := randomSecret(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	now := s.now()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO users(id, username, password_hash, created_at) VALUES(?, ?, ?, ?)`,
		userID, username, passwordHash, now.UnixMilli()); err != nil {
		if isUniqueConstraint(err) {
			writeError(w, http.StatusConflict, "username_unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO auth_sessions(token_hash, user_id, expires_at, created_at) VALUES(?, ?, ?, ?)`,
		digest(token), userID, now.Add(sessionLifetime).UnixMilli(), now.UnixMilli()); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusCreated, sessionResponse{Username: username, Token: token, ExpiresAt: now.Add(sessionLifetime).UTC()})
}

func (s *Server) loginUser(w http.ResponseWriter, r *http.Request) {
	if !s.authAttemptAllowed(r) {
		w.Header().Set("Retry-After", "600")
		writeError(w, http.StatusTooManyRequests, "login_rate_limited")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	username, err := normalizeUsername(input.Username)
	if err != nil || validatePassword(input.Password) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	var userID, passwordHash string
	err = s.db.QueryRowContext(r.Context(), `SELECT id, password_hash FROM users WHERE username = ?`, username).Scan(&userID, &passwordHash)
	if err != nil || !verifyPassword(passwordHash, input.Password) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	token, err := randomSecret(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	now := s.now()
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO auth_sessions(token_hash, user_id, expires_at, created_at) VALUES(?, ?, ?, ?)`,
		digest(token), userID, now.Add(sessionLifetime).UnixMilli(), now.UnixMilli()); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{Username: username, Token: token, ExpiresAt: now.Add(sessionLifetime).UTC()})
}

type sessionResponse struct {
	Username  string    `json:"username"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) logoutUser(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE token_hash = ?`, digest(token)); err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireUserAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		var identity userIdentity
		err := s.db.QueryRowContext(r.Context(), `SELECT u.id, u.username
			FROM auth_sessions s JOIN users u ON u.id = s.user_id
			WHERE s.token_hash = ? AND s.expires_at > ?`, digest(token), s.now().UnixMilli()).Scan(&identity.ID, &identity.Username)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_session")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIdentityKey, identity)))
	}
}

func (s *Server) claimAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	if !validID(accountID) {
		writeError(w, http.StatusNotFound, "account_not_found")
		return
	}
	identity := mustUserIdentity(r)
	var input struct {
		RecoveryAuthentication string `json:"recovery_authentication"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	recoveryAuthentication, err := decodeSized(input.RecoveryAuthentication, 32, 32)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_recovery_authentication")
		return
	}
	result, err := s.db.ExecContext(r.Context(), `UPDATE accounts SET user_id = ?
		WHERE id = ? AND recovery_authentication_hash = ? AND (user_id IS NULL OR user_id = ?)`,
		identity.ID, accountID, digestBytes(recoveryAuthentication), identity.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database_error")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		writeError(w, http.StatusConflict, "account_claim_conflict")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"account_id": accountID, "username": identity.Username})
}

func mustUserIdentity(r *http.Request) userIdentity {
	return r.Context().Value(userIdentityKey).(userIdentity)
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
