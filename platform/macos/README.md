<!-- SPDX-License-Identifier: GPL-3.0-only -->

# YunPin macOS development preview

The macOS client is a GPL-3.0-only derivative of the exact Squirrel 1.1.2
commit recorded in `../upstream-lock.json`. It produces a Universal
`YunPin.app` InputMethodKit bundle for macOS 13 or later, an unsigned
development `.pkg` and a checksum-verified `.dmg`. It does not modify the
pinned submodule in place.

This slice is deliberately labelled **development preview**. It provides a
buildable native InputMethodKit frontend, the original horizontal YunPin
candidate theme, Rime Ice data, isolated user storage and offline packaging.
The build stages the Apache-2.0 `librime-yunpin` adapter and portable `engine/`
sources into Squirrel's exact nested librime checkout, then merges the `yunpin`
module, `yunpin_filter`, candidate-comment visibility filter and schema-local
`yunpin_corrector` into the Universal runtime. A base-commit and SHA-256-locked
compatibility patch for Squirrel's
librime 1.16 lets ScriptTranslator select that unique corrector and classify an
exact match by `(spelling ID, consumed input length)` without replacing the
upstream process-global component. The schema places the filter before
`uniquifier`, with a hard two-personal-candidate cap.
No personal snapshot is bundled; the read-only bridge activates only when a
reviewed `yunpin/private.tsv` exists in the isolated user directory.

The module merge and symbol are build-verified. A headless librime fixture also
verifies short-input filtering, initial-based and full-pinyin long-phrase
ranking, the two-personal candidate cap, upstream deduplication, candidate
commit, immediate private-mode suppression, conservative Pinyin typo
correction, and the real librime session flow
`日长 → Backspace×2 → 日常 → requery`. Its regression boundary now matches the
shipped policy: correct `shujukushiyongdeshinagebanben` stays on “数据库使用的是
哪个版本”; complete valid Pinyin such as `shouxu...`, `you...` and `shangban`
does not invite spelling expansion; one invalid trailing key may create one
exact-prefix-to-exact-suffix bridge at total rank #2; and two invalid regions
produce no correction. The synthetic `youceshizhanghaoma` pair is retained only
as exact homophone evidence, not presented as a spelling-correction success.
The fixture's echo translator supplies the ordinary #1 fail-safe candidate
needed to exercise the real long-correction rank guard. After Prism validation,
the adapter exposes no more than 16 deterministic edges at one bridge offset
and performs no more than 32 variant searches per input. Real InputMethodKit
host evidence has not passed yet. The in-process correction is not persisted;
encrypted SQLite refresh, a desktop habit monitor and cloud sync remain
disconnected.
Expression search/favorite is likewise deferred: the preview never interprets
candidate commit text as a browser or file-system command and injects no action
candidate until a typed, explicitly armed native channel exists.
`preview-manifest.json` records those gates separately: merged-librime typo E2E
is true, while native-host typo E2E, production-dictionary typo E2E, a local
model sidecar and Chinese-English mixed input remain false. This prevents a
merged module from being confused with end-to-end acceptance.

## Identity and privacy boundary

The patch set assigns identifiers that cannot collide with an installed
Squirrel input method:

- app and executable: `/Library/Input Methods/YunPin.app` and `YunPin`;
- bundle/input source: `io.github.kukuyan.inputmethod.YunPin`;
- modes: `.Hans` and `.Hant` beneath that identifier;
- InputMethodKit connection: `YunPin_Connection`;
- user data: `~/Library/Application Support/YunPin/Rime`;
- temporary logs: the `yunpin.ime` temporary directory.

Squirrel's upstream Sparkle feed and menu are disabled, and its built-in Rime
sync command is disabled so it cannot be mistaken for YunPin's encrypted cloud
protocol. No input event performs network access. The package seeds versioned
public overlays only when no user file or symbolic link exists. One reviewed
upgrade migration recognizes the exact hashes of YunPin's previously shipped
aggressive correction overlay and the immediately previous conservative
overlay, saves a mode-600 backup, and atomically replaces only those known files
with the current conservative default. A separate exact-hash migration updates
the previously shipped default overlay so the new candidate-Pinyin choice can
be saved. This lets an existing installation receive the menu and its preference
without treating arbitrary user configuration as package-owned. Every other
existing configuration, including custom content and links, is preserved.
Personal dictionaries are not bundled.

The app and input-source art are original YunPin vector shapes. The build
replaces Squirrel's upstream icon source with `YunPin.icon` and replaces
`rime.pdf` with a newly rendered `yunpin.pdf` before Xcode sees the project.
The compact candidate template retains Rime's comment placeholder, but the
schema-menu option `拼音关 / 拼音开` controls a final display-only filter. It is
off on first run, so bracketed spelling hints are hidden by default while
candidate order, text, score and commit behavior remain unchanged. Open the
Rime schema menu with `F4` or `Control+grave` and choose `拼音开` to reveal the
hints, or `拼音关` to hide them again; a new package or redeploy is not needed.
The option intentionally has no `reset`, and is listed under `save_options`,
but Squirrel scopes non-global switches by app/input session, so another app or
text field may keep its own state instead of inheriting a machine-wide choice.
Xcode registers macOS application products from DerivedData as a normal final
build step. To keep those development copies out of the input-source menu, the
preview build unregisters only its exact generated `YunPin.app` path after all
verification. A separate fail-closed check accepts either no exact-path record
or a disabled tombstone retained for a previously mounted removable volume. A
freshly seeded database with a structurally valid `Bundle: ... 0 units` table
is also accepted because that table cardinality proves no bundle path can be
active; a missing table, count mismatch, malformed record or active exact path
still fails closed. Parse failures emit only structural counts, never the other
applications' paths. On current macOS versions an already-disabled removable-
volume tombstone can make the unregister request return a nonzero status; the
build accepts that status only after the database check proves the exact path
is inactive. It does not delete the build artifact or unregister the installed
`/Library/Input Methods/YunPin.app`.

## Local verification

Static tests work with Command Line Tools and do not download build archives:

```sh
make -C platform/macos test
```

They verify the Squirrel and nested-submodule locks, ordered Squirrel and
librime patch application,
unique IMK identity, disabled update/sync paths, original artwork, safe config
injection, module-staging/build contracts, manifest honesty, shell syntax and
PDF rendering.

A native build needs full Xcode 26 or later (selected with `xcode-select` or
provided through `DEVELOPER_DIR`) and CMake. The pinned Squirrel source uses an
Icon Composer project:

```sh
make -C platform/macos dmg BUILD_ROOT=build/macos
```

The build downloads only the HTTPS archives listed in
`dependencies.lock.json`, verifies SHA-256 before extraction, initializes the
Squirrel-pinned Plum and librime commits, and pre-seeds every Squirrel preset
package at an exact commit so Plum cannot resolve moving branches. Before the
Xcode build, it stages `librime-yunpin` plus `engine/include` and `engine/src`
under nested librime and builds a merged `arm64 x86_64` librime with a macOS 13
deployment target. Outputs are under
`build/macos/package/`:

- `YunPin-IME-development-preview.pkg` — ad-hoc app signature and unsigned
  installer, for controlled development testing only;
- `YunPin-IME-development-preview-source.tar.gz` — matching GPL corresponding
  source, patches, build scripts and licenses;
- `YunPin-IME-macOS-development-preview.dmg` — reproducible UDZO container with
  the unsigned package, corresponding source, Chinese installation guidance
  and an internal checksum manifest;
- `YunPin-IME-macOS-development-preview.sha256` — checksum of the DMG itself.

The DMG builder uses the source commit time (or an explicit
`SOURCE_DATE_EPOCH`), an allowlisted root, normalized HFS catalog metadata and a
deterministic UDIF segment identifier. Identical inputs therefore produce the
same DMG bytes. It runs `hdiutil verify`, attaches the image read-only, rejects
unexpected files or symbolic links, compares every mounted file with staging,
and verifies the internal checksum list before publishing the image atomically.
Neither the DMG nor its enclosed package is Developer ID signed or notarized;
both remain explicitly labelled as development previews. Personal dictionaries
and user configuration are never staged.

Installation refreshes the TIS registration for the fixed system app even
when YunPin is already enabled. The postinstall then enters the logged-in
user's GUI bootstrap, enables and briefly selects each previously enabled mode
(or the primary mode on a fresh install) so macOS persists it, and restores the
input source that was active before the upgrade. This reconciles the stale
runtime-cache/persistent-preference split that can otherwise make YunPin vanish
from the input menu after an ad-hoc preview upgrade without leaving YunPin
selected unexpectedly. Registration, persistence or selection restoration
failure is returned to the package installer instead of being reported as a
successful install.

The GitHub `macos-client` job pairs the `macos-26` runner with its pinned Xcode
26.4.1 and repeats
the static tests, merged Universal librime build and headless ranking fixture,
Universal Xcode build, bundle symbol verification, package build and artifact
upload. Its merged-librime build is capped at two parallel jobs to fit the
runner's memory budget. Local builds use the same safe default; set
`YUNPIN_MACOS_BUILD_JOBS` to another positive integer only when the build host
has been sized for it. Invalid values stop the build before CMake starts.
Keeping the host OS and Xcode on the same runner generation avoids the
AssetCatalogAgent system-symbol mismatch seen when Xcode 26 ran on `macos-15`.
A successful CI build proves source/build portability,
that the app contains the module, and the synthetic candidate flow; it does not
prove InputMethodKit composition, candidate-window placement or application
compatibility.

The real merged-Rime fixture warms each final-key path 10 times and times the
final key plus candidate enumeration for 100 samples, failing above P95 20 ms.
It covers both an exact database sentence and a single trailing-extra-key
bridge. A separate real-Prism O2 probe measured correct whole-input paths at
P95 4.4–6.5 µs with zero `ToleranceSearch` calls, the tail bridge at 0.591 ms
with 13 bounded searches, and a 32-search stress case at 1.303 ms on the current
machine. These are small synthetic/current-machine results; this merged E2E
does not layer the 100,000-entry personal snapshot benchmark and is not a full
Rime Ice or InputMethodKit performance guarantee. Selecting a late bridge
without several read-only prefix attempts remains a P1 optimization. Stock
librime gives every correction edge the same fixed
`log(0.01)` credibility, so production word weights and context still require
the display-time #2/#3 guard.

With pinned dependencies already prepared, the merged runtime can be checked
independently of Xcode's app build:

```sh
platform/macos/scripts/stage-yunpin-plugin.sh build/macos/squirrel
platform/macos/scripts/build-librime-yunpin.sh build/macos/squirrel
```

## Development installation and removal

Do not install the preview on a production workstation. On an isolated test
account, inspect its checksum and source archive first, then install the package
manually. The installer registers and persistently enables YunPin, temporarily
selects it only as part of reconciliation, restores the previously active input
source, and does not overwrite unrecognised Rime settings.

To inject or intentionally refresh the reviewed overlays without installing:

```sh
platform/macos/scripts/inject-rime-config.sh
platform/macos/scripts/inject-rime-config.sh --force
```

Removal requires disabling YunPin in System Settings, quitting its process,
and removing the exact `/Library/Input Methods/YunPin.app` bundle. The isolated
user data directory should be preserved unless the user separately chooses to
delete it.

## Remaining native acceptance gates

Before a signed release, verify the merged filter and in-process correction
through real InputMethodKit hosts. Connect encrypted SQLite/background snapshot
refresh and habit reporting, strengthen host deletion/secure-context evidence,
and keep private input suppressing both ranking and learning. Then test on real
hardware in TextEdit, Safari, Office and Terminal, including native and Rosetta
hosts, marked text, numbered selection, caret movement, dark/light candidate
placement, secure text fields, sleep/login, upgrade and uninstall.

The preview has no language model and correction never opens disk or network.
If a later release experiments with a model, it must remain an optional,
default-off, offline sidecar with bounded local IPC, a strict timeout and
fail-closed fallback to exact/deterministic candidates. Chinese-English mixed
input is likewise a future segmentation/ranking direction, not a current macOS
capability.

Production distribution additionally requires Developer ID signing,
notarization and stapling in a protected GitHub Release environment. The
development scripts intentionally contain no signing or notarization secret.
