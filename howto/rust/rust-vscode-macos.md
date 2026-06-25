# Rust Development in VS Code on macOS

A practical guide oriented toward Go engineers picking up Rust.

---

## 1. Prerequisites: Rust Toolchain

Before touching VS Code, get the toolchain right.

```bash
# Install rustup (the toolchain manager — think: like gvm/goenv for Go)
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh

# After install, restart your shell, then verify
rustc --version
cargo --version
rustup --version
```

Install the stable toolchain components you'll need daily:

```bash
rustup component add rust-analyzer   # Language server (required)
rustup component add clippy          # Linter (cargo clippy)
rustup component add rustfmt         # Formatter (cargo fmt)
rustup component add rust-src        # Source code (for go-to-def into std)
rustup component add llvm-tools      # For coverage reports
```

> **Go analogy:** `rustup` ≈ `go toolchain` / `gvm`. `cargo` ≈ `go` (build, test, run, mod tidy — all in one).

---

## 2. Extensions to Install

### Essential

| Extension | ID | Purpose |
|---|---|---|
| **rust-analyzer** | `rust-lang.rust-analyzer` | The LSP — completion, go-to-def, inlay hints, refactors. Do **not** install the deprecated "Rust" extension. |
| **CodeLLDB** | `vadimcef.codelldb` | LLDB-based debugger. Works with `launch.json`. Essential for breakpoints. |
| **Even Better TOML** | `tamasfe.even-better-toml` | `Cargo.toml` syntax highlighting, validation, schema completion. |
| **crates** | `serayuzgur.crates` | Shows latest crate versions inline in `Cargo.toml` with one-click update. |
| **Error Lens** | `usernamehere.error-lens` | Inline error/warning display on the line itself — saves constant glancing at the Problems panel. |

### Highly Recommended

| Extension | ID | Purpose |
|---|---|---|
| **GitLens** | `eamodio.gitlens` | Blame, history, stash — useful when tracing borrow errors across commits. |
| **Better Comments** | `aaron-bond.better-comments` | Color-codes `// TODO`, `// FIXME`, `// SAFETY`, `// PANIC` — Rust uses these heavily in docs. |
| **Dependi** | `fill-labs.dependi` | Like `crates` but also supports feature flags and workspace dependencies. |
| **Search Crates IO** | `belfz.search-crates-io` | Command palette search for crates without leaving VS Code. |

### Optional but Nice

| Extension | ID | Purpose |
|---|---|---|
| **Cargo Watch** | Built into rust-analyzer | Continuous background build on save — configure via settings. |
| **Toggle** | `rebornix.toggle` | Quickly flip boolean settings (useful for toggling inlay hints). |

---

## 3. VS Code Configuration

Put these in your **workspace** `.vscode/settings.json` (or user settings if you want them globally).

```jsonc
{
  // === rust-analyzer core ===
  "rust-analyzer.check.command": "clippy",      // Use clippy instead of bare check
  "rust-analyzer.check.extraArgs": ["--", "-W", "clippy::pedantic"],
  "rust-analyzer.cargo.features": "all",        // Analyze with all features enabled

  // === Inlay hints — very helpful coming from Go (Go has fewer implicit types) ===
  "rust-analyzer.inlayHints.typeHints.enable": true,
  "rust-analyzer.inlayHints.parameterHints.enable": true,
  "rust-analyzer.inlayHints.chainingHints.enable": true,
  "rust-analyzer.inlayHints.closureReturnTypeHints.enable": "always",
  "rust-analyzer.inlayHints.lifetimeElisionHints.enable": "skip_trivial",  // Shows 'a 'b hints

  // === Completion ===
  "rust-analyzer.completion.autoimport.enable": true,
  "rust-analyzer.completion.postfix.enable": true,  // .if, .match, .dbg etc

  // === Proc macros (derive, serde, tokio::main, etc) ===
  "rust-analyzer.procMacro.enable": true,

  // === Formatting ===
  "editor.formatOnSave": true,
  "[rust]": {
    "editor.defaultFormatter": "rust-lang.rust-analyzer",
    "editor.formatOnSave": true
  },

  // === Editor feel ===
  "editor.semanticHighlighting.enabled": true,  // Critical for Rust — shows lifetimes, mut, unsafe
  "editor.renderWhitespace": "boundary",

  // === Cargo ===
  "rust-analyzer.cargo.buildScripts.enable": true,  // Required for crates with build.rs
}
```

Add a `rustfmt.toml` at your project root for consistent formatting:

```toml
edition = "2021"
max_width = 100
use_small_heuristics = "Max"
imports_granularity = "Crate"   # Groups imports like goimports
group_imports = "StdExternalCrate"
```

---

## 4. Opening and Navigating a Rust Project

### Open the workspace root

Always open the directory containing `Cargo.toml`, not a subdirectory. For workspaces (multiple crates), open the root with the `[workspace]` `Cargo.toml`.

```bash
code path/to/my-project
```

rust-analyzer attaches to the `Cargo.toml` it finds at the workspace root. If you open a subdirectory, you may get partial analysis or no analysis at all.

### Project structure primer

```
my-project/
├── Cargo.toml           # Package manifest (≈ go.mod + build config)
├── Cargo.lock           # Lockfile (≈ go.sum — commit this for binaries, .gitignore for libs)
├── src/
│   ├── main.rs          # Binary entry point (fn main)
│   ├── lib.rs           # Library root (if also a lib)
│   └── bin/
│       └── other.rs     # Additional binaries
├── tests/               # Integration tests (separate from #[test] in src/)
├── benches/             # Criterion benchmarks
├── examples/            # cargo run --example foo
└── build.rs             # Build script (runs before compilation)
```

### Key navigation shortcuts

| Action | Shortcut |
|---|---|
| Go to definition | `F12` |
| Go to type definition | `Cmd+F12` |
| Peek definition | `Option+F12` |
| Find all references | `Shift+F12` |
| Go to implementations | `Cmd+Shift+F12` (finds impls of a trait) |
| Rename symbol | `F2` |
| Show all symbols in file | `Cmd+Shift+O` |
| Show workspace symbols | `Cmd+T` |
| Open `Cargo.toml` for crate | rust-analyzer status bar → click crate name |
| Expand macro inline | Right-click → **Expand Macro** |

### rust-analyzer-specific commands (Command Palette: `Cmd+Shift+P`)

- **rust-analyzer: Open Docs for Symbol Under Cursor** — opens docs.rs in browser
- **rust-analyzer: Expand Macro Recursively** — shows the full expanded macro output
- **rust-analyzer: View MIR** — shows Mid-level IR (useful for understanding what code actually does)
- **rust-analyzer: View Hir** — High-level IR
- **rust-analyzer: Reload Workspace** — when Cargo.toml changes aren't picked up
- **rust-analyzer: Run** — runs the item under the cursor (fn main, #[test], etc.)

---

## 5. Running Code

### From the terminal (preferred)

```bash
cargo run                        # Run main binary
cargo run --bin other            # Run specific binary
cargo run --example foo          # Run examples/foo.rs
cargo run -- --flag arg          # Pass args to your program
cargo run --release              # Optimized build
```

### From VS Code

rust-analyzer adds **Run | Debug** CodeLens above `fn main()` and `#[test]` functions. Click **Run** to execute in the integrated terminal.

To run with arguments, use a `launch.json` (see Debugging section below) or configure a task:

**.vscode/tasks.json:**
```json
{
  "version": "2.0.0",
  "tasks": [
    {
      "label": "cargo run",
      "type": "cargo",
      "command": "run",
      "args": ["--", "--my-flag", "value"],
      "problemMatcher": ["$rustc"],
      "group": { "kind": "build", "isDefault": true }
    },
    {
      "label": "cargo test",
      "type": "cargo",
      "command": "test",
      "problemMatcher": ["$rustc"],
      "group": { "kind": "test", "isDefault": true }
    }
  ]
}
```

Run with `Cmd+Shift+B` (default build task) or `Cmd+Shift+P` → **Run Task**.

---

## 6. Debugging

### Setup

CodeLLDB handles this. Install it, then create `.vscode/launch.json`:

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "type": "lldb",
      "request": "launch",
      "name": "Debug binary",
      "cargo": {
        "args": ["build", "--bin", "my-project", "--package", "my-project"],
        "filter": { "name": "my-project", "kind": "bin" }
      },
      "args": [],
      "cwd": "${workspaceFolder}",
      "env": { "RUST_LOG": "debug" }
    },
    {
      "type": "lldb",
      "request": "launch",
      "name": "Debug unit tests",
      "cargo": {
        "args": ["test", "--no-run", "--lib"],
        "filter": { "name": "my-project", "kind": "lib" }
      },
      "args": [],
      "cwd": "${workspaceFolder}"
    },
    {
      "type": "lldb",
      "request": "launch",
      "name": "Debug integration test",
      "cargo": {
        "args": ["test", "--no-run", "--test", "my_integration_test"],
        "filter": { "name": "my_integration_test", "kind": "test" }
      },
      "args": ["specific_test_name"],  // Filter to one test
      "cwd": "${workspaceFolder}"
    }
  ]
}
```

Press **F5** to start debugging. Breakpoints, watch expressions, call stack, variable inspection all work.

### Inspecting variables

LLDB renders Rust types natively — you'll see `Vec<T>` contents, `Option::Some(value)`, `HashMap` entries, etc. in the Variables panel.

For `String` / `&str`, LLDB shows them correctly in the hover and Variables view.

Useful LLDB commands in the Debug Console:

```
p my_var              # Print variable
p *my_box             # Deref a Box<T>
expr my_vec.len()     # Call methods
```

### Debugging async code (Tokio)

Add this to your `Cargo.toml` for better async stack traces:

```toml
[profile.dev]
debug = true

[dependencies]
tokio = { version = "1", features = ["full", "tracing"] }
console-subscriber = "0.2"   # tokio-console integration
```

Breakpoints work in async functions. Stack traces show the Tokio executor frames — expect some noise, but your code's frames are visible.

---

## 7. Testing

### Running tests

```bash
cargo test                          # All tests
cargo test my_test_name             # Filter by name substring
cargo test -- --nocapture           # Show println! output
cargo test -- --test-threads=1      # Serial (useful for tests with shared state)
cargo test --lib                    # Unit tests only
cargo test --test integration_test  # Specific integration test file
```

### From VS Code

CodeLens above `#[test]` functions shows **Run Test | Debug Test**. Click to run individual tests inline. Results appear in the integrated terminal.

The **Testing** panel (`Cmd+Shift+P` → **Testing: Focus on Test Explorer**) shows a tree of all tests and lets you run/debug them with one click — works well once rust-analyzer has indexed the project.

### Useful test patterns

```rust
#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn it_works() {
        assert_eq!(2 + 2, 4);
    }

    #[test]
    #[should_panic(expected = "overflow")]
    fn test_panic() { /* ... */ }

    #[test]
    fn with_result() -> Result<(), Box<dyn std::error::Error>> {
        // Can use ? operator here — test fails on Err
        Ok(())
    }
}
```

---

## 8. Linting and Code Quality

```bash
cargo clippy                       # Lint
cargo clippy -- -W clippy::pedantic  # Stricter
cargo clippy --fix                 # Auto-fix suggestions
cargo fmt                          # Format
cargo fmt -- --check               # Check without modifying (for CI)
```

rust-analyzer runs clippy on save if you set `"rust-analyzer.check.command": "clippy"`. Warnings show inline in the editor.

For `unsafe` blocks, clippy has extra lints:

```bash
cargo clippy -- -W clippy::undocumented_unsafe_blocks
```

---

## 9. Tips and Tricks

### Inlay hints are your best friend (coming from Go)

Rust has much richer type inference than Go. Turn on all inlay hints — they show inferred types, lifetimes, and closure return types inline, saving you from constantly hovering over variables. See settings above.

### Use `dbg!()` instead of `println!()` for quick inspection

```rust
let x = dbg!(some_complex_expr);  // Prints file, line, value — returns the value
```

### Postfix completions (very fast)

Type an expression, then `.` and get suggestions:
- `.if` → `if expr {}`
- `.match` → `match expr {}`
- `.dbg` → `dbg!(expr)`
- `.unwrap` → `expr.unwrap()`
- `.ok` → `expr.ok()`
- `.ref` → `&expr`
- `.not` → `!expr`

### Structural search and replace

`Cmd+Shift+P` → **rust-analyzer: Structural Search and Replace** — matches Rust syntax trees, not just text. Can replace `foo.unwrap()` with `foo?` across the project, correctly.

### Expand macros to understand them

When a derive or proc macro isn't doing what you expect, right-click → **Expand Macro**. This shows the exact code the macro generates — invaluable when debugging `serde`, `thiserror`, `async-trait`, etc.

### Go to generated impl

With the cursor on a `#[derive(Debug, Clone)]`, press `Cmd+Shift+F12` (Find Implementations) to see what trait impls the derive generates.

### Cargo features

```bash
cargo build --features "feature-a,feature-b"
cargo build --no-default-features --features "minimal"
```

In settings, `"rust-analyzer.cargo.features": "all"` ensures the LSP analyzes all feature-gated code, not just the default feature set.

### Cargo workspace tips

For workspaces (monorepo with multiple crates), add this to the root `Cargo.toml` to share dependency versions:

```toml
[workspace.dependencies]
tokio = { version = "1", features = ["full"] }
serde = { version = "1", features = ["derive"] }

# Then in member crates:
[dependencies]
tokio.workspace = true
serde.workspace = true
```

### `cargo-watch` for auto-rebuild

```bash
cargo install cargo-watch
cargo watch -x check         # Re-check on file save
cargo watch -x "test -- --nocapture"
cargo watch -x "run -- --my-flag"
```

### `cargo-expand` for macro debugging

```bash
cargo install cargo-expand
cargo expand                 # Expands all macros in the crate
cargo expand my_module       # Just one module
```

### Faster incremental builds with `mold` (optional)

On macOS, the default linker is slow for large projects. `lld` or `zld` help:

```toml
# .cargo/config.toml
[target.x86_64-apple-darwin]
rustflags = ["-C", "link-arg=-fuse-ld=/usr/local/bin/zld"]

[target.aarch64-apple-darwin]
rustflags = ["-C", "link-arg=-fuse-ld=/opt/homebrew/bin/zld"]
```

Install zld: `brew install michaeleisel/zld/zld`

For even faster builds, `sccache` caches compilation artifacts:

```bash
brew install sccache
# In .cargo/config.toml:
[build]
rustc-wrapper = "sccache"
```

### Useful `Cargo.toml` profile settings

```toml
[profile.dev]
opt-level = 0
debug = true
incremental = true   # Faster incremental builds

[profile.dev.package."*"]
opt-level = 2        # Optimize dependencies even in dev (huge speed win for serde, regex, etc.)

[profile.release]
opt-level = 3
lto = "thin"         # Link-Time Optimization
codegen-units = 1    # Slower compile, faster binary
strip = true         # Strip debug symbols from release binary
```

---

## 10. Useful Cargo Subcommands to Install

```bash
cargo install cargo-audit        # Check dependencies for known CVEs
cargo install cargo-outdated     # Show stale dependencies
cargo install cargo-tree         # Visualize dependency tree
cargo install cargo-machete      # Find unused dependencies
cargo install cargo-nextest      # Faster test runner with better output
cargo install cargo-llvm-cov     # Code coverage via LLVM instrumentation
```

Usage:

```bash
cargo audit                      # Security audit
cargo outdated                   # Outdated deps
cargo tree --duplicates          # Find duplicate transitive deps
cargo machete                    # Unused deps
cargo nextest run                # Drop-in test runner replacement
cargo llvm-cov --html            # Coverage report in target/llvm-cov/html/
```

---

## 11. Coming from Go — Key Mental Model Shifts

| Go concept | Rust equivalent | Notes |
|---|---|---|
| `interface{}` / `any` | `dyn Trait` / generics | Rust prefers monomorphized generics over dynamic dispatch |
| `error` interface | `Result<T, E>`, `?` operator | `thiserror` for library errors, `anyhow` for application errors |
| goroutines | `tokio::spawn` / `async fn` | `tokio` is the standard async runtime |
| channels | `tokio::sync::mpsc`, `std::sync::mpsc` | Rust channels are typed and bounded |
| `defer` | `Drop` trait | Implement `Drop` or use RAII wrappers; `scopeguard` crate for ad-hoc defer |
| `go test -race` | `cargo test` (borrow checker prevents data races) | Race conditions are compile-time errors in safe Rust |
| `go build ./...` | `cargo build --workspace` | |
| `go mod tidy` | `cargo update` + `cargo machete` | |
| `gofmt` | `rustfmt` via `cargo fmt` | |
| `golangci-lint` | `cargo clippy` | |

---

## 12. Troubleshooting

**rust-analyzer is slow / not responding**

- Check status bar bottom-left — it shows "rust-analyzer: Loading..." or error count
- `Cmd+Shift+P` → **rust-analyzer: Reload Workspace**
- For large workspaces: `"rust-analyzer.cargo.features": "all"` can be slow — set to `[]` or specific features
- Check RAM: rust-analyzer can use 1–4 GB on large codebases

**"proc macro server crashed"**

```bash
rustup update    # Update toolchain
cargo clean      # Clear build artifacts
```

**Inlay hints disappeared**

`Cmd+Shift+P` → **rust-analyzer: Toggle Inlay Hints**

**Debugger won't attach / CodeLLDB error**

Ensure you built in debug mode (`cargo build`, not `--release`). Check that the binary path in `launch.json` matches. On Apple Silicon, make sure you have the `aarch64-apple-darwin` target installed.

**Linker errors on macOS after Xcode update**

```bash
xcode-select --install    # Reinstall command line tools
```
