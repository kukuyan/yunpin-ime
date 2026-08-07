# Native platform integration

The planned desktop release uses pinned upstream Rime frontends rather than
forking and copying their full history into this repository:

```text
Windows applications -> x86/x64 Weasel TSF -> local IPC -> x64 input service
macOS applications   -> Squirrel IMKInputController -> librime
                                               |
                                               v
                                  librime-yunpin in-memory index
```

The bootstrap already supplies and benchmarks the immutable in-memory index.
The not-yet-implemented native adapters must query that index and Rime without
waiting for a file read, HTTP request, or sync operation; a background process
will rebuild and atomically swap it.

`upstream-lock.json` is the source of truth for tags and commits. A future release
checkout must verify each commit before applying the ordered patches described
in `patches/README.md`. The repository contains no proprietary Sogou resource,
skin, icon, dictionary, or code.

The Rime overlays provide a compact horizontal, numbered five-candidate panel
with independent light and dark palettes. It is an original YunPin appearance,
not a copy of a Sogou skin.

Signed distribution remains a release gate:

- Windows: x86/x64 TSF components plus x64 input service, Authenticode signed.
- macOS 13+: Universal arm64/x86_64 InputMethodKit bundle, Developer ID signed
  and notarized.
- iOS 17+: a later independent Swift keyboard; it may share Apache/BSD code but
  must not copy the GPL desktop frontends.
