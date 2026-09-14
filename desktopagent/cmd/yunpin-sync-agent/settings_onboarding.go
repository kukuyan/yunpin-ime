// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

// This optional interface leaves existing settings integrations compatible.
// Invitation is deliberately absent from state: it can only be returned by
// the initiating POST, never by a page reload, redirect, URL, or status query.
type settingsOnboardingOperations interface {
	OnboardingStatus(context.Context) (settingsOnboardingState, error)
	ApplyOnboarding(context.Context, settingsOnboardingAction) (settingsOnboardingResult, error)
}

type settingsOnboardingState struct {
	ServerConfigured  bool
	Server            string
	AllowPrivateHTTP  bool
	SessionValid      bool
	Username          string
	SessionExpires    time.Time
	CredentialPresent bool
	Paired            bool
	PairingPending    bool
	PairingRole       string
	PairingState      string
	BridgeConfigured  bool
}

type settingsOnboardingAction struct {
	Kind                string
	Server              string
	AllowPrivateHTTP    bool
	ConfirmServerChange bool
	Username            string
	Password            string
	Invitation          string
}

type settingsOnboardingResult struct {
	State      settingsOnboardingState
	Notice     string
	Invitation string `json:"-"`
}

// Errors crossing the UI boundary are fixed codes. Neither relay error text
// nor user input (including malformed invitation/password/URL) is retained.
type settingsOnboardingError struct{ Code string }

func (err *settingsOnboardingError) Error() string { return err.Code }

func onboardingError(code string) error { return &settingsOnboardingError{Code: code} }

func classifyOnboardingError(err error, fallback string) error {
	if err == nil {
		return nil
	}
	var safe *settingsOnboardingError
	if errors.As(err, &safe) {
		return safe
	}
	if errors.Is(err, desktopagent.ErrAlreadyRunning) || errors.Is(err, desktopagent.ErrRimeMaintenanceBusy) {
		return onboardingError("busy")
	}
	if errors.Is(err, desktopagent.ErrUserLoginRequired) {
		return onboardingError("session-required")
	}
	var api *syncclient.APIError
	if errors.As(err, &api) {
		switch api.Code {
		case "pairing_not_ready", "pairing_not_ready_to_finalize", "pairing_finalization_pending":
			return onboardingError("pairing-waiting")
		case "pairing_not_found", "pairing_ready_window_expired":
			return onboardingError("pairing-expired")
		}
	}
	return onboardingError(fallback)
}

type settingsOnboardingReadStore struct{ desktopagent.SecretStore }

func (store settingsOnboardingReadStore) Load(ctx context.Context, profile string) ([]byte, error) {
	if reader, ok := store.SecretStore.(interface {
		LoadWithoutUserInteraction(context.Context, string) ([]byte, error)
	}); ok {
		return reader.LoadWithoutUserInteraction(ctx, profile)
	}
	return nil, errors.New("non-interactive onboarding credential read is unavailable")
}

func (settingsOnboardingReadStore) Save(context.Context, string, []byte) error {
	return errors.New("onboarding status cannot save credentials")
}
func (settingsOnboardingReadStore) Delete(context.Context, string) error {
	return errors.New("onboarding status cannot delete credentials")
}

func (operations *localSettingsOperations) onboardingSecrets() desktopagent.SecretStore {
	if operations.secrets != nil {
		return operations.secrets.underlying
	}
	return operations.agent.Secrets
}

func (operations *localSettingsOperations) invalidateOnboardingSecrets() {
	if operations.secrets != nil {
		operations.secrets.mu.Lock()
		defer operations.secrets.mu.Unlock()
		operations.secrets.invalidateLocked()
	}
}

func (operations *localSettingsOperations) OnboardingStatus(ctx context.Context) (settingsOnboardingState, error) {
	var state settingsOnboardingState
	endpoint, err := syncclient.LoadEndpointConfig(operations.defaults.EndpointConfigPath)
	if err == nil {
		state.ServerConfigured, state.Server = true, endpoint.String()
		state.AllowPrivateHTTP = strings.HasPrefix(state.Server, "http://")
	} else if !errors.Is(err, os.ErrNotExist) {
		return state, onboardingError("server-invalid")
	}
	local, err := desktopagent.ReadOnboardingLocalStatus(ctx, settingsOnboardingReadStore{operations.onboardingSecrets()}, operations.agent.Profile, state.Server)
	state.SessionValid, state.Username, state.SessionExpires = local.SessionValid, local.Username, local.SessionExpires
	state.CredentialPresent, state.Paired = local.CredentialPresent, local.Paired
	state.PairingPending, state.PairingRole, state.PairingState = local.PairingPending, local.PairingRole, local.PairingState
	state.BridgeConfigured, _ = desktopagent.RimeBridgeConfigured(operations.defaults)
	return state, classifyOnboardingError(err, "local-state")
}

// checkOnboardingServer is anonymous even on paired installations. Redirects
// are disabled, so a health check can neither leak an existing bearer nor
// quietly validate a different service from the address the user supplied.
func checkOnboardingServer(ctx context.Context, endpoint syncclient.Endpoint) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String()+"/healthz", nil)
	if err != nil {
		return onboardingError("server-invalid")
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return onboardingError("server-unreachable")
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(contents) > 4096 || response.StatusCode != http.StatusOK {
		return onboardingError("server-unreachable")
	}
	var health struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(contents, &health) != nil || health.Status != "ok" {
		return onboardingError("server-unreachable")
	}
	return nil
}

func (operations *localSettingsOperations) applyOnboardingServer(ctx context.Context, action settingsOnboardingAction) (string, error) {
	endpoint, err := syncclient.ParseEndpoint(action.Server, syncclient.EndpointPolicy{AllowPrivateHTTP: action.AllowPrivateHTTP})
	if err != nil {
		return "", onboardingError("server-invalid")
	}
	if action.Kind == "server-check" {
		return "服务器可以连接；地址尚未保存。", checkOnboardingServer(ctx, endpoint)
	}
	previous, err := syncclient.LoadEndpointConfig(operations.defaults.EndpointConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", onboardingError("server-invalid")
	}
	local, err := desktopagent.ReadOnboardingLocalStatus(ctx, operations.onboardingSecrets(), operations.agent.Profile, "")
	if err != nil {
		return "", onboardingError("local-state")
	}
	if endpoint.String() != previous.String() {
		if local.PairingPending {
			return "", onboardingError("pairing-pending")
		}
		if local.CredentialPresent {
			if !action.ConfirmServerChange {
				return "", onboardingError("server-confirm-required")
			}
			if !local.Paired {
				return "", onboardingError("server-identity-mismatch")
			}
			if err := desktopagent.VerifyOnboardingServer(ctx, syncclient.New(endpoint), operations.onboardingSecrets(), operations.agent.Profile); err != nil {
				return "", onboardingError("server-identity-mismatch")
			}
		}
	}
	if err := checkOnboardingServer(ctx, endpoint); err != nil {
		return "", err
	}
	if err := desktopagent.ConfigureEndpoint(operations.defaults.EndpointConfigPath, endpoint.String(), action.AllowPrivateHTTP); err != nil {
		return "", onboardingError("local-state")
	}
	return "服务器地址已保存。已配对的设备继续使用原有身份。", nil
}

func (operations *localSettingsOperations) applyOnboardingLogin(ctx context.Context, client *syncclient.Client, endpoint syncclient.Endpoint, action settingsOnboardingAction) (string, error) {
	secrets := operations.onboardingSecrets()
	username := strings.TrimSpace(action.Username)
	session, err := desktopagent.LoadUserSession(ctx, secrets, operations.agent.Profile, endpoint.String())
	if err == nil && (username == "" || username == session.Username) {
		session.Token = ""
		return "已复用本机现有登录会话。", nil
	}
	if username == "" || len(username) > 64 || action.Password == "" || len(action.Password) > 4096 {
		return "", onboardingError("login-failed")
	}
	if err != nil && !errors.Is(err, desktopagent.ErrUserLoginRequired) {
		return "", onboardingError("local-state")
	}
	if action.Kind == "register" {
		_, err = desktopagent.RegisterUser(ctx, client, secrets, operations.agent.Profile, endpoint.String(), username, action.Password)
	} else {
		_, err = desktopagent.LoginUser(ctx, client, secrets, operations.agent.Profile, endpoint.String(), username, action.Password)
	}
	if err != nil {
		return "", classifyOnboardingError(err, "login-failed")
	}
	return "账号管理登录已保存；设备同步继续使用独立的已配对身份。", nil
}

func (operations *localSettingsOperations) applyOnboardingPairing(ctx context.Context, client *syncclient.Client, action settingsOnboardingAction) (desktopagent.PairingResult, error) {
	options := desktopagent.PairingOptions{Secrets: operations.onboardingSecrets(), Profile: operations.agent.Profile, DatabasePath: operations.defaults.DatabasePath, Random: rand.Reader}
	state, err := desktopagent.ReadOnboardingLocalStatus(ctx, options.Secrets, options.Profile, "")
	if err != nil {
		return desktopagent.PairingResult{}, onboardingError("local-state")
	}
	kind := action.Kind
	if kind == "pairing-continue" {
		switch state.PairingRole {
		case "creator":
			switch state.PairingState {
			case "cancel_pending":
				kind = "pairing-cancel"
			case "invited":
				kind = "pairing-approve"
			default:
				kind = "pairing-finalize"
			}
		case "joiner":
			if state.PairingState == "rollback_pending" {
				kind = "pairing-abort"
			} else {
				if state.PairingState == "joined" {
					if _, err := desktopagent.JoinPairing(ctx, client, options, ""); err != nil {
						// A journal's join may already be beyond the joinable state;
						// the original claim operation is the supported continuation.
						var api *syncclient.APIError
						if !errors.As(err, &api) || api.Code != "pairing_not_joinable" {
							return desktopagent.PairingResult{}, err
						}
					}
				}
				kind = "pairing-claim"
			}
		default:
			return desktopagent.PairingResult{}, onboardingError("pairing-pending")
		}
	}
	if kind == "pairing-cancel" && state.PairingRole == "joiner" {
		kind = "pairing-abort"
	}
	switch kind {
	case "pairing-invite":
		if !state.CredentialPresent {
			return desktopagent.PairingResult{}, onboardingError("device-required")
		}
		return desktopagent.StartPairing(ctx, client, options)
	case "pairing-join":
		if state.CredentialPresent && state.PairingRole != "joiner" {
			return desktopagent.PairingResult{}, onboardingError("pairing-pending")
		}
		return desktopagent.JoinPairing(ctx, client, options, action.Invitation)
	case "pairing-approve":
		if state.PairingRole == "creator" && state.PairingState == "invited" {
			remoteState, expired, err := desktopagent.ReadCreatorOnboardingPairingState(ctx, client, options)
			if err != nil {
				return desktopagent.PairingResult{}, err
			}
			if expired {
				return desktopagent.PairingResult{}, onboardingError("pairing-expired")
			}
			if remoteState == "created" {
				return desktopagent.PairingResult{}, onboardingError("pairing-waiting")
			}
		}
		return desktopagent.ApprovePairing(ctx, client, options)
	case "pairing-claim":
		return desktopagent.ClaimPairing(ctx, client, options)
	case "pairing-finalize":
		return desktopagent.FinalizePairing(ctx, client, options)
	case "pairing-cancel":
		return desktopagent.CancelCreatorPairing(ctx, client, options)
	case "pairing-abort":
		return desktopagent.AbortJoiningPairing(ctx, client, options)
	default:
		return desktopagent.PairingResult{}, onboardingError("pairing-failed")
	}
}

func (operations *localSettingsOperations) ApplyOnboarding(ctx context.Context, action settingsOnboardingAction) (settingsOnboardingResult, error) {
	var result settingsOnboardingResult
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// No credential or invitation is copied into long-lived operations fields.
	defer func() { action.Password, action.Invitation = "", "" }()
	if len(action.Server) > 4096 || len(action.Invitation) > 4096 {
		return result, onboardingError("invalid-input")
	}
	if action.Kind == "server-check" {
		var err error
		result.Notice, err = operations.applyOnboardingServer(ctx, action)
		if err != nil {
			return result, err
		}
		result.State, _ = operations.OnboardingStatus(ctx)
		return result, nil
	}
	err := desktopagent.WithProcessLock(operations.defaults.LockPath, func() error {
		operations.invalidateOnboardingSecrets()
		defer operations.invalidateOnboardingSecrets()
		if action.Kind == "server-save" {
			var err error
			result.Notice, err = operations.applyOnboardingServer(ctx, action)
			return err
		}
		if action.Kind == "prepare-bridge" {
			paths, err := desktopagent.DefaultRimeBridgePaths(operations.defaults)
			if err != nil {
				return onboardingError("local-state")
			}
			if err := desktopagent.ConfigureRimeBridge(paths); err != nil {
				return onboardingError("local-state")
			}
			result.Notice = "本机词库同步桥接已配置。"
			return nil
		}
		endpoint, err := syncclient.LoadEndpointConfig(operations.defaults.EndpointConfigPath)
		if err != nil {
			return onboardingError("server-invalid")
		}
		client := syncclient.New(endpoint)
		switch action.Kind {
		case "login", "register":
			result.Notice, err = operations.applyOnboardingLogin(ctx, client, endpoint, action)
			return err
		case "claim-account":
			session, err := desktopagent.LoadUserSession(ctx, operations.onboardingSecrets(), operations.agent.Profile, endpoint.String())
			if err != nil {
				return classifyOnboardingError(err, "session-required")
			}
			defer func() { session.Token = "" }()
			client = syncclient.New(endpoint, syncclient.WithUserSession(session.Token))
			if err := desktopagent.ClaimCurrentAccount(ctx, client, operations.onboardingSecrets(), operations.agent.Profile, endpoint.String()); err != nil {
				return classifyOnboardingError(err, "login-failed")
			}
			result.Notice = "现有设备账号已关联至本次登录。"
			return nil
		case "pairing-invite", "pairing-join", "pairing-approve", "pairing-claim", "pairing-finalize", "pairing-cancel", "pairing-abort", "pairing-continue":
			pairing, err := operations.applyOnboardingPairing(ctx, client, action)
			if err != nil {
				return classifyOnboardingError(err, "pairing-failed")
			}
			result.Invitation = pairing.Invitation
			switch pairing.State {
			case "ready":
				result.Notice = "本机配对步骤已完成；可继续导入与同步验收。"
			case "claim_pending", "finalize_pending":
				result.Notice = "本机进度已保存；请在另一台设备完成相应步骤，再继续。"
			case "expired", "rolled_back", "aborted", "cancelled":
				result.Notice = "本次配对已结束。原有设备信任保留。"
			default:
				result.Notice = "配对进度已保存，请按界面提示继续。"
			}
			return nil
		default:
			return onboardingError("invalid-input")
		}
	})
	if err != nil {
		return settingsOnboardingResult{}, classifyOnboardingError(err, "local-state")
	}
	result.State, err = operations.OnboardingStatus(ctx)
	// A committed action must not be reported as failed merely because an OS
	// store refuses an optional non-interactive status read after the operation.
	if err != nil {
		result.Notice += " 请刷新页面查看最新状态。"
	}
	return result, nil
}
