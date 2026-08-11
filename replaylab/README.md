# YunPin Replay Lab

Replay Lab is a local, opt-in experiment recorder and deterministic analyzer for YunPin IME. The MVP defines the bounded event protocol, append-only store, lifecycle CLI, export format, rule-based report, native-frame parser, and a disabled-by-default bounded C++ event ring described in [the operator guide](../docs/INPUT_REPLAY_LAB.md).

It is **disabled by default** and has no networking or synchronization code. The Windows/macOS input-method callbacks and background sinks are **not connected yet**, so installing or starting this CLI does not mean continuous monitoring is active. The missing Squirrel and Weasel bridges are P0. `ingest` accepts events only from a future YunPin native sidecar or a dedicated experiment harness; this project is not a system keyboard logger.

Build and test with the Go version pinned in `go.mod`:

```sh
go test ./...
go vet ./...
go build ./cmd/yunpin-replay-lab
```

All checked-in tests use synthetic text. Real `*.yunpinreplay` traces belong outside the repository and are blocked by both `.gitignore` and the private-data scan.
