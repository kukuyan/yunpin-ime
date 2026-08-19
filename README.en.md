# YunPin IME

YunPin is a privacy-first Chinese Pinyin input method with optional self-hosted encrypted synchronization. Windows 10/11 and macOS 13+ are the current targets; a shared candidate core for Android and iOS 17+ has entered phase two. Its compact, numbered horizontal candidate bar follows familiar IME conventions without copying proprietary branding or assets.

This repository is a development preview. It contains a runnable ranking core, offline migration tools, an opaque-envelope sync server, Rime desktop configuration, and CI. Signed native installers are not available yet.

## Download development previews

[GitHub Releases](https://github.com/kukuyan/yunpin-ime/releases) provides the
Windows runtime archive and macOS Universal DMG together with corresponding
source, an SPDX SBOM, and canonical `SHA256SUMS`. These are explicitly
**unsigned and unnotarized development previews** for controlled personal
testing. Verify the checksums and read the installation notes first; no signed
stable installer is available yet.

Key properties:

- Pinned personal phrases rank before learned/imported phrases, public packs, and Rime base candidates.
- Pinned long phrases can be recalled after two complete syllables or four initials; exact full Pinyin ranks first.
- Automatic vocabulary is eligible for sync after two explicit selections.
- Personal phrases occupy at most two of the first eight candidates.
- A bounded local corrector covers physical QWERTY neighbours, one missing or
  extra key, adjacent transposition, and a small reviewed valid-syllable list;
  exact short Pinyin remains preferred.
- The keystroke path reads an immutable in-memory snapshot and never waits for disk or network.
- Personal data is encrypted client-side; the server stores opaque envelopes and token hashes.
- ChatGPT/Codex/text imports are previewed and filtered locally. SCEL/BIN conversion is delegated to a pinned, offline ImeWlConverter process operating on a copy.
- Public packs are built offline only after verifying all four source commits and clean worktrees; the generated manifest records source hashes and licenses.

Run the checks:

```bash
make test-engine
make test-tools
make test-sync-docker
make privacy-check
```

Start the development sync service with `docker compose up --build`, then visit `http://127.0.0.1:8787/healthz`.
You can point clients to a local NAS relay by setting a local URL such as `http://192.168.1.127:8787`; client-side behavior is unchanged.

The original shared code is Apache-2.0. Weasel/Squirrel derivative patches and desktop distributions are GPL-3.0. Third-party dictionaries retain their upstream licenses. See [the license matrix](docs/LICENSE_MATRIX.md), [privacy model](PRIVACY.md), and [architecture](docs/ARCHITECTURE.md).

The current corrector is deterministic and does not call a model or network
service. A future local-model experiment, if justified, will remain an
optional default-off sidecar with a strict deadline and fail-closed fallback;
Chinese-English mixed-input ranking is also a documented future direction,
not a current preview capability.

### If you still see only 5 candidates locally

This is usually caused by stale per-user Rime overlays. You can refresh without rebuilding:

- macOS:

```bash
platform/macos/scripts/inject-rime-config.sh --force
killall Squirrel || true
```

- Windows (PowerShell):

```powershell
Copy-Item -LiteralPath "$PWD\platform\rime\weasel\default.custom.yaml" -Destination "$env:APPDATA\YunPin\Rime\default.custom.yaml" -Force
Copy-Item -LiteralPath "$PWD\platform\rime\weasel\weasel.custom.yaml" -Destination "$env:APPDATA\YunPin\Rime\weasel.custom.yaml" -Force
Copy-Item -LiteralPath "$PWD\platform\windows\rime\rime_ice.custom.yaml" -Destination "$env:APPDATA\YunPin\Rime\rime_ice.custom.yaml" -Force
```

Then reactivate YunPin (macOS: toggle input method, Windows: disable and re-enable) and test `12345678`.
