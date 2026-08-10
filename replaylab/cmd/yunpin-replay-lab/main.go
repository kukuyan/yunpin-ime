// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kukuyan/yunpin-ime/replaylab"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	defaultRoot, err := replaylab.DefaultRoot()
	if err != nil {
		return err
	}
	root := flags.String("root", defaultRoot, "local Replay Lab root")
	input := flags.String("input", "-", "JSONL input path or - for stdin")
	output := flags.String("output", "", "export output ending in .yunpinreplay")
	confirm := flags.Bool("confirm", false, "confirm exact lab-root deletion")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("%s flags: %w", command, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	switch command {
	case "init":
		store, err := replaylab.Init(*root, time.Now())
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"state": "disabled", "root": store.Root(), "network": "disabled"})
	case "clear":
		if err := replaylab.Clear(*root, *confirm); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"state": "cleared", "root": filepath.Clean(*root)})
	}

	store, err := replaylab.Open(*root)
	if err != nil {
		return err
	}
	switch command {
	case "start":
		metadata, err := store.Start(time.Now())
		if err != nil {
			return err
		}
		return writeJSON(stdout, metadata)
	case "pause":
		metadata, err := store.Pause(time.Now())
		if err != nil {
			return err
		}
		return writeJSON(stdout, metadata)
	case "resume":
		metadata, err := store.Resume(time.Now())
		if err != nil {
			return err
		}
		return writeJSON(stdout, metadata)
	case "status":
		metadata, err := store.Status()
		if err != nil {
			return err
		}
		return writeJSON(stdout, metadata)
	case "ingest":
		return ingest(store, *input, stdin, stdout)
	case "report":
		report, err := store.Report()
		if err != nil {
			return err
		}
		return writeJSON(stdout, report)
	case "export":
		if *output == "" {
			return errors.New("export requires --output PATH.yunpinreplay")
		}
		if err := store.Export(*output); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"state": "exported", "output": filepath.Clean(*output)})
	default:
		return usageError()
	}
}

func ingest(store *replaylab.Store, input string, stdin io.Reader, stdout io.Writer) error {
	reader := stdin
	var file *os.File
	if input != "-" {
		opened, err := os.Open(input)
		if err != nil {
			return fmt.Errorf("open ingest input: %w", err)
		}
		file = opened
		defer file.Close()
		reader = file
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), replaylab.MaxEventBytes+1)
	accepted := 0
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		event, err := replaylab.DecodeEventV1(scanner.Bytes())
		if err != nil {
			return fmt.Errorf("ingest line %d: %w", lineNumber, err)
		}
		if err := store.Append(event); err != nil {
			return fmt.Errorf("ingest line %d: %w", lineNumber, err)
		}
		accepted++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read ingest stream: %w", err)
	}
	return writeJSON(stdout, map[string]any{"accepted": accepted, "source": "native_sidecar_or_experiment_harness"})
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usageError() error {
	return errors.New("usage: yunpin-replay-lab <init|start|pause|resume|status|ingest|report|export|clear> [--root PATH]")
}
