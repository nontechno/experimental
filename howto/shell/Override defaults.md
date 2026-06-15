Here's a clean example showing the pattern, using a hypothetical VM launcher as context (fits your world):

```bash
#!/usr/bin/env bash
set -euo pipefail

# Defaults
CDROM=""
DISK=""
MEMORY=4096
EXTRA_ARGS=()

usage() {
    echo "Usage: $0 [--cdrom <path>] [--disk <path>] [--memory <mb>]"
    exit 1
}

# Parse named arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --cdrom)
            CDROM="${2:?'--cdrom requires a path'}"
            shift 2
            ;;
        --disk)
            DISK="${2:?'--disk requires a path'}"
            shift 2
            ;;
        --memory)
            MEMORY="${2:?'--memory requires a value'}"
            shift 2
            ;;
        --help) usage ;;
        *) echo "Unknown argument: $1"; usage ;;
    esac
done

# Conditionally build the argument list
if [[ -n "$CDROM" ]]; then
    EXTRA_ARGS+=(-drive "file=${CDROM},media=cdrom,readonly=on")
fi

if [[ -n "$DISK" ]]; then
    EXTRA_ARGS+=(-drive "file=${DISK},format=qcow2,if=virtio")
fi

# Always-present args + conditionally accumulated ones
exec qemu-system-x86_64 \
    -m "$MEMORY" \
    -enable-kvm \
    "${EXTRA_ARGS[@]}"
```

**Key techniques used:**

**`EXTRA_ARGS` array** — the idiomatic bash way to accumulate arguments that may contain spaces or special characters. Never use a plain string for this.

```bash
# BAD - breaks on spaces in paths
EXTRA_ARGS="$EXTRA_ARGS -drive file=$DISK"

# GOOD - safe with any path
EXTRA_ARGS+=(-drive "file=${DISK},format=qcow2")
```

**`"${EXTRA_ARGS[@]}"` expansion** — the double-quotes + `@` preserves word boundaries correctly when passing to the final command.

**`${2:?'message'}` syntax** — causes immediate failure with a message if the value argument is missing, rather than silently using an empty string.

**`exec`** — replaces the shell process with `qemu-system-x86_64` rather than forking a child, which is cleaner for the final invocation.

---

A slightly more advanced variant using a flag-only argument (no value) like `--snapshot`:

```bash
SNAPSHOT=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --snapshot) SNAPSHOT=true; shift ;;
        # ...
    esac
done

if [[ "$SNAPSHOT" == true ]]; then
    EXTRA_ARGS+=(-snapshot)
fi
```

This pattern scales well — each `--flag` either sets a variable or pushes directly into `EXTRA_ARGS`, and the conditional block at the end assembles the final command cleanly.
