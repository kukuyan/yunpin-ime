// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

type commonFlags struct {
	profile  string
	state    string
	endpoint string
	database string
	lock     string
	service  string
}

func addCommonFlags(set *flag.FlagSet, defaults desktopagent.Paths) *commonFlags {
	flags := &commonFlags{}
	set.StringVar(&flags.profile, "profile", desktopagent.DefaultProfile, "local credential profile")
	set.StringVar(&flags.state, "state-dir", defaults.StateDirectory, "private local state directory")
	set.StringVar(&flags.endpoint, "endpoint-config", defaults.EndpointConfigPath, "non-secret endpoint JSON")
	set.StringVar(&flags.database, "database", defaults.DatabasePath, "encrypted local SQLite database")
	set.StringVar(&flags.lock, "lock", defaults.LockPath, "single-instance lock file")
	set.StringVar(&flags.service, "credential-service", defaults.CredentialService, "OS credential service identifier")
	return flags
}

func (flags commonFlags) validate() error {
	for label, path := range map[string]string{
		"state directory": flags.state, "endpoint config": flags.endpoint,
		"database": flags.database, "lock": flags.lock,
	} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s path must be absolute", label)
		}
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
	return secrets, desktopagent.Agent{
		Secrets: secrets, Profile: flags.profile, EndpointConfigPath: flags.endpoint,
		DatabasePath: flags.database,
	}, nil
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

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
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
	summary, err := agent.SyncOnce(ctx)
	if err != nil {
		return err
	}
	return writeJSON(summary)
}

func commandInitAccount(_ context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("init-account", flag.ContinueOnError)
	addCommonFlags(set, defaults)
	confirm := set.Bool(
		"confirm-display-recovery-key", false,
		"confirm that the one-time recovery key and account ID may be displayed on stdout",
	)
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("init-account requires --confirm-display-recovery-key before any network request")
	}
	// The relay currently has no account-delete rollback. Do not create a
	// remote account that could become permanently orphaned if the following
	// local Keychain/DPAPI or SQLite commit failed.
	return errors.New("init-account is disabled until rollback-safe account provisioning is available")
}

func commandRun(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	interval := set.Duration("interval", time.Minute, "successful synchronization interval")
	if err := parse(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
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
		return errors.New("usage: yunpin-sync-agent <init-account|sync-once|run|status> [options]")
	}
	defaults, err := desktopagent.DefaultPaths()
	if err != nil {
		return err
	}
	switch arguments[0] {
	case "init-account":
		return commandInitAccount(ctx, defaults, arguments[1:])
	case "sync-once":
		return commandSyncOnce(ctx, defaults, arguments[1:])
	case "run":
		return commandRun(ctx, defaults, arguments[1:])
	case "status":
		return commandStatus(ctx, defaults, arguments[1:])
	default:
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
