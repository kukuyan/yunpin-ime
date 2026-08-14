// SPDX-License-Identifier: Apache-2.0
package syncclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/kukuyan/yunpin-ime/protocol"
)

const maxResponseBytes = 2 << 20

type Client struct {
	endpoint         Endpoint
	http             *http.Client
	userSessionToken string
}

type Option func(*http.Client)

func WithTransport(transport http.RoundTripper) Option {
	return func(client *http.Client) {
		if session, ok := client.Transport.(sessionTransport); ok {
			session.base = transport
			client.Transport = session
			return
		}
		client.Transport = transport
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(client *http.Client) { client.Timeout = timeout }
}

// WithUserSession attaches one protected, user-login session to account
// registration and account-claim calls. Device synchronization continues to
// supply its own device bearer explicitly and never uses this session.
func WithUserSession(token string) Option {
	return func(client *http.Client) {
		client.Transport = sessionTransport{base: client.Transport, token: token}
	}
}

type sessionTransport struct {
	base  http.RoundTripper
	token string
}

func (transport sessionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.ContainsAny(transport.token, " \t\r\n") || transport.token == "" {
		return nil, errors.New("invalid user session")
	}
	if request.Header.Get("Authorization") != "" {
		base := transport.base
		if base == nil {
			base = http.DefaultTransport
		}
		return base.RoundTrip(request)
	}
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.Header.Set("Authorization", "Bearer "+transport.token)
	base := transport.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func New(endpoint Endpoint, options ...Option) *Client {
	httpClient := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for _, option := range options {
		option(httpClient)
	}
	return &Client{endpoint: endpoint, http: httpClient}
}

type APIError struct {
	Status int
	Code   string
}

func (err *APIError) Error() string {
	return fmt.Sprintf("sync relay returned HTTP %d (%s)", err.Status, err.Code)
}

func (client *Client) doJSON(ctx context.Context, method, path, token string, input, output any, expectedStatus int) error {
	var body io.Reader
	if input != nil {
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(input); err != nil {
			return fmt.Errorf("encode sync request: %w", err)
		}
		body = &encoded
	}
	request, err := http.NewRequestWithContext(ctx, method, client.endpoint.resolve(path), body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "YunPin-SyncClient/0.1")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		if strings.ContainsAny(token, "\r\n") {
			return errors.New("invalid device token")
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("sync relay request failed: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read sync relay response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return errors.New("sync relay response exceeds size limit")
	}
	if response.StatusCode != expectedStatus {
		var api struct {
			Code string `json:"error"`
		}
		if json.Unmarshal(payload, &api) != nil || api.Code == "" {
			api.Code = "unexpected_status"
		}
		return &APIError{Status: response.StatusCode, Code: api.Code}
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return errors.New("sync relay returned invalid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("sync relay returned invalid JSON")
	}
	return nil
}

type DeviceRegistration struct {
	DeviceNameCiphertext []byte
	Ed25519PublicKey     ed25519.PublicKey
	X25519PublicKey      []byte
}

type AccountRegistration struct {
	RecoveryAuthentication []byte
	DeviceRegistration
}

type Account struct {
	AccountID   []byte
	DeviceID    []byte
	DeviceToken string
	// RollbackToken is populated only for a locally provisioned first account.
	// It is a short-lived, single-purpose capability and is never the device
	// bearer token used by normal sync operations.
	RollbackToken string
	// DeviceRollbackToken is populated only for a joining device. It is a
	// short-lived capability for idempotently undoing a failed local pairing
	// commit and is never used for sync authentication.
	DeviceRollbackToken string
}

// UserSession is an opaque bearer returned by a selected YunPin relay after a
// successful password login. Store Token only in the platform secret store;
// it is intentionally absent from endpoint configuration and sync payloads.
type UserSession struct {
	Username  string
	Token     string
	ExpiresAt time.Time
}

type userSessionWire struct {
	Username  string    `json:"username"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func sessionFromWire(wire userSessionWire) (UserSession, error) {
	if wire.Username == "" || len(wire.Username) > 64 || wire.Token == "" ||
		strings.ContainsAny(wire.Token, " \t\r\n") || wire.ExpiresAt.IsZero() {
		return UserSession{}, errors.New("sync relay returned invalid login session")
	}
	return UserSession{Username: wire.Username, Token: wire.Token, ExpiresAt: wire.ExpiresAt.UTC()}, nil
}

// Register creates a self-hosted YunPin login. The password is sent only to
// the selected relay over its configured transport and is never persisted by
// this client.
func (client *Client) Register(ctx context.Context, username, password string) (UserSession, error) {
	var response userSessionWire
	if err := client.doJSON(ctx, http.MethodPost, "/v1/auth/register", "", map[string]string{
		"username": username, "password": password,
	}, &response, http.StatusCreated); err != nil {
		return UserSession{}, err
	}
	return sessionFromWire(response)
}

// Login exchanges the supplied password for a bounded opaque session.
func (client *Client) Login(ctx context.Context, username, password string) (UserSession, error) {
	var response userSessionWire
	if err := client.doJSON(ctx, http.MethodPost, "/v1/auth/login", "", map[string]string{
		"username": username, "password": password,
	}, &response, http.StatusOK); err != nil {
		return UserSession{}, err
	}
	return sessionFromWire(response)
}

func (client *Client) Logout(ctx context.Context, token string) error {
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("invalid user session")
	}
	return client.doJSON(ctx, http.MethodPost, "/v1/auth/logout", token, map[string]any{}, nil, http.StatusNoContent)
}

// ClaimAccount binds an existing recovery-protected account to the logged-in
// user session configured with WithUserSession. It never uploads the human
// recovery key, only its already-domain-separated authentication material.
func (client *Client) ClaimAccount(ctx context.Context, accountID, recoveryAuthentication []byte) error {
	if len(accountID) != 16 || len(recoveryAuthentication) != 32 {
		return errors.New("account claim requires valid account and recovery authentication")
	}
	return client.doJSON(ctx, http.MethodPost, "/v1/accounts/"+hex.EncodeToString(accountID)+"/claim", "",
		map[string]string{"recovery_authentication": base64.RawURLEncoding.EncodeToString(recoveryAuthentication)}, nil, http.StatusOK)
}

type PairingInvitation struct {
	PairingID               []byte
	PairingSecret           []byte
	AccountID               []byte
	CreatorDeviceID         []byte
	CreatorEd25519PublicKey ed25519.PublicKey
	CreatorX25519PublicKey  []byte
	ExpiresAt               time.Time
}

type PairingStatus struct {
	PairingID            []byte
	State                string
	ExpiresAt            time.Time
	ClaimExpiresAt       time.Time
	ReadyExpiresAt       time.Time
	Expired              bool
	DeviceID             []byte
	JoinProof            []byte
	DeviceNameCiphertext []byte
	Ed25519PublicKey     ed25519.PublicKey
	X25519PublicKey      []byte
}

type PairingClaim struct {
	Account
	EncryptedKeyring protocol.SealedBox
}

type Device struct {
	ID               []byte
	NameCiphertext   []byte
	Ed25519PublicKey ed25519.PublicKey
	X25519PublicKey  []byte
	CreatedAt        time.Time
	Current          bool
	Revoked          bool
	RevokedAt        *time.Time
}

type registrationWire struct {
	AccountID              string `json:"account_id,omitempty"`
	DeviceID               string `json:"device_id"`
	DeviceToken            string `json:"device_token"`
	RollbackToken          string `json:"rollback_token,omitempty"`
	RecoveryAuthentication string `json:"recovery_authentication"`
	DeviceNameCiphertext   string `json:"device_name_ciphertext"`
	Ed25519PublicKey       string `json:"ed25519_public_key"`
	X25519PublicKey        string `json:"x25519_public_key"`
}

func registrationToWire(credentials Account, registration AccountRegistration, includeAccountID bool) (registrationWire, error) {
	if len(registration.RecoveryAuthentication) != 32 || len(registration.DeviceNameCiphertext) < 16 ||
		len(registration.DeviceNameCiphertext) > 512 || len(registration.Ed25519PublicKey) != ed25519.PublicKeySize ||
		len(registration.X25519PublicKey) != 32 {
		return registrationWire{}, errors.New("invalid account registration material")
	}
	if err := validateProvisionedAccount(credentials); err != nil {
		return registrationWire{}, err
	}
	encode := base64.RawURLEncoding.EncodeToString
	wire := registrationWire{
		DeviceID:               hex.EncodeToString(credentials.DeviceID),
		DeviceToken:            credentials.DeviceToken,
		RecoveryAuthentication: encode(registration.RecoveryAuthentication),
		DeviceNameCiphertext:   encode(registration.DeviceNameCiphertext),
		Ed25519PublicKey:       encode(registration.Ed25519PublicKey),
		X25519PublicKey:        encode(registration.X25519PublicKey),
	}
	if includeAccountID {
		wire.AccountID = hex.EncodeToString(credentials.AccountID)
		wire.RollbackToken = credentials.RollbackToken
	}
	return wire, nil
}

type accountWire struct {
	AccountID   string `json:"account_id"`
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
}

func accountFromWire(wire accountWire) (Account, error) {
	accountID, err := hex.DecodeString(wire.AccountID)
	if err != nil || len(accountID) != 16 || wire.AccountID != strings.ToLower(wire.AccountID) {
		return Account{}, errors.New("sync relay returned invalid account ID")
	}
	deviceID, err := hex.DecodeString(wire.DeviceID)
	if err != nil || len(deviceID) != 16 || wire.DeviceID != strings.ToLower(wire.DeviceID) || wire.DeviceToken == "" {
		return Account{}, errors.New("sync relay returned invalid device credentials")
	}
	return Account{AccountID: accountID, DeviceID: deviceID, DeviceToken: wire.DeviceToken}, nil
}

func decodeHexID(encoded string) ([]byte, error) {
	value, err := hex.DecodeString(encoded)
	if err != nil || len(value) != 16 || encoded != strings.ToLower(encoded) {
		return nil, errors.New("sync relay returned an invalid identifier")
	}
	return value, nil
}

func decodeBase64Size(encoded string, minimum, maximum int) ([]byte, error) {
	if encoded == "" || minimum < 0 || maximum < minimum || len(encoded) > base64.RawURLEncoding.EncodedLen(maximum) {
		return nil, errors.New("sync relay returned invalid base64url material")
	}
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(value) < minimum || len(value) > maximum || base64.RawURLEncoding.EncodeToString(value) != encoded {
		return nil, errors.New("sync relay returned invalid base64url material")
	}
	return value, nil
}

func deviceRegistrationToWire(registration DeviceRegistration) (map[string]string, error) {
	if len(registration.DeviceNameCiphertext) < 16 || len(registration.DeviceNameCiphertext) > 512 ||
		len(registration.Ed25519PublicKey) != ed25519.PublicKeySize || len(registration.X25519PublicKey) != 32 {
		return nil, errors.New("invalid device registration material")
	}
	encode := base64.RawURLEncoding.EncodeToString
	return map[string]string{
		"device_name_ciphertext": encode(registration.DeviceNameCiphertext),
		"ed25519_public_key":     encode(registration.Ed25519PublicKey),
		"x25519_public_key":      encode(registration.X25519PublicKey),
	}, nil
}

func GenerateAccountCredentials(source io.Reader) (Account, error) {
	accountID := make([]byte, 16)
	deviceID := make([]byte, 16)
	if source == nil {
		return Account{}, errors.New("cryptographic random source is required")
	}
	if _, err := io.ReadFull(source, accountID); err != nil {
		return Account{}, fmt.Errorf("generate account ID: %w", err)
	}
	if _, err := io.ReadFull(source, deviceID); err != nil {
		return Account{}, fmt.Errorf("generate device ID: %w", err)
	}
	token, err := GenerateDeviceToken(source)
	if err != nil {
		return Account{}, err
	}
	rollbackToken, err := GenerateDeviceToken(source)
	if err != nil {
		return Account{}, err
	}
	credentials := Account{AccountID: accountID, DeviceID: deviceID, DeviceToken: token, RollbackToken: rollbackToken}
	if err := validateProvisionedAccount(credentials); err != nil {
		return Account{}, err
	}
	return credentials, nil
}

func GenerateDeviceCredentials(accountID []byte, source io.Reader) (Account, error) {
	if len(accountID) != 16 || source == nil {
		return Account{}, errors.New("account ID and cryptographic random source are required")
	}
	deviceID := make([]byte, 16)
	if _, err := io.ReadFull(source, deviceID); err != nil {
		return Account{}, fmt.Errorf("generate device ID: %w", err)
	}
	token, err := GenerateDeviceToken(source)
	if err != nil {
		return Account{}, err
	}
	rollbackToken, err := GenerateDeviceToken(source)
	if err != nil {
		return Account{}, err
	}
	credentials := Account{AccountID: append([]byte(nil), accountID...), DeviceID: deviceID,
		DeviceToken: token, DeviceRollbackToken: rollbackToken}
	if err := validateProvisionedAccount(credentials); err != nil {
		return Account{}, err
	}
	return credentials, nil
}

func GenerateDeviceToken(source io.Reader) (string, error) {
	if source == nil {
		return "", errors.New("cryptographic random source is required")
	}
	value := make([]byte, 32)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", fmt.Errorf("generate device token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validateProvisionedAccount(credentials Account) error {
	zeroID := make([]byte, 16)
	if len(credentials.AccountID) != 16 || len(credentials.DeviceID) != 16 || bytes.Equal(credentials.AccountID, credentials.DeviceID) ||
		bytes.Equal(credentials.AccountID, zeroID) || bytes.Equal(credentials.DeviceID, zeroID) {
		return errors.New("invalid provisioning identifiers")
	}
	token, err := base64.RawURLEncoding.DecodeString(credentials.DeviceToken)
	if err != nil || len(token) != 32 || base64.RawURLEncoding.EncodeToString(token) != credentials.DeviceToken {
		return errors.New("invalid provisioning device token")
	}
	if credentials.RollbackToken != "" {
		rollback, err := base64.RawURLEncoding.DecodeString(credentials.RollbackToken)
		if err != nil || len(rollback) != 32 || base64.RawURLEncoding.EncodeToString(rollback) != credentials.RollbackToken {
			return errors.New("invalid provisioning rollback capability")
		}
	}
	if credentials.DeviceRollbackToken != "" {
		rollback, err := base64.RawURLEncoding.DecodeString(credentials.DeviceRollbackToken)
		if err != nil || len(rollback) != 32 || base64.RawURLEncoding.EncodeToString(rollback) != credentials.DeviceRollbackToken {
			return errors.New("invalid paired-device rollback capability")
		}
	}
	return nil
}

func sameAccount(left, right Account) bool {
	return bytes.Equal(left.AccountID, right.AccountID) && bytes.Equal(left.DeviceID, right.DeviceID) && left.DeviceToken == right.DeviceToken
}

func (client *Client) CreateAccount(ctx context.Context, credentials Account, registration AccountRegistration) (Account, error) {
	if credentials.RollbackToken == "" {
		return Account{}, errors.New("provisioning rollback capability is required")
	}
	wire, err := registrationToWire(credentials, registration, true)
	if err != nil {
		return Account{}, err
	}
	var response accountWire
	if err := client.doJSON(ctx, http.MethodPost, "/v1/accounts", "", wire, &response, http.StatusCreated); err != nil {
		return Account{}, err
	}
	created, err := accountFromWire(response)
	if err != nil || !sameAccount(created, credentials) {
		return Account{}, errors.New("sync relay returned mismatched provisioning identity")
	}
	created.RollbackToken = credentials.RollbackToken
	return created, nil
}

// DeleteAccount rolls back a just-created account.  The relay accepts this
// only while the authenticated device is still the sole device and the
// account has no pairings or vocabulary envelopes.
func (client *Client) DeleteAccount(ctx context.Context, accountID []byte, rollbackToken string) error {
	if len(accountID) != 16 || rollbackToken == "" {
		return errors.New("account ID and rollback capability are required")
	}
	path := "/v1/accounts/" + hex.EncodeToString(accountID)
	return client.doJSON(ctx, http.MethodDelete, path, rollbackToken, nil, nil, http.StatusNoContent)
}

func (client *Client) SealAccount(ctx context.Context, accountID []byte, deviceToken string) error {
	if len(accountID) != 16 || deviceToken == "" {
		return errors.New("account ID and device token are required")
	}
	path := "/v1/accounts/" + hex.EncodeToString(accountID) + "/seal"
	return client.doJSON(ctx, http.MethodPost, path, deviceToken, map[string]any{}, nil, http.StatusNoContent)
}

// DeleteCurrentDevice rolls back the otherwise-unused device created by a
// completed pairing claim. The relay rejects devices that have already
// written sync state or that are not backed by exactly one claimed pairing.
func (client *Client) DeleteCurrentDevice(ctx context.Context, accountID, deviceID, pairingID []byte, rollbackToken string) error {
	if len(accountID) != 16 || len(deviceID) != 16 || len(pairingID) != 16 || rollbackToken == "" {
		return errors.New("paired-device rollback identity and capability are required")
	}
	path := "/v1/devices/current?account_id=" + hex.EncodeToString(accountID) +
		"&device_id=" + hex.EncodeToString(deviceID) + "&pairing_id=" + hex.EncodeToString(pairingID)
	return client.doJSON(ctx, http.MethodDelete, path, rollbackToken, nil, nil, http.StatusNoContent)
}

func (client *Client) RecoverAccount(ctx context.Context, credentials Account, registration AccountRegistration) (Account, error) {
	wire, err := registrationToWire(credentials, registration, false)
	if err != nil {
		return Account{}, err
	}
	var response accountWire
	path := "/v1/accounts/" + hex.EncodeToString(credentials.AccountID) + "/recover"
	if err := client.doJSON(ctx, http.MethodPost, path, "", wire, &response, http.StatusCreated); err != nil {
		return Account{}, err
	}
	recovered, err := accountFromWire(response)
	if err != nil || !sameAccount(recovered, credentials) {
		return Account{}, errors.New("sync relay returned mismatched recovery identity")
	}
	return recovered, nil
}

func GeneratePairingInvitation(creator Account, registration DeviceRegistration, source io.Reader) (PairingInvitation, error) {
	if err := validateProvisionedAccount(creator); err != nil {
		return PairingInvitation{}, err
	}
	if _, err := deviceRegistrationToWire(registration); err != nil {
		return PairingInvitation{}, err
	}
	if source == nil {
		return PairingInvitation{}, errors.New("cryptographic random source is required")
	}
	pairingID := make([]byte, 16)
	pairingSecret := make([]byte, protocol.PairingSecretSize)
	if _, err := io.ReadFull(source, pairingID); err != nil {
		return PairingInvitation{}, fmt.Errorf("generate pairing ID: %w", err)
	}
	if _, err := io.ReadFull(source, pairingSecret); err != nil {
		return PairingInvitation{}, fmt.Errorf("generate pairing secret: %w", err)
	}
	if bytes.Equal(pairingID, make([]byte, 16)) {
		return PairingInvitation{}, errors.New("random source returned an invalid pairing ID")
	}
	return PairingInvitation{
		PairingID: pairingID, PairingSecret: pairingSecret, AccountID: append([]byte(nil), creator.AccountID...),
		CreatorDeviceID:         append([]byte(nil), creator.DeviceID...),
		CreatorEd25519PublicKey: append(ed25519.PublicKey(nil), registration.Ed25519PublicKey...),
		CreatorX25519PublicKey:  append([]byte(nil), registration.X25519PublicKey...),
	}, nil
}

func validatePairingInvitation(invitation PairingInvitation) error {
	if len(invitation.PairingID) != 16 || len(invitation.PairingSecret) != protocol.PairingSecretSize ||
		len(invitation.AccountID) != 16 || len(invitation.CreatorDeviceID) != 16 ||
		len(invitation.CreatorEd25519PublicKey) != ed25519.PublicKeySize || len(invitation.CreatorX25519PublicKey) != 32 ||
		bytes.Equal(invitation.PairingID, make([]byte, 16)) || bytes.Equal(invitation.AccountID, make([]byte, 16)) ||
		bytes.Equal(invitation.CreatorDeviceID, make([]byte, 16)) {
		return errors.New("pairing invitation is invalid")
	}
	return nil
}

func pairingVerifier(invitation PairingInvitation) ([]byte, error) {
	if err := validatePairingInvitation(invitation); err != nil {
		return nil, err
	}
	return protocol.PairingRelayVerifier(invitation.PairingSecret, invitation.PairingID)
}

func (client *Client) CreatePairing(ctx context.Context, creator Account, invitation PairingInvitation) (PairingInvitation, error) {
	if err := validateProvisionedAccount(creator); err != nil || !bytes.Equal(creator.AccountID, invitation.AccountID) ||
		!bytes.Equal(creator.DeviceID, invitation.CreatorDeviceID) {
		return PairingInvitation{}, errors.New("pairing invitation does not belong to the authenticated creator")
	}
	verifier, err := pairingVerifier(invitation)
	if err != nil {
		return PairingInvitation{}, err
	}
	var response struct {
		PairingID string    `json:"pairing_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	request := map[string]string{
		"pairing_id":       hex.EncodeToString(invitation.PairingID),
		"pairing_verifier": base64.RawURLEncoding.EncodeToString(verifier),
	}
	if err := client.doJSON(ctx, http.MethodPost, "/v1/pairings", creator.DeviceToken, request, &response, http.StatusCreated); err != nil {
		return PairingInvitation{}, err
	}
	id, err := decodeHexID(response.PairingID)
	if err != nil || !bytes.Equal(id, invitation.PairingID) || response.ExpiresAt.IsZero() {
		return PairingInvitation{}, errors.New("sync relay returned a mismatched pairing invitation")
	}
	invitation.ExpiresAt = response.ExpiresAt
	return invitation, nil
}

func PairingTranscript(invitation PairingInvitation, joining Account, registration DeviceRegistration) (protocol.PairingTranscript, error) {
	if err := validatePairingInvitation(invitation); err != nil {
		return protocol.PairingTranscript{}, err
	}
	if err := validateProvisionedAccount(joining); err != nil || !bytes.Equal(joining.AccountID, invitation.AccountID) ||
		bytes.Equal(joining.DeviceID, invitation.CreatorDeviceID) {
		return protocol.PairingTranscript{}, errors.New("joining credentials do not match the invitation")
	}
	if _, err := deviceRegistrationToWire(registration); err != nil {
		return protocol.PairingTranscript{}, err
	}
	return protocol.PairingTranscript{
		PairingID: append([]byte(nil), invitation.PairingID...), AccountID: append([]byte(nil), invitation.AccountID...),
		CreatorDeviceID: append([]byte(nil), invitation.CreatorDeviceID...), JoiningDeviceID: append([]byte(nil), joining.DeviceID...),
		CreatorEd25519PublicKey: append([]byte(nil), invitation.CreatorEd25519PublicKey...),
		JoiningEd25519PublicKey: append([]byte(nil), registration.Ed25519PublicKey...),
		CreatorX25519PublicKey:  append([]byte(nil), invitation.CreatorX25519PublicKey...),
		JoiningX25519PublicKey:  append([]byte(nil), registration.X25519PublicKey...),
	}, nil
}

func (client *Client) JoinPairing(ctx context.Context, invitation PairingInvitation, joining Account, registration DeviceRegistration) (protocol.PairingTranscript, error) {
	transcript, err := PairingTranscript(invitation, joining, registration)
	if err != nil {
		return protocol.PairingTranscript{}, err
	}
	verifier, err := pairingVerifier(invitation)
	if err != nil {
		return protocol.PairingTranscript{}, err
	}
	proof, err := protocol.PairingJoinProof(invitation.PairingSecret, transcript)
	if err != nil {
		return protocol.PairingTranscript{}, err
	}
	wire, err := deviceRegistrationToWire(registration)
	if err != nil {
		return protocol.PairingTranscript{}, err
	}
	if joining.DeviceRollbackToken == "" {
		return protocol.PairingTranscript{}, errors.New("paired-device rollback capability is required")
	}
	if _, err := decodeBase64Size(joining.DeviceRollbackToken, 32, 32); err != nil {
		return protocol.PairingTranscript{}, errors.New("paired-device rollback capability is invalid")
	}
	wire["pairing_verifier"] = base64.RawURLEncoding.EncodeToString(verifier)
	wire["device_id"] = hex.EncodeToString(joining.DeviceID)
	wire["join_proof"] = base64.RawURLEncoding.EncodeToString(proof)
	wire["rollback_token"] = joining.DeviceRollbackToken
	var response struct {
		State string `json:"state"`
	}
	path := "/v1/pairings/" + hex.EncodeToString(invitation.PairingID)
	if err := client.doJSON(ctx, http.MethodPut, path, "", wire, &response, http.StatusOK); err != nil {
		return protocol.PairingTranscript{}, err
	}
	// The relay deliberately normalizes both the initial transition and every
	// exact advanced-state replay to "joined".  Accepting later states here
	// would mask a relay contract drift or an injected response.
	if response.State != "joined" {
		return protocol.PairingTranscript{}, errors.New("sync relay returned invalid pairing state")
	}
	return transcript, nil
}

func (client *Client) GetPairing(ctx context.Context, invitation PairingInvitation, creator Account) (PairingStatus, error) {
	if err := validatePairingInvitation(invitation); err != nil || creator.DeviceToken == "" ||
		!bytes.Equal(creator.AccountID, invitation.AccountID) || !bytes.Equal(creator.DeviceID, invitation.CreatorDeviceID) {
		return PairingStatus{}, errors.New("pairing ID and device token are required")
	}
	var wire struct {
		PairingID            string    `json:"pairing_id"`
		State                string    `json:"state"`
		ExpiresAt            time.Time `json:"expires_at"`
		ClaimExpiresAt       time.Time `json:"claim_expires_at,omitempty"`
		ReadyExpiresAt       time.Time `json:"ready_expires_at,omitempty"`
		Expired              bool      `json:"expired"`
		DeviceID             string    `json:"device_id,omitempty"`
		JoinProof            string    `json:"join_proof,omitempty"`
		DeviceNameCiphertext string    `json:"device_name_ciphertext,omitempty"`
		Ed25519PublicKey     string    `json:"ed25519_public_key,omitempty"`
		X25519PublicKey      string    `json:"x25519_public_key,omitempty"`
	}
	path := "/v1/pairings/" + hex.EncodeToString(invitation.PairingID)
	if err := client.doJSON(ctx, http.MethodGet, path, creator.DeviceToken, nil, &wire, http.StatusOK); err != nil {
		return PairingStatus{}, err
	}
	id, err := decodeHexID(wire.PairingID)
	if err != nil || !bytes.Equal(id, invitation.PairingID) || wire.ExpiresAt.IsZero() ||
		(wire.State != "created" && wire.State != "joined" && wire.State != "approved" && wire.State != "claimed" &&
			wire.State != "ready" && wire.State != "finalized") {
		return PairingStatus{}, errors.New("sync relay returned invalid pairing status")
	}
	status := PairingStatus{PairingID: id, State: wire.State, ExpiresAt: wire.ExpiresAt,
		ClaimExpiresAt: wire.ClaimExpiresAt, ReadyExpiresAt: wire.ReadyExpiresAt, Expired: wire.Expired}
	if wire.DeviceID != "" || wire.JoinProof != "" || wire.DeviceNameCiphertext != "" || wire.Ed25519PublicKey != "" || wire.X25519PublicKey != "" {
		status.DeviceID, err = decodeHexID(wire.DeviceID)
		if err == nil {
			status.JoinProof, err = decodeBase64Size(wire.JoinProof, 32, 32)
		}
		if err == nil {
			status.DeviceNameCiphertext, err = decodeBase64Size(wire.DeviceNameCiphertext, 16, 512)
		}
		if err == nil {
			status.Ed25519PublicKey, err = decodeBase64Size(wire.Ed25519PublicKey, ed25519.PublicKeySize, ed25519.PublicKeySize)
		}
		if err == nil {
			status.X25519PublicKey, err = decodeBase64Size(wire.X25519PublicKey, 32, 32)
		}
		if err != nil {
			return PairingStatus{}, errors.New("sync relay returned invalid pending device keys")
		}
		transcript := protocol.PairingTranscript{
			PairingID: invitation.PairingID, AccountID: invitation.AccountID,
			CreatorDeviceID: invitation.CreatorDeviceID, JoiningDeviceID: status.DeviceID,
			CreatorEd25519PublicKey: invitation.CreatorEd25519PublicKey, JoiningEd25519PublicKey: status.Ed25519PublicKey,
			CreatorX25519PublicKey: invitation.CreatorX25519PublicKey, JoiningX25519PublicKey: status.X25519PublicKey,
		}
		if err := protocol.VerifyPairingJoinProof(invitation.PairingSecret, transcript, status.JoinProof); err != nil {
			return PairingStatus{}, errors.New("sync relay returned unauthenticated pending device keys")
		}
	}
	return status, nil
}

func (client *Client) ApprovePairing(ctx context.Context, pairingID []byte, deviceToken string, box protocol.SealedBox) error {
	if len(pairingID) != 16 || deviceToken == "" {
		return errors.New("pairing ID and device token are required")
	}
	encoded, err := protocol.EncodeSealedBox(box)
	if err != nil {
		return err
	}
	var response struct {
		State string `json:"state"`
	}
	path := "/v1/pairings/" + hex.EncodeToString(pairingID) + "/approve"
	if err := client.doJSON(ctx, http.MethodPost, path, deviceToken, map[string]string{"encrypted_keyring": encoded}, &response, http.StatusOK); err != nil {
		return err
	}
	if response.State != "approved" {
		return errors.New("sync relay returned invalid pairing approval")
	}
	return nil
}

// ReadyPairing acknowledges that the joining device has durably committed its
// authenticated credential and encrypted local database. Normal sync remains
// blocked until the creator finalizes the roster.
func (client *Client) ReadyPairing(ctx context.Context, pairingID []byte, deviceToken string) (string, error) {
	if len(pairingID) != 16 || deviceToken == "" {
		return "", errors.New("pairing ID and device token are required")
	}
	var response struct {
		State string `json:"state"`
	}
	path := "/v1/pairings/" + hex.EncodeToString(pairingID) + "/ready"
	if err := client.doJSON(ctx, http.MethodPost, path, deviceToken, map[string]any{}, &response, http.StatusOK); err != nil {
		return "", err
	}
	if response.State != "ready" && response.State != "finalized" {
		return "", errors.New("sync relay returned invalid pairing readiness")
	}
	return response.State, nil
}

func (client *Client) FinalizePairing(ctx context.Context, pairingID []byte, creatorToken string) error {
	if len(pairingID) != 16 || creatorToken == "" {
		return errors.New("pairing ID and creator token are required")
	}
	var response struct {
		State string `json:"state"`
	}
	path := "/v1/pairings/" + hex.EncodeToString(pairingID) + "/finalize"
	if err := client.doJSON(ctx, http.MethodPost, path, creatorToken, map[string]any{}, &response, http.StatusOK); err != nil {
		return err
	}
	if response.State != "finalized" {
		return errors.New("sync relay returned invalid pairing finalization")
	}
	return nil
}

func (client *Client) CancelPairing(ctx context.Context, pairingID []byte, creatorToken string) error {
	if len(pairingID) != 16 || creatorToken == "" {
		return errors.New("pairing ID and creator token are required")
	}
	path := "/v1/pairings/" + hex.EncodeToString(pairingID)
	return client.doJSON(ctx, http.MethodDelete, path, creatorToken, nil, nil, http.StatusNoContent)
}

// SealPairingPackage authenticates the creator-side trust roster and encrypts
// the complete account package to the joining device. The relay receives only
// the resulting opaque box.
func SealPairingPackage(invitation PairingInvitation, transcript protocol.PairingTranscript,
	payload protocol.PairingPackage, creatorX25519Private []byte, source io.Reader) (protocol.SealedBox, error) {
	if err := validatePairingInvitation(invitation); err != nil ||
		!bytes.Equal(transcript.PairingID, invitation.PairingID) || !bytes.Equal(transcript.AccountID, invitation.AccountID) ||
		!bytes.Equal(transcript.CreatorDeviceID, invitation.CreatorDeviceID) ||
		!bytes.Equal(transcript.CreatorEd25519PublicKey, invitation.CreatorEd25519PublicKey) ||
		!bytes.Equal(transcript.CreatorX25519PublicKey, invitation.CreatorX25519PublicKey) {
		return protocol.SealedBox{}, errors.New("pairing transcript does not match the creator invitation")
	}
	return protocol.SealPairingPackage(creatorX25519Private, transcript.JoiningX25519PublicKey,
		invitation.PairingSecret, transcript, payload, source)
}

// OpenPairingClaim performs all joining-side identity checks before returning
// account key material. Callers must persist the returned signed roster (or its
// complete device keys and version) as the only verification trust source.
func OpenPairingClaim(invitation PairingInvitation, joining Account, transcript protocol.PairingTranscript,
	joiningX25519Private []byte, claim PairingClaim) (protocol.PairingPackage, error) {
	if err := validatePairingInvitation(invitation); err != nil || !sameAccount(joining, claim.Account) ||
		!bytes.Equal(claim.Account.AccountID, transcript.AccountID) || !bytes.Equal(claim.Account.DeviceID, transcript.JoiningDeviceID) ||
		!bytes.Equal(transcript.PairingID, invitation.PairingID) || !bytes.Equal(transcript.CreatorDeviceID, invitation.CreatorDeviceID) ||
		!bytes.Equal(transcript.CreatorEd25519PublicKey, invitation.CreatorEd25519PublicKey) ||
		!bytes.Equal(transcript.CreatorX25519PublicKey, invitation.CreatorX25519PublicKey) {
		return protocol.PairingPackage{}, errors.New("pairing claim identity does not match the authenticated transcript")
	}
	return protocol.OpenPairingPackage(joiningX25519Private, transcript.CreatorX25519PublicKey,
		invitation.PairingSecret, transcript, claim.EncryptedKeyring)
}

func (client *Client) ClaimPairing(ctx context.Context, invitation PairingInvitation, joining Account,
	transcript protocol.PairingTranscript, signingPrivate ed25519.PrivateKey) (PairingClaim, error) {
	if err := validatePairingInvitation(invitation); err != nil || validateProvisionedAccount(joining) != nil ||
		!bytes.Equal(joining.AccountID, invitation.AccountID) || !bytes.Equal(transcript.PairingID, invitation.PairingID) ||
		!bytes.Equal(transcript.AccountID, joining.AccountID) || !bytes.Equal(transcript.JoiningDeviceID, joining.DeviceID) ||
		!bytes.Equal(transcript.CreatorDeviceID, invitation.CreatorDeviceID) ||
		!bytes.Equal(transcript.CreatorEd25519PublicKey, invitation.CreatorEd25519PublicKey) ||
		!bytes.Equal(transcript.CreatorX25519PublicKey, invitation.CreatorX25519PublicKey) {
		return PairingClaim{}, errors.New("pairing ID and secret are invalid")
	}
	verifier, err := pairingVerifier(invitation)
	if err != nil {
		return PairingClaim{}, err
	}
	claimProof, err := protocol.PairingClaimProof(transcript, joining.DeviceToken, signingPrivate)
	if err != nil {
		return PairingClaim{}, err
	}
	var wire struct {
		accountWire
		EncryptedKeyring string `json:"encrypted_keyring"`
	}
	path := "/v1/pairings/" + hex.EncodeToString(invitation.PairingID) + "/claim"
	if err := client.doJSON(ctx, http.MethodPost, path, "", map[string]string{
		"pairing_verifier": base64.RawURLEncoding.EncodeToString(verifier),
		"device_token":     joining.DeviceToken,
		"claim_proof":      base64.RawURLEncoding.EncodeToString(claimProof),
	}, &wire, http.StatusCreated); err != nil {
		return PairingClaim{}, err
	}
	account, err := accountFromWire(wire.accountWire)
	if err != nil {
		return PairingClaim{}, err
	}
	if !sameAccount(account, joining) {
		return PairingClaim{}, errors.New("sync relay returned mismatched pairing identity")
	}
	box, err := protocol.DecodeSealedBox(wire.EncryptedKeyring)
	if err != nil {
		return PairingClaim{}, errors.New("sync relay returned invalid pairing keyring")
	}
	return PairingClaim{Account: account, EncryptedKeyring: box}, nil
}

func (client *Client) ListDevices(ctx context.Context, deviceToken string) ([]Device, error) {
	if deviceToken == "" {
		return nil, errors.New("device token is required")
	}
	var wire struct {
		Devices []struct {
			ID               string     `json:"id"`
			NameCiphertext   string     `json:"name_ciphertext"`
			Ed25519PublicKey string     `json:"ed25519_public_key"`
			X25519PublicKey  string     `json:"x25519_public_key"`
			CreatedAt        time.Time  `json:"created_at"`
			Current          bool       `json:"current"`
			Revoked          bool       `json:"revoked"`
			RevokedAt        *time.Time `json:"revoked_at,omitempty"`
		} `json:"devices"`
	}
	if err := client.doJSON(ctx, http.MethodGet, "/v1/devices", deviceToken, nil, &wire, http.StatusOK); err != nil {
		return nil, err
	}
	if len(wire.Devices) < 1 || len(wire.Devices) > 256 {
		return nil, errors.New("sync relay returned an invalid device roster size")
	}
	devices := make([]Device, 0, len(wire.Devices))
	currentCount := 0
	seen := make(map[string]struct{}, len(wire.Devices))
	for _, item := range wire.Devices {
		id, err := decodeHexID(item.ID)
		if err != nil || item.CreatedAt.IsZero() || (item.RevokedAt != nil) != item.Revoked {
			return nil, errors.New("sync relay returned invalid device metadata")
		}
		if _, exists := seen[item.ID]; exists {
			return nil, errors.New("sync relay returned a duplicate device")
		}
		seen[item.ID] = struct{}{}
		name, err := decodeBase64Size(item.NameCiphertext, 16, 512)
		if err != nil {
			return nil, err
		}
		edKey, err := decodeBase64Size(item.Ed25519PublicKey, ed25519.PublicKeySize, ed25519.PublicKeySize)
		if err != nil {
			return nil, err
		}
		xKey, err := decodeBase64Size(item.X25519PublicKey, 32, 32)
		if err != nil {
			return nil, err
		}
		if item.Current {
			currentCount++
		}
		devices = append(devices, Device{ID: id, NameCiphertext: name, Ed25519PublicKey: edKey,
			X25519PublicKey: xKey, CreatedAt: item.CreatedAt, Current: item.Current,
			Revoked: item.Revoked, RevokedAt: item.RevokedAt})
	}
	if currentCount != 1 {
		return nil, errors.New("sync relay returned an invalid current-device roster")
	}
	return devices, nil
}

type Keyring struct {
	Epoch          uint64
	Box            protocol.SealedBox
	WriterDeviceID string
	CreatedAt      time.Time
}

// PutKeyring stores one immutable recovery-encrypted epoch package. Encoding is
// delegated to protocol.EncodeSealedBox so malformed or non-canonical wire
// representations never leave the client.
func (client *Client) PutKeyring(ctx context.Context, token string, epoch uint64, box protocol.SealedBox) error {
	if token == "" || epoch < 1 || epoch > math.MaxInt64 {
		return errors.New("invalid keyring request")
	}
	encoded, err := protocol.EncodeSealedBox(box)
	if err != nil {
		return err
	}
	var response struct {
		Epoch uint64 `json:"epoch"`
	}
	if err := client.doJSON(ctx, http.MethodPut, "/v1/keyring", token, map[string]any{
		"epoch": epoch, "ciphertext": encoded,
	}, &response, http.StatusOK); err != nil {
		return err
	}
	if response.Epoch != epoch {
		return errors.New("sync relay returned the wrong keyring epoch")
	}
	return nil
}

// GetKeyrings strictly decodes the sealed-box wire and requires ordered,
// unique epochs. Opening the box remains a client-side recovery-key operation.
func (client *Client) GetKeyrings(ctx context.Context, token string) ([]Keyring, error) {
	if token == "" {
		return nil, errors.New("device token is required")
	}
	var response struct {
		Keyrings []struct {
			Epoch          uint64    `json:"epoch"`
			Ciphertext     string    `json:"ciphertext"`
			WriterDeviceID string    `json:"writer_device_id"`
			CreatedAt      time.Time `json:"created_at"`
		} `json:"keyrings"`
	}
	if err := client.doJSON(ctx, http.MethodGet, "/v1/keyring", token, nil, &response, http.StatusOK); err != nil {
		return nil, err
	}
	keyrings := make([]Keyring, 0, len(response.Keyrings))
	var previous uint64
	for _, wire := range response.Keyrings {
		if wire.Epoch < 1 || wire.Epoch > math.MaxInt64 || wire.Epoch <= previous || wire.CreatedAt.IsZero() {
			return nil, errors.New("sync relay returned invalid keyring metadata")
		}
		writer, err := hex.DecodeString(wire.WriterDeviceID)
		if err != nil || len(writer) != 16 || wire.WriterDeviceID != strings.ToLower(wire.WriterDeviceID) {
			return nil, errors.New("sync relay returned invalid keyring writer")
		}
		box, err := protocol.DecodeSealedBox(wire.Ciphertext)
		if err != nil {
			return nil, errors.New("sync relay returned invalid keyring ciphertext")
		}
		keyrings = append(keyrings, Keyring{
			Epoch: wire.Epoch, Box: box, WriterDeviceID: wire.WriterDeviceID, CreatedAt: wire.CreatedAt,
		})
		previous = wire.Epoch
	}
	return keyrings, nil
}

type SyncRequest struct {
	Cursor    int64                   `json:"cursor"`
	AckCursor int64                   `json:"ack_cursor"`
	Limit     int                     `json:"limit,omitempty"`
	Envelopes []protocol.WireEnvelope `json:"envelopes,omitempty"`
}

type SyncRejection struct {
	DeviceSequence uint64 `json:"device_seq"`
	Code           string `json:"code"`
}

type SyncResponse struct {
	AcceptedSequences []uint64                `json:"accepted_sequences"`
	RejectedSequences []SyncRejection         `json:"rejected_sequences"`
	Envelopes         []protocol.WireEnvelope `json:"envelopes"`
	NextCursor        int64                   `json:"next_cursor"`
	HasMore           bool                    `json:"has_more"`
	CurrentKeyEpoch   uint64                  `json:"current_key_epoch"`
}

func (client *Client) Sync(ctx context.Context, token string, request SyncRequest) (SyncResponse, error) {
	if token == "" || request.Cursor < 0 || request.AckCursor < 0 || request.AckCursor > request.Cursor ||
		request.Limit < 0 || request.Limit > 256 || len(request.Envelopes) > 256 {
		return SyncResponse{}, errors.New("invalid sync request")
	}
	var response SyncResponse
	if err := client.doJSON(ctx, http.MethodPost, "/v1/sync", token, request, &response, http.StatusOK); err != nil {
		return SyncResponse{}, err
	}
	if response.NextCursor < request.Cursor || len(response.Envelopes) > 256 {
		return SyncResponse{}, errors.New("sync relay returned an invalid cursor or page")
	}
	return response, nil
}
