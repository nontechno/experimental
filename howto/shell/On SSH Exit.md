Q: is there a way to be notified (e.g. run a script) when ssh session ends?

Yes, a few clean approaches:

## 1. Trap in your zsh session (simplest)

Add to `~/.zshrc`:

```zsh
trap 'your-script-here' EXIT
```

This runs when the shell exits for any reason. For SSH specifically, you can check if you're in an SSH session:

```zsh
if [[ -n "$SSH_CONNECTION" ]]; then
    trap 'your-script-here' EXIT
fi
```

## 2. Wrap the `ssh` command in a function

In `~/.zshrc`:

```zsh
ssh() {
    command ssh "$@"
    your-script-here  # runs after ssh exits
}
```

This is the cleanest **client-side** approach — runs after every `ssh` invocation regardless of how it ends (normal exit, timeout, connection drop).

You can get fancy and pass the host to your script:

```zsh
ssh() {
    local host="$@"
    command ssh "$@"
    local exit_code=$?
    your-script-here "$host" "$exit_code"
}
```

## 3. macOS notification via `osascript`

If you want a native macOS notification when the session ends:

```zsh
ssh() {
    command ssh "$@"
    osascript -e 'display notification "SSH session ended" with title "SSH"'
}
```

Or with the host name:

```zsh
ssh() {
    command ssh "$@"
    local code=$?
    osascript -e "display notification \"Session ended (exit $code)\" with title \"SSH: $*\""
}
```

---

**Which approach to use:**
- Want notification for every `ssh` call -> **function wrapper** (option 2/3)
- Want to run cleanup from *within* a remote session's local counterpart -> **trap** (option 1)
- The function wrapper is generally the most reliable since `trap EXIT` in `~/.zshrc` fires for all shell exits, not just SSH-related ones
