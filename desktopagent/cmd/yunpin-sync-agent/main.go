// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/syncclient"
	"golang.org/x/term"
)

const savedRecoveryConfirmation = "SAVED"

type commonFlags struct {
	profile       string
	state         string
	endpoint      string
	database      string
	lock          string
	service       string
	nativeEvents  string
	rimeUserDB    string
	baseline      string
	snapshot      string
	snapshotState string
}

func addCommonFlags(set *flag.FlagSet, defaults desktopagent.Paths) *commonFlags {
	flags := &commonFlags{}
	set.StringVar(&flags.profile, "profile", desktopagent.DefaultProfile, "local credential profile")
	set.StringVar(&flags.state, "state-dir", defaults.StateDirectory, "private local state directory")
	set.StringVar(&flags.endpoint, "endpoint-config", defaults.EndpointConfigPath, "non-secret endpoint JSON")
	set.StringVar(&flags.database, "database", defaults.DatabasePath, "encrypted local SQLite database")
	set.StringVar(&flags.lock, "lock", defaults.LockPath, "single-instance lock file")
	set.StringVar(&flags.service, "credential-service", defaults.CredentialService, "OS credential service identifier")
	set.StringVar(&flags.nativeEvents, "native-events", defaults.NativeEventsPath, "native selection event spool")
	set.StringVar(&flags.rimeUserDB, "rime-userdb-export", "", "private Rime userdb snapshot produced by the fixed platform helper")
	set.StringVar(&flags.baseline, "baseline", defaults.BaselinePath, "static private vocabulary baseline")
	set.StringVar(&flags.snapshot, "snapshot", defaults.SnapshotPath, "generated immutable private snapshot")
	set.StringVar(&flags.snapshotState, "snapshot-state", defaults.SnapshotStatePath, "last successfully reloaded snapshot marker")
	return flags
}

func (flags commonFlags) validate() error {
	for label, path := range map[string]string{
		"state directory": flags.state, "endpoint config": flags.endpoint,
		"database": flags.database, "lock": flags.lock,
		"native events": flags.nativeEvents, "baseline": flags.baseline, "snapshot": flags.snapshot,
		"snapshot state": flags.snapshotState,
	} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s path must be absolute", label)
		}
	}
	if flags.rimeUserDB != "" && !filepath.IsAbs(flags.rimeUserDB) {
		return errors.New("Rime userdb export path must be absolute")
	}
	return nil
}

func (flags commonFlags) components() (desktopagent.SecretStore, desktopagent.Agent, error) {
	if err := flags.validate(); err != nil {
		return nil, desktopagent.Agent{}, err
	}
	secrets, err := desktopagent.NewPlatformSecretStore(desktopagent.PlatformSecretStoreOptions{
		Service: flags.service, Directory: flags.state,
	})
	if err != nil {
		return nil, desktopagent.Agent{}, err
	}
	agent := desktopagent.Agent{
		Secrets: secrets, Profile: flags.profile, StateDirectory: flags.state, EndpointConfigPath: flags.endpoint,
		DatabasePath: flags.database, NativeEventsPath: flags.nativeEvents,
		RimeUserDBExportPath: flags.rimeUserDB,
		BaselinePath:         flags.baseline, SnapshotPath: flags.snapshot, SnapshotStatePath: flags.snapshotState,
		Reload: desktopagent.DefaultReloadHook(),
	}
	return secrets, agent, nil
}

func parse(set *flag.FlagSet, arguments []string) error {
	set.SetOutput(os.Stderr)
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional argument")
	}
	return nil
}

func parseRedacted(set *flag.FlagSet, arguments []string) error {
	set.SetOutput(io.Discard)
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 {
		return errors.New("resident readiness check failed")
	}
	return nil
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func readSecretPrompt(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("interactive terminal input is required; do not pass passwords or recovery keys as command arguments")
	}
	_, _ = fmt.Fprint(os.Stderr, label)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", errors.New("could not read private terminal input")
	}
	defer clear(value)
	if len(value) == 0 {
		return "", errors.New("private terminal input cannot be empty")
	}
	return string(value), nil
}

func commandRegister(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("register", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	username := set.String("username", "", "self-hosted YunPin username")
	if err := parse(set, arguments); err != nil {
		return err
	}
	password, err := readSecretPrompt("YunPin password: ")
	if err != nil {
		return err
	}
	confirm, err := readSecretPrompt("Repeat YunPin password: ")
	if err != nil {
		return err
	}
	if password != confirm {
		return errors.New("password confirmation does not match")
	}
	secrets, _, err := common.components()
	if err != nil {
		return err
	}
	endpoint, err := syncclient.LoadEndpointConfig(common.endpoint)
	if err != nil {
		return err
	}
	var result desktopagent.UserLoginResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var registerErr error
		result, registerErr = desktopagent.RegisterUser(ctx, syncclient.New(endpoint), secrets, common.profile, endpoint.String(), *username, password)
		return registerErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func commandLogin(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("login", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	username := set.String("username", "", "self-hosted YunPin username")
	if err := parse(set, arguments); err != nil {
		return err
	}
	password, err := readSecretPrompt("YunPin password: ")
	if err != nil {
		return err
	}
	secrets, _, err := common.components()
	if err != nil {
		return err
	}
	endpoint, err := syncclient.LoadEndpointConfig(common.endpoint)
	if err != nil {
		return err
	}
	var result desktopagent.UserLoginResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var loginErr error
		result, loginErr = desktopagent.LoginUser(ctx, syncclient.New(endpoint), secrets, common.profile, endpoint.String(), *username, password)
		return loginErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func commandLogout(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("logout", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	if err := parse(set, arguments); err != nil {
		return err
	}
	secrets, _, err := common.components()
	if err != nil {
		return err
	}
	endpoint, err := syncclient.LoadEndpointConfig(common.endpoint)
	if err != nil {
		return err
	}
	return desktopagent.WithProcessLock(common.lock, func() error {
		return desktopagent.LogoutUser(ctx, syncclient.New(endpoint), secrets, common.profile, endpoint.String())
	})
}

func commandClaimAccount(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("claim-account", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	confirm := set.Bool("confirm-claim-existing-account", false, "confirm binding this local account to the current YunPin login")
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("claim-account requires --confirm-claim-existing-account")
	}
	secrets, _, err := common.components()
	if err != nil {
		return err
	}
	endpoint, err := syncclient.LoadEndpointConfig(common.endpoint)
	if err != nil {
		return err
	}
	session, err := desktopagent.LoadUserSession(ctx, secrets, common.profile, endpoint.String())
	if err != nil {
		return err
	}
	if err := desktopagent.WithProcessLock(common.lock, func() error {
		return desktopagent.ClaimCurrentAccount(ctx, syncclient.New(endpoint, syncclient.WithUserSession(session.Token)), secrets, common.profile, endpoint.String())
	}); err != nil {
		return err
	}
	return writeJSON(map[string]bool{"claimed": true})
}

type installProbeResult struct {
	Ready bool `json:"ready"`
}

// commandInstallProbe proves only that the staged executable can start and
// parse its own command. It deliberately has no context, platform paths,
// SecretStore, endpoint, database, dictionary, or network dependency.
func commandInstallProbe(arguments []string) error {
	set := flag.NewFlagSet("install-probe", flag.ContinueOnError)
	if err := parse(set, arguments); err != nil {
		return err
	}
	return writeJSON(installProbeResult{Ready: true})
}

func displayAndConfirmRecovery(ctx context.Context, result desktopagent.PrepareAccountResult) error {
	if err := writeJSON(result); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "Save both values now, then type %s and press Enter: ", savedRecoveryConfirmation)
	line, err := bufio.NewReader(io.LimitReader(os.Stdin, 64)).ReadString('\n')
	if err != nil {
		return errors.New("saved-key confirmation was not received")
	}
	if strings.TrimSpace(line) != savedRecoveryConfirmation {
		return errors.New("recovery key was displayed but not confirmed saved; no pending account was persisted")
	}
	return nil
}

func commandStatus(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("status", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	if err := parse(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
	status, err := agent.Status(ctx)
	if err != nil {
		return err
	}
	return writeJSON(status)
}

func commandResidentReady(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("resident-ready", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	if err := parseRedacted(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return errors.New("resident readiness check failed")
	}
	var readiness desktopagent.ResidentReadiness
	if err := desktopagent.WithProcessLock(common.lock, func() error {
		var readyErr error
		readiness, readyErr = agent.ResidentReady(ctx)
		return readyErr
	}); err != nil {
		return errors.New("resident readiness check failed")
	}
	return writeJSON(readiness)
}

func commandConfigure(defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("configure", flag.ContinueOnError)
	path := set.String("endpoint-config", defaults.EndpointConfigPath, "non-secret endpoint JSON")
	endpoint := set.String("endpoint", "", "absolute YunPin relay HTTP(S) endpoint")
	allowPrivateHTTP := set.Bool("allow-private-http", false, "explicitly permit HTTP to localhost/private IP")
	if err := parse(set, arguments); err != nil {
		return err
	}
	return desktopagent.ConfigureEndpoint(*path, *endpoint, *allowPrivateHTTP)
}

func commandConfigureRimeBridge(defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("configure-rime-bridge", flag.ContinueOnError)
	confirm := set.Bool("confirm", false, "confirm the private backup and local Rime sync_dir update")
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("configure-rime-bridge requires --confirm")
	}
	paths, err := desktopagent.DefaultRimeBridgePaths(defaults)
	if err != nil {
		return err
	}
	if err := desktopagent.WithProcessLock(defaults.LockPath, func() error {
		return desktopagent.ConfigureRimeBridge(paths)
	}); err != nil {
		return err
	}
	return writeJSON(map[string]bool{"ready": true})
}

func commandSyncOnce(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("sync-once", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	if err := parse(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
	var summary desktopagent.SyncSummary
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var syncErr error
		summary, syncErr = agent.SyncOnce(ctx)
		return syncErr
	})
	if err != nil {
		return err
	}
	return writeJSON(summary)
}

func commandPrepareAccount(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("prepare-account", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	confirm := set.Bool(
		"confirm-display-recovery-key", false,
		"confirm that the one-time recovery key and account ID may be displayed on stdout",
	)
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("prepare-account requires --confirm-display-recovery-key")
	}
	secrets, _, err := common.components()
	if err != nil {
		return err
	}
	var result desktopagent.PrepareAccountResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var prepareErr error
		result, prepareErr = desktopagent.PrepareAccount(ctx, desktopagent.InitAccountOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database, Random: rand.Reader,
			ConfirmRecoverySaved: displayAndConfirmRecovery,
		})
		return prepareErr
	})
	if err != nil {
		return err
	}
	return writeJSON(map[string]string{"account_id": result.AccountIDHex, "state": "prepared"})
}

func commandInitAccount(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("init-account", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	confirm := set.Bool(
		"confirm-saved-recovery-key", false,
		"confirm the prepare-account recovery key and account ID were saved before any network request",
	)
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("init-account requires --confirm-saved-recovery-key before any network request")
	}
	secrets, _, err := common.components()
	if err != nil {
		return err
	}
	endpoint, err := syncclient.LoadEndpointConfig(common.endpoint)
	if err != nil {
		return err
	}
	session, err := desktopagent.LoadUserSession(ctx, secrets, common.profile, endpoint.String())
	if err != nil {
		return err
	}
	var result desktopagent.InitAccountResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var initErr error
		result, initErr = desktopagent.InitAccount(ctx, syncclient.New(endpoint, syncclient.WithUserSession(session.Token)), desktopagent.InitAccountOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database,
		})
		return initErr
	})
	if err != nil {
		if errors.Is(err, desktopagent.ErrAccountSealPending) {
			if writeErr := writeJSON(result); writeErr != nil {
				return errors.Join(err, writeErr)
			}
		}
		return err
	}
	return writeJSON(result)
}

func commandAbortAccount(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("abort-account", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	confirm := set.Bool("confirm-abort-unsealed-account", false, "confirm rollback of the pending unsealed account")
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("abort-account requires --confirm-abort-unsealed-account")
	}
	secrets, _, err := common.components()
	if err != nil {
		return err
	}
	endpoint, err := syncclient.LoadEndpointConfig(common.endpoint)
	if err != nil {
		return err
	}
	var result desktopagent.InitAccountResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var abortErr error
		result, abortErr = desktopagent.AbortAccount(ctx, syncclient.New(endpoint), desktopagent.InitAccountOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database,
		})
		return abortErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func pairingComponents(ctx context.Context, common *commonFlags) (desktopagent.SecretStore, *syncclient.Client, error) {
	secrets, _, err := common.components()
	if err != nil {
		return nil, nil, err
	}
	endpoint, err := syncclient.LoadEndpointConfig(common.endpoint)
	if err != nil {
		return nil, nil, err
	}
	return secrets, syncclient.New(endpoint), nil
}

func commandPairingInvite(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("pairing-invite", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	confirm := set.Bool("confirm-display-invitation", false, "confirm that a one-time secret pairing invitation may be displayed on stdout")
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("pairing-invite requires --confirm-display-invitation")
	}
	secrets, client, err := pairingComponents(ctx, common)
	if err != nil {
		return err
	}
	var result desktopagent.PairingResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var pairErr error
		result, pairErr = desktopagent.StartPairing(ctx, client, desktopagent.PairingOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database, Random: rand.Reader,
		})
		return pairErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func commandPairingApprove(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("pairing-approve", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	if err := parse(set, arguments); err != nil {
		return err
	}
	secrets, client, err := pairingComponents(ctx, common)
	if err != nil {
		return err
	}
	var result desktopagent.PairingResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var pairErr error
		result, pairErr = desktopagent.ApprovePairing(ctx, client, desktopagent.PairingOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database, Random: rand.Reader,
		})
		return pairErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func commandPairingFinalize(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("pairing-finalize", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	if err := parse(set, arguments); err != nil {
		return err
	}
	secrets, client, err := pairingComponents(ctx, common)
	if err != nil {
		return err
	}
	var result desktopagent.PairingResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var pairErr error
		result, pairErr = desktopagent.FinalizePairing(ctx, client, desktopagent.PairingOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database,
		})
		return pairErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func readPairingInvitation(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path != "-" {
		return desktopagent.ReadPrivatePairingInvitation(path)
	}
	contents, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil || len(contents) > 4096 {
		return "", errors.New("pairing invitation on stdin exceeds size limit")
	}
	text := strings.TrimSpace(string(contents))
	if _, err := desktopagent.DecodePairingInvitation(text); err != nil {
		return "", err
	}
	return text, nil
}

func commandPairingJoin(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("pairing-join", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	invitationFile := set.String("invitation-file", "", "absolute private invitation file, or - for stdin; omit only to resume")
	if err := parse(set, arguments); err != nil {
		return err
	}
	invitation, err := readPairingInvitation(*invitationFile)
	if err != nil {
		return err
	}
	secrets, client, err := pairingComponents(ctx, common)
	if err != nil {
		return err
	}
	var result desktopagent.PairingResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var pairErr error
		result, pairErr = desktopagent.JoinPairing(ctx, client, desktopagent.PairingOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database, Random: rand.Reader,
		}, invitation)
		return pairErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func commandPairingClaim(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("pairing-claim", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	if err := parse(set, arguments); err != nil {
		return err
	}
	secrets, client, err := pairingComponents(ctx, common)
	if err != nil {
		return err
	}
	var result desktopagent.PairingResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var pairErr error
		result, pairErr = desktopagent.ClaimPairing(ctx, client, desktopagent.PairingOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database,
		})
		return pairErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func commandRun(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	interval := set.Duration("interval", time.Minute, "successful synchronization interval")
	if err := parse(set, arguments); err != nil {
		return err
	}
	if common.rimeUserDB != "" {
		return errors.New("run does not accept a static Rime userdb export; use the configured fixed maintenance bridge")
	}
	if filepath.Clean(common.state) != filepath.Clean(defaults.StateDirectory) {
		return errors.New("run requires the fixed platform state directory for the Rime maintenance acknowledgement")
	}
	if filepath.Clean(common.lock) != filepath.Clean(defaults.LockPath) {
		return errors.New("run requires the fixed platform process lock")
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
	bridgePaths, err := desktopagent.DefaultRimeBridgePaths(defaults)
	if err != nil {
		return err
	}
	refresh, err := desktopagent.NewDefaultRimeUserDBRefresh(bridgePaths)
	if err != nil {
		return err
	}
	agent.RimeUserDBExportPath = bridgePaths.StagingPath
	agent.RimeUserDBRefresh = refresh
	return agent.Run(ctx, desktopagent.RunOptions{
		LockPath: common.lock, Interval: *interval,
		OnEvent: func(event desktopagent.RunEvent) {
			// Events deliberately contain only a stable code and numeric summary.
			encoded, _ := json.Marshal(event)
			_, _ = fmt.Fprintln(os.Stderr, string(encoded))
		},
	})
}

func run(ctx context.Context, arguments []string) error {
	if len(arguments) < 1 {
		return errors.New("usage: yunpin-sync-agent <install-probe|configure|configure-server|configure-rime-bridge|register|login|logout|claim-account|prepare-account|init-account|abort-account|sync-once|run|status|resident-ready> [options]")
	}
	// Keep the package/install health probe ahead of DefaultPaths: even a broken
	// or unavailable user state root must not make binary installation look like
	// account provisioning, nor cause credentials or vocabulary to be read.
	if arguments[0] == "install-probe" {
		return commandInstallProbe(arguments[1:])
	}
	defaults, err := desktopagent.DefaultPaths()
	if err != nil {
		if arguments[0] == "resident-ready" {
			return errors.New("resident readiness check failed")
		}
		return err
	}
	switch arguments[0] {
	case "configure":
		return commandConfigure(defaults, arguments[1:])
	case "configure-server":
		return commandConfigure(defaults, arguments[1:])
	case "configure-rime-bridge":
		return commandConfigureRimeBridge(defaults, arguments[1:])
	case "register":
		return commandRegister(ctx, defaults, arguments[1:])
	case "login":
		return commandLogin(ctx, defaults, arguments[1:])
	case "logout":
		return commandLogout(ctx, defaults, arguments[1:])
	case "claim-account":
		return commandClaimAccount(ctx, defaults, arguments[1:])
	case "prepare-account":
		return commandPrepareAccount(ctx, defaults, arguments[1:])
	case "init-account":
		return commandInitAccount(ctx, defaults, arguments[1:])
	case "abort-account":
		return commandAbortAccount(ctx, defaults, arguments[1:])
	case "sync-once":
		return commandSyncOnce(ctx, defaults, arguments[1:])
	case "run":
		return commandRun(ctx, defaults, arguments[1:])
	case "status":
		return commandStatus(ctx, defaults, arguments[1:])
	case "resident-ready":
		return commandResidentReady(ctx, defaults, arguments[1:])
	default:
		if handled, privateErr := runPrivatePairingCommand(ctx, defaults, arguments); handled {
			return privateErr
		}
		return errors.New("unknown command")
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "yunpin-sync-agent:", err)
		os.Exit(1)
	}
}
