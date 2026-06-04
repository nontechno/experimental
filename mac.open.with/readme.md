# macos app to open files in their respective apps

**Build & install**
```bash
go build -o openwith .
# optional: put it on $PATH
cp openwith /usr/local/bin/
```

**Usage**
```bash
openwith go /path/to/main.go      # opens in GoLand
openwith py ~/scripts/foo.py      # opens in PyCharm
openwith code ~/.zshrc            # opens in VS Code with --wait
```

**Config file** — looked up in this order:
1. `$OPENWITH_CONFIG` env var
2. `~/.config/openwith/apps.json` (default)

**Two entry formats in `apps.json`:**

```json
{
  "go":   "/Applications/GoLand.app",          // simple
  "code": {                                     // with extra args
    "app":  "/Applications/Visual Studio Code.app",
    "args": ["--wait"]
  }
}
```

**App path behaviour:**
- Path ending in `.app` → dispatched via `open -a <bundle>`, which is the correct macOS way to launch a bundle and handles already-running instances gracefully. Extra `args` are forwarded after `--args`.
- Plain binary path → executed directly with the filename appended, suitable for CLI editors like `nvim`, `hx`, etc.

The file is resolved to an absolute path before being passed, so relative paths like `openwith go ../foo.go` work correctly.
