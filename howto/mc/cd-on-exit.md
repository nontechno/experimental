When MC exits normally, the shell stays in whatever directory it was launched from. To fix this, you need MC's shell wrapper function — it runs MC as usual, then feeds the shell MC's "last dir" file on exit.

## Setup

MC writes its last directory to `~/.config/mc/last_dir` on exit (or `~/.mc/last_dir` on older setups). The wrapper function reads that and `cd`s into it.

### For bash — add to `~/.bashrc`:

```bash
mc() {
    local last_dir_file="${XDG_CONFIG_HOME:-$HOME/.config}/mc/last_dir"
    command mc --printwd="$last_dir_file" "$@"
    if [[ -f "$last_dir_file" ]]; then
        local last_dir
        last_dir=$(cat "$last_dir_file")
        [[ -d "$last_dir" ]] && cd "$last_dir"
    fi
}
```

### For zsh — same thing in `~/.zshrc`, identical syntax works.

### For fish — add to `~/.config/fish/config.fish`:

```fish
function mc
    set last_dir_file "$HOME/.config/mc/last_dir"
    command mc --printwd="$last_dir_file" $argv
    if test -f "$last_dir_file"
        set last_dir (cat "$last_dir_file")
        test -d "$last_dir" && cd "$last_dir"
    end
end
```

## How it works

- `--printwd=<file>` tells MC to write its working directory to that file just before exiting
- The wrapper reads the file and `cd`s into it
- The `command` builtin bypasses the wrapper when actually calling the MC binary (avoids infinite recursion)

## Activate

```bash
source ~/.bashrc   # or ~/.zshrc
```

After that, just type `mc` as usual — when you quit with F10 (or `q`), your shell will be in whichever panel's directory was active.

> **Note:** This only works when MC exits cleanly. Killing it with `kill`/SIGKILL won't trigger the `--printwd` write.
