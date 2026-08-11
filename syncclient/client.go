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
	endpoint Endpoint
	http     *http.Client
}

type Option func(*http.Client)

func WithTransport(transport http.RoundTripper) Option {
	return func(client *http.Client) { client.Transport = transport }
}

func WithTimeout(timeout time.Duration) Option {
	return func(client *http.Client) { client.Timeout = timeout }
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
}

type registrationWire struct {
	RecoveryAuthentication string `json:"recovery_authentication"`
	DeviceNameCiphertext   string `json:"device_name_ciphertext"`
	Ed25519PublicKey       string `json:"ed25519_public_key"`
	X25519PublicKey        string `json:"x25519_public_key"`
}

func registrationToWire(registration AccountRegistration) (registrationWire, error) {
	if len(registration.RecoveryAuthentication) != 32 || len(registration.DeviceNameCiphertext) < 16 ||
		len(registration.DeviceNameCiphertext) > 512 || len(registration.Ed25519PublicKey) != ed25519.PublicKeySize ||
		len(registration.X25519PublicKey) != 32 {
		return registrationWire{}, errors.New("invalid account registration material")
	}
	encode := base64.RawURLEncoding.EncodeToString
	return registrationWire{
		RecoveryAuthentication: encode(registration.RecoveryAuthentication),
		DeviceNameCiphertext:   encode(registration.DeviceNameCiphertext),
		Ed25519PublicKey:       encode(registration.Ed25519PublicKey),
		X25519PublicKey:        encode(registration.X25519PublicKey),
	}, nil
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

func (client *Client) CreateAccount(ctx context.Context, registration AccountRegistration) (Account, error) {
	wire, err := registrationToWire(registration)
	if err != nil {
		return Account{}, err
	}
	var response accountWire
	if err := client.doJSON(ctx, http.MethodPost, "/v1/accounts", "", wire, &response, http.StatusCreated); err != nil {
		return Account{}, err
	}
	return accountFromWire(response)
}

func (client *Client) RecoverAccount(ctx context.Context, accountID []byte, registration AccountRegistration) (Account, error) {
	if len(accountID) != 16 {
		return Account{}, errors.New("account ID must be 16 bytes")
	}
	wire, err := registrationToWire(registration)
	if err != nil {
		return Account{}, err
	}
	var response accountWire
	path := "/v1/accounts/" + hex.EncodeToString(accountID) + "/recover"
	if err := client.doJSON(ctx, http.MethodPost, path, "", wire, &response, http.StatusCreated); err != nil {
		return Account{}, err
	}
	return accountFromWire(response)
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
