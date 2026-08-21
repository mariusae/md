# grok-mermaid renderer

This internal package embeds `grok-mermaid.wasm` from
`/home/meriksen/src/tries/2026-08-21-simonw-tools/grok-mermaid`, which is the
Mermaid terminal renderer extracted from `xai-org/grok-build` by Simon
Willison. The renderer supports flowchart, sequence, state, class, and entity
relationship diagrams and falls back to a framed source listing for
unsupported syntax.

The exact Rust source used to produce the artifact is vendored under
`upstream/`. The embedded renderer and its original source are Apache-2.0
licensed; see `LICENSE`.

`renderer.wasm` is architecture-neutral and is executed by Node.js through
the package's Go API. To rebuild it, install Rust's
`wasm32-unknown-unknown` target and run `./rebuild.sh`.
