<!-- SPDX-License-Identifier: GPL-3.0-only -->

# YunPin macOS development preview

The macOS client is a GPL-3.0-only derivative of the exact Squirrel 1.1.2
commit recorded in `../upstream-lock.json`. It produces a Universal
`YunPin.app` InputMethodKit bundle for macOS 13 or later and an unsigned
development `.pkg`. It does not modify the pinned submodule in place.

This slice is deliberately labelled **development preview**. It provides a
buildable native InputMethodKit frontend, the original horizontal YunPin
candidate theme, Rime Ice data, isolated user storage and offline packaging.
The build stages the Apache-2.0 `librime-yunpin` adapter and portable `engine/`
sources into Squirrel's exact nested librime checkout, then merges the `yunpin`
module, `yunpin_filter` and schema-local `yunpin_corrector` into the Universal
runtime. A base-commit and SHA-256-locked compatibility patch for Squirrel's
librime 1.16 lets ScriptTranslator select that unique corrector and classify an
exact match by `(spelling ID, consumed input length)` without replacing the
upstream process-global component. The schema places the filter before
`uniquifier`, with a hard two-personal-candidate cap.
No personal snapshot is bundled; the read-only bridge activates only when a
reviewed `yunpin/private.tsv` exists in the isolated user directory.

The module merge and symbol are build-verified. A headless librime fixture also
verifies short-input filtering, initial-based and full-pinyin long-phrase
ranking, the two-personal candidate cap, upstream deduplication, candidate
commit, immediate private-mode suppression, deterministic Pinyin typo
correction, and the real librime session flow
`日长 → Backspace×2 → 日常 → requery`. Typo goldens cover a QWERTY neighbour,
missing key, extra key, adjacent transposition, reviewed one-way `you` → `yao`,
and the user's two-error `shouxubijiakuaideshihou` sample while keeping exact
`xu` and `you` first. It also keeps exact “上班” before a present corrected
“山班” whose synthetic dictionary weight is 50 times higher. After Prism
validation, the adapter exposes no more than 16 deterministic correction edges
per input offset. Real InputMethodKit host evidence has not passed yet. The
in-process correction is not persisted; encrypted SQLite refresh, a desktop
habit monitor and cloud sync remain disconnected.
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
protocol. No input event performs network access. The package seeds only
versioned public overlays when no user file exists; it never overwrites an
existing user configuration. Personal dictionaries are not bundled.

The app and input-source art are original YunPin vector shapes. The build
replaces Squirrel's upstream icon source with `YunPin.icon` and replaces
`rime.pdf` with a newly rendered `yunpin.pdf` before Xcode sees the project.

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
make -C platform/macos package
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
  source, patches, build scripts and licenses.

The GitHub `macos-client` job selects the runner's pinned Xcode 26.3 and repeats
the static tests, merged Universal librime build and headless ranking fixture,
Universal Xcode build, bundle symbol verification, package build and artifact
upload on `macos-15`. A successful CI build proves source/build portability,
that the app contains the module, and the synthetic candidate flow; it does not
prove InputMethodKit composition, candidate-window placement or application
compatibility.

The real merged-Rime fixture warms each corrected long input 10 times and times
the final key plus candidate enumeration for 100 samples, failing above P95
20 ms. On the completed synthetic run, the two-error
`shouxubijiakuaideshihou` case measured 534–841 µs and the 37-byte reviewed
`you` → `yao` case measured 907–1611 µs across two independent runs. These are current-machine, small synthetic
dictionary results, not full Rime Ice, 50,000-personal-entry or InputMethodKit
performance guarantees. Stock librime also gives every correction edge the
same fixed `log(0.01)` credibility, so production word weights and context can
still expose exact-versus-corrected ranking collisions beyond the tested 50×
prefix case.

With pinned dependencies already prepared, the merged runtime can be checked
independently of Xcode's app build:

```sh
platform/macos/scripts/stage-yunpin-plugin.sh build/macos/squirrel
platform/macos/scripts/build-librime-yunpin.sh build/macos/squirrel
```

## Development installation and removal

Do not install the preview on a production workstation. On an isolated test
account, inspect its checksum and source archive first, then install the package
manually. The installer registers and enables YunPin but does not select it or
overwrite existing Rime settings.

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
