// SPDX-License-Identifier: Apache-2.0
//
// yunpin-sync-resident is the windowless background synchronization process.
//
// It exists as a separate binary from yunpin-sync-agent for one reason: on
// Windows it is linked with -H=windowsgui. Go links console-subsystem binaries
// by default, so a scheduled task that starts a long-running console binary in
// the user's interactive session gets a console window allocated for it -- not
// a flash, a window that stays for the life of the process. The interactive
// subcommands (status, configure, pairing, ...) print JSON to stdout and must
// stay console-subsystem, so the two cannot be one binary.
//
// This binary therefore implements only `run`. It writes no stdout, and its
// redacted run events go to the bounded log file both platforms share.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

func run(ctx context.Context, arguments []string) error {
	set := flag.NewFlagSet("yunpin-sync-resident", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	interval := set.Duration("interval", time.Minute, "successful synchronization interval")
	if err := set.Parse(arguments); err != nil {
		return errors.New("usage: yunpin-sync-resident [--interval <duration>]")
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional argument")
	}
	if *interval < time.Second {
		return errors.New("interval must be at least one second")
	}

	defaults, err := desktopagent.DefaultPaths()
	if err != nil {
		return err
	}
	log, err := desktopagent.OpenEventLog(defaults)
	if err != nil {
		return err
	}
	defer log.Close()

	return desktopagent.RunResident(ctx, defaults, desktopagent.ResidentOptions{
		Interval: *interval,
		Events:   log.Write,
	})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		// A windowless process has nowhere useful to print. Report through the
		// exit code; the bounded log file carries the redacted detail.
		_, _ = fmt.Fprintln(os.Stderr, "yunpin-sync-resident:", err)
		os.Exit(1)
	}
}
