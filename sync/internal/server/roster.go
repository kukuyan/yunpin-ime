// SPDX-License-Identifier: Apache-2.0
package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
)

// This independent relay codec deliberately has no dependency on client key
// handling. A protocol/relay golden test freezes the shared canonical bytes.
type rosterDevice struct {
	DeviceID         []byte `json:"device_id" cbor:"1,keyasint"`
	Ed25519PublicKey []byte `json:"ed25519_public_key" cbor:"2,keyasint"`
	X25519PublicKey  []byte `json:"x25519_public_key" cbor:"3,keyasint"`
}

type signedRoster struct {
	Version        uint64         `json:"version" cbor:"1,keyasint"`
	AccountID      []byte         `json:"account_id" cbor:"2,keyasint"`
	Devices        []rosterDevice `json:"devices" cbor:"3,keyasint"`
	SignerDeviceID []byte         `json:"signer_device_id" cbor:"4,keyasint"`
	Signature      []byte         `json:"signature" cbor:"5,keyasint"`
	PreviousHash   []byte         `json:"previous_hash,omitempty" cbor:"6,keyasint,omitempty"`
}

func rosterDigest(roster signedRoster) ([]byte, error) {
	if roster.Version == 0 || roster.Version > math.MaxInt64 || len(roster.AccountID) != 16 ||
		len(roster.SignerDeviceID) != 16 || len(roster.Devices) < 2 || len(roster.Devices) > 256 ||
		len(roster.Signature) != ed25519.SignatureSize ||
		bytes.Equal(roster.AccountID, make([]byte, 16)) || bytes.Equal(roster.SignerDeviceID, make([]byte, 16)) ||
		(len(roster.PreviousHash) != 0 && (len(roster.PreviousHash) != 32 || roster.Version < 2)) {
		return nil, errors.New("invalid signed roster metadata")
	}
	var previous, signer []byte
	for _, device := range roster.Devices {
		if len(device.DeviceID) != 16 || len(device.Ed25519PublicKey) != 32 || len(device.X25519PublicKey) != 32 ||
			bytes.Equal(device.DeviceID, make([]byte, 16)) || bytes.Equal(device.Ed25519PublicKey, make([]byte, 32)) ||
			bytes.Equal(device.X25519PublicKey, make([]byte, 32)) || (previous != nil && bytes.Compare(previous, device.DeviceID) >= 0) {
			return nil, errors.New("invalid signed roster device")
		}
		previous = device.DeviceID
		if bytes.Equal(device.DeviceID, roster.SignerDeviceID) {
			signer = device.Ed25519PublicKey
		}
	}
	unsigned := struct {
		Version        uint64         `cbor:"1,keyasint"`
		AccountID      []byte         `cbor:"2,keyasint"`
		Devices        []rosterDevice `cbor:"3,keyasint"`
		SignerDeviceID []byte         `cbor:"4,keyasint"`
		PreviousHash   []byte         `cbor:"5,keyasint,omitempty"`
	}{roster.Version, roster.AccountID, roster.Devices, roster.SignerDeviceID, roster.PreviousHash}
	encoded, err := canonicalCBOR.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	domain := "yunpin-pairing-roster-v1\x00"
	if len(roster.PreviousHash) != 0 {
		domain = "yunpin-pairing-roster-chain-v1\x00"
	}
	if len(signer) != ed25519.PublicKeySize || !ed25519.Verify(signer, append([]byte(domain), encoded...), roster.Signature) {
		return nil, errors.New("invalid roster signature")
	}
	encoded, err = canonicalCBOR.Marshal(roster)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return append([]byte(nil), digest[:]...), nil
}

func verifyRosterAdvance(previous, next signedRoster) error {
	digest, err := rosterDigest(previous)
	if err != nil {
		return err
	}
	if _, err := rosterDigest(next); err != nil {
		return err
	}
	if next.Version != previous.Version+1 || !bytes.Equal(next.PreviousHash, digest) ||
		!bytes.Equal(next.AccountID, previous.AccountID) || len(next.Devices) != len(previous.Devices)+1 || len(next.Devices) > maxActiveDevices {
		return errors.New("roster update does not extend its predecessor")
	}
	signerTrusted := false
	for _, old := range previous.Devices {
		preserved := false
		for _, device := range next.Devices {
			if bytes.Equal(old.DeviceID, device.DeviceID) {
				preserved = bytes.Equal(old.Ed25519PublicKey, device.Ed25519PublicKey) && bytes.Equal(old.X25519PublicKey, device.X25519PublicKey)
				break
			}
		}
		if !preserved {
			return errors.New("roster changes a trusted member")
		}
		if bytes.Equal(old.DeviceID, next.SignerDeviceID) {
			signerTrusted = true
		}
	}
	if !signerTrusted {
		return errors.New("roster signer is not previously trusted")
	}
	return nil
}

func loadRosterHead(ctx context.Context, tx *sql.Tx, account string) (signedRoster, []byte, error) {
	var encoded, digest []byte
	err := tx.QueryRowContext(ctx, `SELECT roster_json, digest FROM account_rosters WHERE account_id = ? ORDER BY version DESC LIMIT 1`, account).Scan(&encoded, &digest)
	if err != nil {
		return signedRoster{}, nil, err
	}
	var roster signedRoster
	if err := json.Unmarshal(encoded, &roster); err != nil {
		return signedRoster{}, nil, err
	}
	return roster, digest, nil
}

func verifyRosterDevices(ctx context.Context, tx *sql.Tx, account string, roster signedRoster) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, ed25519_public_key, x25519_public_key FROM devices WHERE account_id = ? AND revoked_at IS NULL ORDER BY id`, account)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var id string
		var ed, x []byte
		if err := rows.Scan(&id, &ed, &x); err != nil {
			return err
		}
		if index >= len(roster.Devices) {
			return errors.New("roster omits an existing device")
		}
		device := roster.Devices[index]
		if hex.EncodeToString(device.DeviceID) != id || !bytes.Equal(device.Ed25519PublicKey, ed) || !bytes.Equal(device.X25519PublicKey, x) {
			return errors.New("roster keys differ from enrolled devices")
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(roster.Devices) {
		return errors.New("roster contains an unenrolled device")
	}
	return nil
}

func insertRoster(ctx context.Context, tx *sql.Tx, account string, roster signedRoster) error {
	digest, err := rosterDigest(roster)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(roster)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_rosters(account_id, version, digest, roster_json) VALUES(?, ?, ?, ?)`, account, roster.Version, digest, encoded)
	return err
}

func (s *Server) publishRosterAnchor(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r)
	var roster signedRoster
	if !decodeJSON(w, r, &roster) {
		return
	}
	digest, err := rosterDigest(roster)
	if err != nil || roster.Version != 1 || len(roster.PreviousHash) != 0 || len(roster.Devices) != 2 || hex.EncodeToString(roster.AccountID) != identity.AccountID {
		writeError(w, http.StatusBadRequest, "invalid_roster_anchor")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database_error")
		return
	}
	defer tx.Rollback()
	_, existing, err := loadRosterHead(r.Context(), tx, identity.AccountID)
	if err == nil {
		if !bytes.Equal(existing, digest) {
			writeError(w, 409, "roster_conflict")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, 500, "database_error")
		return
	}
	if err := verifyRosterDevices(r.Context(), tx, identity.AccountID, roster); err != nil {
		writeError(w, 409, "roster_conflict")
		return
	}
	var pending int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pairings WHERE account_id = ? AND state = 'claimed' AND finalized_at IS NULL`, identity.AccountID).Scan(&pending); err != nil {
		writeError(w, 500, "database_error")
		return
	}
	if pending != 0 {
		writeError(w, 409, "roster_conflict")
		return
	}
	if err := insertRoster(r.Context(), tx, identity.AccountID, roster); err != nil {
		writeError(w, 500, "database_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getRosterUpdates(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r)
	version, err := strconv.ParseUint(r.URL.Query().Get("after_version"), 10, 64)
	hash, hashErr := hex.DecodeString(r.URL.Query().Get("after_hash"))
	if err != nil || version < 1 || version >= maxActiveDevices || hashErr != nil || len(hash) != 32 || hex.EncodeToString(hash) != r.URL.Query().Get("after_hash") {
		writeError(w, 400, "invalid_roster_checkpoint")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database_error")
		return
	}
	defer tx.Rollback()
	head, headHash, err := loadRosterHead(r.Context(), tx, identity.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "roster_not_initialized")
		return
	}
	if err != nil {
		writeError(w, 500, "database_error")
		return
	}
	var knownHash []byte
	err = tx.QueryRowContext(r.Context(), `SELECT digest FROM account_rosters WHERE account_id = ? AND version = ?`, identity.AccountID, version).Scan(&knownHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !bytes.Equal(hash, knownHash)) {
		writeError(w, 409, "roster_conflict")
		return
	}
	if err != nil {
		writeError(w, 500, "database_error")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT roster_json FROM account_rosters WHERE account_id = ? AND version > ? ORDER BY version LIMIT 16`, identity.AccountID, version)
	if err != nil {
		writeError(w, 500, "database_error")
		return
	}
	updates := make([]signedRoster, 0, 16)
	for rows.Next() {
		var encoded []byte
		var roster signedRoster
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			writeError(w, 500, "database_error")
			return
		}
		if err := json.Unmarshal(encoded, &roster); err != nil {
			rows.Close()
			writeError(w, 500, "database_error")
			return
		}
		updates = append(updates, roster)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "database_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"head_version": head.Version, "head_hash": hex.EncodeToString(headHash), "updates": updates})
}
