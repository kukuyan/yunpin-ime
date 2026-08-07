# macOS frontend boundary

Target: macOS 13 or later, Universal arm64/x86_64.

The macOS package is built from the pinned GPL-3.0 Squirrel source and uses one
`IMKInputController` per client. The YunPin provider exposes an immutable local
candidate snapshot; index rebuild, SQLite and sync occur outside key event
handling. Secure input and private mode suppress learning events.

Copy `../rime/common/default.custom.yaml` and
`../rime/squirrel/squirrel.custom.yaml` into the test user's Rime data
directory, then redeploy. The Squirrel 1.x overlay uses the supported
`candidate_list_layout: linear` setting rather than the removed `horizontal`
property.

Native acceptance covers TextEdit, Safari, Office, Terminal, composition text,
candidate placement, dark/light appearance, native and Rosetta applications,
and uninstall. The current bootstrap can be source-tested with Command Line
Tools, but a production bundle requires full Xcode, Developer ID signing and
Apple notarization.
