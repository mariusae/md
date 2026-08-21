#!/usr/bin/env bash
set -euo pipefail

package_dir="$(cd "$(dirname "$0")" && pwd)"
cargo build \
  --manifest-path "$package_dir/upstream/Cargo.toml" \
  --release \
  --target wasm32-unknown-unknown
cp \
  "$package_dir/upstream/target/wasm32-unknown-unknown/release/grok_mermaid.wasm" \
  "$package_dir/renderer.wasm"
