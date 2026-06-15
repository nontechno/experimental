Several approaches, from simplest to most robust:

---

## 1. Source the include (shares the calling shell's variables)

**parse-args.sh**
```bash
# Sourced script — no shebang, no exec
# Reads from "$@" which must be set by caller before sourcing

CDROM=""
DISK=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --cdrom) CDROM="${2:?}"; shift 2 ;;
        --disk)  DISK="${2:?}";  shift 2 ;;
        *)       echo "Unknown: $1"; exit 1 ;;
    esac
done
```

**main.sh**
```bash
#!/usr/bin/env bash
set -euo pipefail

source ./parse-args.sh "$@"   # $@ is re-set inside sourced script's context

echo "CDROM=$CDROM"
echo "DISK=$DISK"
```

When you `source` a script, it runs in the **current shell** — so variables it sets are directly visible in the caller. Passing `"$@"` re-populates `$@` inside the sourced file's parsing loop.

---

## 2. Source + use a dedicated parsing function

Better for larger scripts — the include can define multiple functions, not just one flat parse block.

**lib/args.sh**
```bash
parse_args() {
    # Uses the caller's $@ passed as function arguments
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --cdrom) CDROM="${2:?}"; shift 2 ;;
            --disk)  DISK="${2:?}";  shift 2 ;;
            *)       echo "Unknown: $1"; return 1 ;;
        esac
    done
}
```

**main.sh**
```bash
#!/usr/bin/env bash
source ./lib/args.sh

CDROM=""
DISK=""

parse_args "$@"   # explicit — reads $@ from main script

echo "CDROM=$CDROM"
```

The function modifies variables in the **caller's scope** (no subshell), so `CDROM`/`DISK` are set directly. This is the most common real-world pattern.

---

## 3. Subshell + export (subprocess, explicit env passing)

If you truly need the include to run as a **separate process** (e.g. it's someone else's script), it can print results and you capture them — but this is awkward for multiple values. More practical: export variables *into* it.

```bash
#!/usr/bin/env bash

# Pass args explicitly; script prints KEY=VALUE lines
eval "$(./parse-args-printer.sh "$@")"

echo "CDROM=$CDROM"
```

**parse-args-printer.sh**
```bash
#!/usr/bin/env bash
while [[ $# -gt 0 ]]; do
    case "$1" in
        --cdrom) echo "CDROM=$2"; shift 2 ;;
        --disk)  echo "DISK=$2";  shift 2 ;;
    esac
done
```

This works but `eval` is a code smell — avoid unless necessary.

---

## Summary

| Approach | Mechanism | Variables visible in caller? | Separate process? |
|---|---|---|---|
| `source script.sh "$@"` | flat sourcing | ✅ directly | ❌ |
| `source` + function | function call | ✅ directly | ❌ |
| subshell + `eval` | stdout capture | ✅ via eval | ✅ |
| subshell + export | env vars | only exported ones | ✅ |

**Recommended:** option 2 — source a lib file that defines a `parse_args` function. Clean, testable, no subprocess overhead, and the function signature makes the intent explicit.
