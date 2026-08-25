# YunPin Replay Lab

Replay Lab is a local, opt-in experiment recorder and deterministic analyzer for YunPin IME. It defines the bounded event protocol, append-only store, lifecycle CLI, export format, rule-based report, native-frame parser, and the disabled-by-default bounded C++ event path described in [the operator guide](../docs/INPUT_REPLAY_LAB.md).

It is **disabled by default** and has no networking or synchronization code. The merged Windows/macOS plugin now publishes bounded composition, candidate, selection, commit, and composition-backspace frames into an in-memory ring. A dormant host watcher drains that ring on a background thread only while `active.json` contains an explicitly started or resumed session. `pause` disables production and discards the boundary queue; protected contexts use the same fail-closed gate as learning. This project is not a system keyboard logger.

The synthetic native-host-to-Go-report test is automated on both desktop build paths. Real capture in an installed Squirrel/Weasel host remains a manual acceptance gate; source integration and CI do not prove that an older installed build has this feature.

Build and test with the Go version pinned in `go.mod`:

```sh
go test ./...
go vet ./...
go build ./cmd/yunpin-replay-lab
```

All checked-in tests use synthetic text. Real `*.yunpinreplay` traces belong outside the repository and are blocked by both `.gitignore` and the private-data scan.
