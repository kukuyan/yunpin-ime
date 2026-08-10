# YunPin Windows development preview

Target: Windows 10 22H2 or Windows 11 on x64. This frontend is a GPL-3.0
derivative of commit-pinned [Weasel](../../third_party/weasel/README.md). It
builds x86 and x64 TSF components plus one x64 input service. It is an unsigned
development preview, not a production installer.

## What is integrated

`scripts/Build-Preview.ps1` exports the pinned Weasel and librime trees into an
isolated generated directory. It applies the GPL patch series, then stages:

```text
build/windows/weasel-src/librime/plugins/librime-yunpin/
├── CMakeLists.txt and adapter sources from librime-yunpin/
└── engine/
    ├── include/ from engine/include/
    └── src/ from engine/src/
```

The script sets `RIME_PLUGINS=librime-yunpin` before building both x64 and
Win32 librime. It also rejects a generated librime project unless the merged
`yunpin` module is registered. The x86 TSF DLL does not link a second copy of
the phrase engine; both TSF architectures talk to the x64 service's merged
librime.

The preview Rime overlay inserts `yunpin_filter` before the normal Rime
candidate stream and caps it at two candidates. It reserves the unique
`yunpin_corrector` through a base-commit and SHA-256-locked minimal patch for
librime 1.17, but the shipped overlay keeps both
`translator/enable_correction: false` and `yunpin/typo_correction: false`.
When disabled, the component factory returns no corrector; it does not fall
back to NearSearch. The patch keys exact classification by `(spelling ID,
consumed input length)` for an explicitly enabled experiment, without replacing
librime's process-global `corrector` or changing another schema's behavior. Its
private snapshot remains disabled with `"yunpin/enabled": false`. The package
contains only an empty example header, never a real `yunpin/private.tsv`, user
database, Sogou file, or conversation data.

The independent `"yunpin/short_input_guard": true` remains active. It performs
only an in-memory check over Rime's already-produced candidates, so Windows can
drop implausible long pure-CJK predictions for one/two-letter input without
loading personal data.

`"yunpin/session_learning": false` is a separate Windows safety gate. Session
correction stays disabled until TSF secure-input and authenticated IPC behavior
has passed real-host testing; enabling the public short-input guard does not
silently enable learning.

Automatic typing correction is fail-closed in this preview:

```yaml
translator/enable_correction: false
translator/corrector_component: yunpin_corrector
yunpin/typo_correction: false
yunpin/typo_reviewed_confusions: false
yunpin/long_correction_guard: true
yunpin/long_correction_min_chars: 12
```

An explicit experiment may enable both translator and YunPin correction. If the
whole normalized input already has a normal exact path, correction expansion is
globally suppressed. Otherwise the graph permits only one bridge from a
forward exact-reachable offset to a reverse exact-suffix-reachable end, so the
whole composition can use at most one correction offset. Analysis is limited to
32 correction searches and input shorter than 128 bytes; each searched offset
keeps at most 16 deduplicated, Prism-validated edges. The reviewed valid-Pinyin
confusion set is a separate default-off experiment. No correction path reads a
personal snapshot, file, network service, or model.

For normalized correction input of at least 12 characters, the filter retains
at most one automatic correction candidate. It may occupy only total rank 2
when there is no private head, or total rank 3 when there is one private head;
with two private heads, a correction-only upstream, or no available target slot,
it is hidden and does not spill to a later page. Earlier synthetic correction,
weight-collision, and timing statements are not current Windows capability or
performance evidence. The opt-in experiment still needs fresh full-dictionary
candidate and latency acceptance through real Weasel/TSF.

Expression search/favorite is not connected in this preview. The TSF frontend
never interprets candidate commit text as a browser or file-system command,
and the filter injects no action candidate until a typed, explicitly armed
native channel exists. Command-looking ordinary/imported/synchronized text is
inserted as text.

## Isolated preview identity

The patch series assigns YunPin its own TSF CLSID/profile GUID, registry key,
named pipe, IPC window, mutexes, log/data path, executable names, and Rime app
name. It removes WinSparkle linkage and disables automatic/manual update calls,
so a development build cannot consult the upstream Weasel appcast.
`New-PreviewIcon.ps1` also generates the multi-size Windows icon from the
original `assets/yunpin-mark.svg` and replaces the upstream main/setup artwork
inside the isolated build tree.

The named-pipe ACL grants full access to SYSTEM and the current user, plus the
read/write compatibility rights needed by app-container TSF clients. This is
an ACL boundary, not cryptographic client authentication. Therefore private
candidate loading and learning remain blocked until all of these gates pass on
real hosts:

- current-user isolation and low-integrity/UWP compatibility;
- password, privacy-mode, and one-shot-input suppression across x86/x64 TSF;
- authenticated learning IPC and crash/reconnect behavior;
- Notepad, Office, Chrome, Terminal, and 32/64-bit host testing.

R0W is not contacted by any build, package, install, or test script. The reviewed
offline conversion contains 94,382 merged rows, but its complete TSV remains in
incoming staging and has not been deployed. The currently installed legacy
private-snapshot binary is limited to 50,000 rows; the full conversion must not
be enabled until a 100,000-row-capable binary is rebuilt and tested. The preview
package still carries no real private snapshot and keeps `yunpin/enabled: false`.

## Reproducible build

Run from a Visual Studio 2022 developer environment on a Windows x64 host with
the Desktop C++ workload, CMake, Git, Python, 7-Zip, and PowerShell 5.1+:

```powershell
git submodule update --init third_party/weasel third_party/librime third_party/rime-ice
git -C third_party/librime submodule update --init --recursive
PowerShell -ExecutionPolicy Bypass -File .\platform\windows\scripts\Build-Preview.ps1
```

The build checks every checkout, patch, and Boost archive against
`dependencies.lock.json`. Outputs are written only below `build/windows/`:

- `artifacts/YunPin-IME-Windows-development-preview.zip` — manifest-verified
  runtime, Rime Ice data, overlays, license texts, and guarded install scripts;
- `artifacts/YunPin-IME-Windows-development-preview-source.zip` — exact
  corresponding source, patches, build scripts, nested librime dependencies,
  Rime Ice, and the verified Boost source archive;
- `artifacts/SHA256SUMS` — hashes for both archives.

The runtime archive intentionally excludes WinSparkle and stock Weasel-named
binaries. `Test-Package.ps1` verifies its manifest, PE architectures, privacy
exclusions, and preview gates without registering the IME.

## Install and uninstall

Extract the runtime archive and read its `README.txt`. Installation requires an
explicit acknowledgement because the binaries are unsigned:

```powershell
PowerShell -ExecutionPolicy Bypass -File .\Install-Preview.ps1 `
  -AcceptUnsignedDevelopmentBuild
```

The installer verifies all hashes before mutation, keeps the runtime under
`%LOCALAPPDATA%\Programs\YunPinIME\Preview`, backs up replaced Rime config, and
uses `%APPDATA%\YunPin\Rime` for user data. Windows asks for elevation only when
the upstream registration helper installs the TSF components. Native helper
arguments use explicit Windows command-line quoting rather than PowerShell 5.1
`Start-Process`, whose trailing whitespace can make Weasel's exact option parser
fall through to an invisible dialog in a non-interactive session. The installer
also records the runtime root in both 32-bit and 64-bit registry views so either
TSF architecture can restart the x64 service. Uninstall retires the runtime to
a recoverable directory, removes the matching 64-bit runtime record, and retains
user dictionaries:

```powershell
PowerShell -ExecutionPolicy Bypass -File .\Uninstall-Preview.ps1 `
  -ConfirmUninstall
```

Production Windows releases require Authenticode signing and protected release
credentials. This preview never claims to be signed or production-ready.

## Checks runnable without Windows

```bash
python3 -m unittest discover -s platform/windows/tests -p 'test_*.py' -v
ruby scripts/check_yaml.rb
python3 scripts/check_supply_chain.py
```

CI performs the real Visual Studio build and uploads the runtime plus
corresponding-source archive for seven days.

No local language model is implemented. Any future experiment must be an
optional, default-off sidecar with no network access, bounded IPC and a strict
timeout; failure leaves only the ordinary exact candidate path. There is no
NearSearch or model fallback in the shipped disabled configuration.
Chinese-English mixed input remains a future segmentation/ranking direction and
must not be treated as a capability of this Windows preview.
