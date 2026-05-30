## Zsh Hooks — Complete List

### Directory hooks
| Hook | Fires when |
|------|------------|
| `chpwd` | Current directory changes |

---

### Prompt hooks
| Hook | Fires when |
|------|------------|
| `precmd` | Before each prompt is drawn |
| `preexec` | Before a command is executed (receives the command string as `$1`) |
| `preexec_functions` | Array variant of `preexec` |

---

### Signal / process hooks
| Hook | Fires when |
|------|------------|
| `TRAPINT` | `SIGINT` received (Ctrl+C) |
| `TRAPTERM` | `SIGTERM` received |
| `TRAPEXIT` | Shell exits (any cause) |
| `TRAPDEBUG` | After every simple command (debug trap) |
| `TRAPZERR` | A command exits with non-zero status |
| `TRAP`*`SIG`* | Any signal — define `TRAP` + uppercase signal name |

---

### Completion hooks
| Hook | Fires when |
|------|------------|
| `compsys` init via `compinit` | Completion system initializes |
| `_approximate` | Approximate completion is triggered |

---

### Zle (line editor) hooks
| Hook | Fires when |
|------|------------|
| `zle-line-init` | A new command line is started |
| `zle-line-finish` | The line editor finishes (command accepted) |
| `zle-keymap-select` | Keymap changes (e.g. vi normal ↔ insert mode) |
| `zle-history-line-set` | A history line is loaded into the buffer |
| `zle-isearch-update` | Incremental search buffer updates |
| `zle-isearch-exit` | Incremental search exits |

ZLE hooks are defined and registered differently — via `zle -N`:
```zsh
_my_zle_hook() { ... }
zle -N zle-line-init _my_zle_hook
```

---

### Startup / shutdown hooks
| Hook | Fires when |
|------|------------|
| `zshexit` | Shell exits (alias for `TRAPEXIT` in function form) |

---

### `add-zsh-hook` utility

The cleanest way to register any hook without overwriting existing definitions:

```zsh
autoload -Uz add-zsh-hook

add-zsh-hook chpwd    _my_chpwd
add-zsh-hook precmd   _my_precmd
add-zsh-hook preexec  _my_preexec
add-zsh-hook zshexit  _my_cleanup
```

This is equivalent to appending to the `hook_functions` arrays but handles the autoload and array initialization for you. Prefer this over manually managing `chpwd_functions+=()` etc.

---

### All `_functions` arrays (multi-registration)

Every major hook has a corresponding array:

| Array | Hook |
|-------|------|
| `chpwd_functions` | `chpwd` |
| `precmd_functions` | `precmd` |
| `preexec_functions` | `preexec` |
| `periodic_functions` | `periodic` (fires every `$PERIOD` seconds) |
| `zshaddhistory_functions` | `zshaddhistory` |
| `zshexit_functions` | `zshexit` |

---

### Miscellaneous hooks
| Hook | Fires when |
|------|------------|
| `periodic` | Every `$PERIOD` seconds before a prompt (requires `$PERIOD` to be set) |
| `zshaddhistory` | Before a line is added to history — return `1` to suppress it |
| `zsh_directory_name` | Custom named directory expansion (`~myname`) |

---

### Quick reference: hook vs trap vs zle

- **Named hooks** (`chpwd`, `precmd`, etc.) — define a function with that exact name, or append to `hook_functions`
- **Traps** (`TRAPINT`, `TRAPZERR`, etc.) — define a function with that name; they shadow `trap` builtin behavior
- **ZLE widgets** — must be registered with `zle -N widgetname functionname`
