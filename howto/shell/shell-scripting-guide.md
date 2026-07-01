# Shell Scripting Reference Guide

## 1. The skeleton

```bash
#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'
```

- `set -e` — exit immediately if any command fails (nonzero exit).
- `set -u` — error on use of an unset variable, instead of silently treating it as empty.
- `set -o pipefail` — a pipeline (`a | b | c`) fails if *any* stage fails, not just the last one.
- `IFS=$'\n\t'` — restricts word-splitting to newlines/tabs instead of any whitespace, so filenames with spaces don't get mangled in loops. Optional but common in "strict mode" scripts.

Caveats: `set -e` does **not** trigger inside conditionals (`if cmd; then`), inside `&&`/`||` chains, or for the last command of a function called in a condition. It's a safety net, not a guarantee — test your error paths.

---

## 2. trap

`trap` registers a command to run when the shell receives a signal or reaches a particular event.

```bash
trap 'cmd' SIGNAL
```

Common signals/events:

| Signal | When it fires |
|---|---|
| `EXIT` | Always, when the script exits — normal exit, `exit`, or any error (if combined with `set -e`) |
| `ERR` | Whenever a command fails (with `set -e` semantics) |
| `INT` | Ctrl-C (SIGINT) |
| `TERM` | Process killed via `kill` |
| `DEBUG` | Before every command (rarely used, mostly for debugging traps themselves) |

**Cleanup pattern (the most common use):**

```bash
tmpfile="$(mktemp)"
trap 'rm -f "$tmpfile"' EXIT

echo "data" > "$tmpfile"
# tmpfile is removed automatically, however the script exits
```

**Restoring directory on exit:**

```bash
original_dir="$(pwd)"
trap 'cd "$original_dir"' EXIT
```

**Multiple cleanup actions** — build a function and trap that, rather than stacking strings:

```bash
cleanup() {
    rm -f "$tmpfile"
    kill "$bg_pid" 2>/dev/null || true
    echo "cleaned up" >&2
}
trap cleanup EXIT

bg_pid=""
tmpfile=""
```

**Catching Ctrl-C specifically** (e.g. to print a message before the EXIT trap runs):

```bash
trap 'echo "interrupted"; exit 130' INT
```

**Clearing a trap:** `trap - EXIT` removes it.

A `trap ... EXIT` fires for *every* exit path, so it's the right place for anything that must always happen — closing file descriptors, releasing locks, killing background jobs.

---

## 3. `[[ ]]` vs `[ ]` vs `(( ))` vs `$(( ))`

These look similar but do different jobs.

### `[ ]` — POSIX test command
The original, portable test. Actually a *command* (`/usr/bin/[` or a builtin), so it follows normal word-splitting/glob rules — meaning unquoted variables can break it.

```bash
[ "$name" = "pavel" ]   # must quote $name, must use single = 
```

### `[[ ]]` — bash/zsh extended test
A shell keyword, not a command — safer, more features, no need to quote variables to avoid word-splitting (though quoting is still good practice for clarity).

```bash
[[ $name == "pavel" ]]       # == and = both work, no quoting required
[[ -f $file && -r $file ]]   # && / || work directly inside [[ ]]
[[ $name =~ ^p.*l$ ]]        # regex matching — [ ] can't do this at all
```

Use `[[ ]]` by default in bash scripts. Only drop to `[ ]` if you need POSIX `sh` portability (e.g. `#!/bin/sh`, Alpine's busybox ash, dash).

### `(( ))` — arithmetic evaluation context
For numeric comparisons and math — treats its content as a C-like arithmetic expression. Exit status is 0 (true) if the expression is nonzero.

```bash
count=5
if (( count > 3 )); then echo "big"; fi
(( count++ ))
(( total = a + b ))
```

No `$` needed on variables inside `(( ))`, and operators are the familiar `>`, `<`, `==`, `&&`, `||` — not `-gt`, `-lt`, etc.

### `$(( ))` — arithmetic expansion
Same arithmetic engine as `(( ))`, but it *returns a value* instead of an exit status, for use in assignment or substitution.

```bash
result=$(( 5 + 3 ))
echo "next: $(( count + 1 ))"
```

### Quick comparison table

| | Purpose | Returns | Variables need `$`? |
|---|---|---|---|
| `[ ]` | String/file tests (POSIX) | exit status | yes |
| `[[ ]]` | String/file tests (bash) | exit status | yes (but safer unquoted) |
| `(( ))` | Numeric comparison/math | exit status | no |
| `$(( ))` | Numeric expansion | a value | no |

### Common test operators (work in both `[ ]` and `[[ ]]`)

```
-f file       regular file exists
-d dir        directory exists
-e path       exists (any type)
-L path       symlink
-r/-w/-x      readable/writable/executable
-s file       exists and non-empty
-z str        string is empty
-n str        string is non-empty
str1 == str2  string equality   (=  in [ ], == preferred in [[ ]])
str1 != str2  string inequality
-eq -ne       numeric equal/not-equal   (use inside [ ] / [[ ]], not (( )))
-lt -le       numeric less-than / less-or-equal
-gt -ge       numeric greater-than / greater-or-equal
```

---

## 4. Quoting and word-splitting

```bash
files=(*.txt)            # array of matched files
for f in "${files[@]}"; do   # always quote array expansions
    echo "$f"
done
```

- `"$var"` — always quote variable expansions unless you specifically want word-splitting/globbing.
- `"$@"` vs `$@` — `"$@"` expands each positional arg as a separate quoted word (preserves spaces in args); `$@` unquoted does not. Almost always want `"$@"`.
- Use `${var}` over `$var` when adjacent to other text: `"${name}_suffix"`.

---

## 5. Arrays

```bash
arr=(one two three)
echo "${arr[0]}"        # one
echo "${arr[@]}"        # all elements
echo "${#arr[@]}"        # length
arr+=(four)              # append

# associative arrays (bash 4+)
declare -A map
map[host]="localhost"
map[port]=5432
for key in "${!map[@]}"; do
    echo "$key=${map[$key]}"
done
```

---

## 6. Functions, return values, and `local`

```bash
add() {
    local a="$1" b="$2"
    echo "$((a + b))"     # "return" a value via stdout
}
result="$(add 3 4)"

is_valid() {
    [[ -n "$1" ]]          # exit status doubles as boolean return
}
if is_valid "$name"; then echo "ok"; fi
```

`return` only sets an integer exit status (0-255) — for actual data, print to stdout and capture with `$(...)`. Always use `local` for function-scoped variables, or you'll silently clobber caller variables.

---

## 7. Argument parsing with `getopts`

```bash
verbose=0
output=""

while getopts "vo:h" opt; do
    case "$opt" in
        v) verbose=1 ;;
        o) output="$OPTARG" ;;
        h) echo "usage: $0 [-v] [-o file]"; exit 0 ;;
        *) echo "unknown option" >&2; exit 1 ;;
    esac
done
shift $((OPTIND - 1))   # remaining args are now $1, $2, ...
```

`v` and `h` are flags (no argument), `o:` takes an argument (the trailing `:`). `getopts` only handles single-dash short options; for `--long-flags` you generally hand-roll a `while [[ $# -gt 0 ]]; do case "$1" in ...) shift ;; esac done` loop instead.

---

## 8. Subshells vs current shell

```bash
(cd /tmp && ls)     # parens = subshell; cd doesn't affect the parent shell
{ cd /tmp; ls; }    # braces = same shell; cd DOES persist — note required spaces/semicolons
```

Subshells are useful exactly when you want a `cd`, variable change, or `set` option to be local to a block without manual save/restore.

---

## 9. Common pitfalls

- **`set -e` doesn't catch everything** — failures inside `if`, `&&/||`, or command substitution in some contexts are exempt.
- **Unquoted variables** in `[ ]`/`for`/command args break on spaces or globs — quote by default.
- **`$(cmd)` vs `` `cmd` `` — always prefer `$(...)`, nests cleanly, no escaping headaches.**
- **Comparing strings with `-eq`** or numbers with `==` — wrong operator family, will misbehave or error.
- **Forgetting `local`** — leaks variables into global scope, causes hard-to-trace bugs in larger scripts.
- **Mixing `[[` and POSIX `sh`** — `[[` isn't available in `/bin/sh` (dash/busybox); if the shebang says `#!/bin/sh`, stick to `[ ]`.

---

## 10. When to stop writing bash

Once a script needs real data structures, concurrent processing, robust error types, or testability — that's the point to reach for Go instead. Bash is great for orchestration and glue (a few dozen lines, calling other programs, simple control flow); past that, the lack of types and the quoting minefield start costing more time than they save.
