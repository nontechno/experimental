## `chpwd` Hook in Zsh

`chpwd` is a special zsh hook function that runs automatically every time the current working directory changes — via `cd`, `pushd`, `popd`, or any other mechanism.

---

### Defining the hook

The simplest form: just define a function named `chpwd`:

```zsh
chpwd() {
    echo "Changed to: $PWD"
}
```

Put this in your `~/.zshrc`. It runs once on every directory change.

---

### Using the hook array (`chpwd_functions`)

Zsh supports an array of functions to call, which is far better than a single `chpwd()` because it lets multiple independent hooks coexist without clobbering each other:

```zsh
# Define your function with any name
_my_chpwd_hook() {
    echo "Now in: $PWD"
    echo "Previous: $OLDPWD"
}

# Register it
chpwd_functions+=(_my_chpwd_hook)
```

**This is the preferred pattern** — plugins, frameworks (oh-my-zsh, prezto), and your own config can all register into `chpwd_functions` independently.

---

### Key variables available inside the hook

| Variable | Value |
|----------|-------|
| `$PWD` | New (current) directory |
| `$OLDPWD` | Previous directory |
| `$0` | Name of the hook function itself |

---

### Practical examples

**Auto-activate a Python virtualenv:**
```zsh
_auto_venv() {
    if [[ -f "$PWD/.venv/bin/activate" ]]; then
        source "$PWD/.venv/bin/activate"
    elif [[ -n "$VIRTUAL_ENV" ]]; then
        deactivate
    fi
}
chpwd_functions+=(_auto_venv)
```

**Print directory contents on cd:**
```zsh
_ls_on_cd() {
    ls --color=auto
}
chpwd_functions+=(_ls_on_cd)
```

**Project-specific env vars (useful for Go work):**
```zsh
_project_env() {
    # Clear previous project env
    unset PROJECT_ROOT

    case "$PWD" in
        $HOME/projects/myapp*)
            export PROJECT_ROOT="$HOME/projects/myapp"
            export GOFLAGS="-mod=vendor"
            ;;
        $HOME/projects/oracle*)
            export PROJECT_ROOT="$HOME/projects/oracle"
            ;;
    esac
}
chpwd_functions+=(_project_env)
```

**Git branch in notification:**
```zsh
_git_cd_info() {
    if git -C "$PWD" rev-parse --is-inside-work-tree &>/dev/null; then
        local branch
        branch=$(git -C "$PWD" branch --show-current 2>/dev/null)
        echo "  git: $branch"
    fi
}
chpwd_functions+=(_git_cd_info)
```

---

### Removing a hook

```zsh
# Remove a specific function from the array
chpwd_functions=(${chpwd_functions:#_my_chpwd_hook})

# Or clear all hooks
chpwd_functions=()
```

---

### Inspecting registered hooks

```zsh
# See what's registered
print -l $chpwd_functions

# See the function body
which _my_chpwd_hook
# or
functions _my_chpwd_hook
```

---

### Triggering behavior notes

- **Does NOT fire** on shell startup for the initial directory
- **Does fire** on `cd -`, `cd ~`, `pushd`, `popd`, and even `cd .`
- **Does NOT fire** if the directory didn't actually change (e.g., `cd $PWD`)
- Runs **synchronously** before the next prompt is drawn — keep it fast

---

### Related hooks

| Hook | When it fires |
|------|---------------|
| `chpwd` / `chpwd_functions` | After any directory change |
| `precmd` / `precmd_functions` | Before each prompt |
| `preexec` / `preexec_functions` | Before each command runs |
| `zshexit` | On shell exit |

The `_functions` array pattern works identically for all of these.
