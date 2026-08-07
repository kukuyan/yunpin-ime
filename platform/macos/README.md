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
module and `yunpin_filter` component into the Universal runtime. The schema
places that filter before `uniquifier`, with a hard two-personal-candidate cap.
No personal snapshot is bundled; the read-only bridge activates only when a
reviewed `yunpin/private.tsv` exists in the isolated user directory.

The module merge and symbol are build-verified. A headless librime fixture also
verifies initial-based and full-pinyin long-phrase ranking, the two-personal
candidate cap, upstream deduplication, candidate commit and immediate
private-mode suppression. Real InputMethodKit host evidence has not passed yet.
Learning events, encrypted SQLite refresh and cloud sync also remain
disconnected.
`preview-manifest.json` records those gates separately so a merged module is not
confused with end-to-end acceptance.

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

They verify the Squirrel and nested-submodule locks, ordered patch application,
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

Before a signed release, verify the merged read-only filter through real
InputMethodKit hosts. Connect the
encrypted SQLite/background snapshot refresh and learning bridge, with secure
and private input suppressing both ranking and learning. Then test on real
hardware in TextEdit, Safari, Office and Terminal, including native and Rosetta
hosts, marked text, numbered selection, caret movement, dark/light candidate
placement, secure text fields, sleep/login, upgrade and uninstall.

Production distribution additionally requires Developer ID signing,
notarization and stapling in a protected GitHub Release environment. The
development scripts intentionally contain no signing or notarization secret.
