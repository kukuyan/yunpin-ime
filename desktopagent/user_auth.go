// SPDX-License-Identifier: Apache-2.0

package desktopagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/kukuyan/yunpin-ime/syncclient"
)

const (
	userSessionProfileSuffix = ".user-session"
	userSessionVersion       = 1
)

var ErrUserLoginRequired = errors.New("YunPin login is required for this account-management operation")

type userSessionRecord struct {
	Version  uint8  `json:"version"`
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
	Token    string `json:"token"`
	Expires  int64  `json:"expires_at_unix_ms"`
}

type UserLoginResult struct {
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}

func userSessionProfile(profile string) (string, error) {
	if err := validateProfile(profile); err != nil {
		return "", err
	}
	if len(profile)+len(userSessionProfileSuffix) > 64 {
		return "", errors.New("profile is too long for user session")
	}
	return profile + userSessionProfileSuffix, nil
}

func validateUserSession(record userSessionRecord, endpoint string, now time.Time) error {
	if record.Version != userSessionVersion || record.Endpoint != endpoint || record.Username == "" ||
		len(record.Username) > 64 || !validCanonicalToken(record.Token) || record.Expires <= now.UnixMilli() {
		return ErrUserLoginRequired
	}
	return nil
}

func saveUserSession(ctx context.Context, secrets SecretStore, profile, endpoint string, session syncclient.UserSession) (UserLoginResult, error) {
	secretProfile, err := userSessionProfile(profile)
	if err != nil {
		return UserLoginResult{}, err
	}
	record := userSessionRecord{
		Version: userSessionVersion, Endpoint: endpoint, Username: session.Username, Token: session.Token,
		Expires: session.ExpiresAt.UTC().UnixMilli(),
	}
	if err := validateUserSession(record, endpoint, time.Now()); err != nil {
		return UserLoginResult{}, err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return UserLoginResult{}, err
	}
	defer zeroBytes(encoded)
	if err := secrets.Save(ctx, secretProfile, encoded); err != nil {
		return UserLoginResult{}, fmt.Errorf("store YunPin login session: %w", err)
	}
	return UserLoginResult{Username: session.Username, ExpiresAt: session.ExpiresAt.UTC()}, nil
}

func LoadUserSession(ctx context.Context, secrets SecretStore, profile, endpoint string) (syncclient.UserSession, error) {
	secretProfile, err := userSessionProfile(profile)
	if err != nil {
		return syncclient.UserSession{}, err
	}
	encoded, err := secrets.Load(ctx, secretProfile)
	if errors.Is(err, ErrSecretNotFound) {
		return syncclient.UserSession{}, ErrUserLoginRequired
	}
	if err != nil {
		return syncclient.UserSession{}, fmt.Errorf("load YunPin login session: %w", err)
	}
	defer zeroBytes(encoded)
	var record userSessionRecord
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return syncclient.UserSession{}, ErrUserLoginRequired
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return syncclient.UserSession{}, ErrUserLoginRequired
	}
	if err := validateUserSession(record, endpoint, time.Now()); err != nil {
		return syncclient.UserSession{}, err
	}
	return syncclient.UserSession{Username: record.Username, Token: record.Token, ExpiresAt: time.UnixMilli(record.Expires).UTC()}, nil
}

func RegisterUser(ctx context.Context, client *syncclient.Client, secrets SecretStore, profile, endpoint, username, password string) (UserLoginResult, error) {
	session, err := client.Register(ctx, username, password)
	if err != nil {
		return UserLoginResult{}, err
	}
	return saveUserSession(ctx, secrets, profile, endpoint, session)
}

func LoginUser(ctx context.Context, client *syncclient.Client, secrets SecretStore, profile, endpoint, username, password string) (UserLoginResult, error) {
	session, err := client.Login(ctx, username, password)
	if err != nil {
		return UserLoginResult{}, err
	}
	return saveUserSession(ctx, secrets, profile, endpoint, session)
}

func LogoutUser(ctx context.Context, client *syncclient.Client, secrets SecretStore, profile, endpoint string) error {
	session, err := LoadUserSession(ctx, secrets, profile, endpoint)
	if err != nil {
		return err
	}
	if err := client.Logout(ctx, session.Token); err != nil {
		var api *syncclient.APIError
		if !errors.As(err, &api) || api.Status != 401 {
			return err
		}
	}
	secretProfile, err := userSessionProfile(profile)
	if err != nil {
		return err
	}
	if err := secrets.Delete(ctx, secretProfile); err != nil {
		return fmt.Errorf("delete YunPin login session: %w", err)
	}
	return nil
}

// ClaimCurrentAccount binds the local credential's account to the current
// user session using the device already held in the OS secret store.
func ClaimCurrentAccount(ctx context.Context, client *syncclient.Client, secrets SecretStore, profile, endpoint string) error {
	if _, err := LoadUserSession(ctx, secrets, profile, endpoint); err != nil {
		return err
	}
	active, err := secrets.Load(ctx, profile)
	if err != nil {
		return fmt.Errorf("load local YunPin credential: %w", err)
	}
	defer zeroBytes(active)
	bundle, err := DecodeCredentialBundle(active)
	if err != nil {
		return err
	}
	defer bundle.Zero()
	// Credential bundles persist the device token in its canonical textual
	// representation. ClaimAccount deliberately receives the 32 raw bytes and
	// encodes them once for the wire; passing the stored text directly made every
	// valid existing-device claim fail its length check before reaching the relay.
	if !validCanonicalToken(string(bundle.DeviceToken)) {
		return errors.New("local YunPin credential has an invalid device token")
	}
	deviceToken, err := base64.RawURLEncoding.DecodeString(string(bundle.DeviceToken))
	if err != nil {
		return errors.New("local YunPin credential has an invalid device token")
	}
	defer zeroBytes(deviceToken)
	return client.ClaimAccount(ctx, bundle.AccountID[:], bundle.DeviceID[:], deviceToken)
}
