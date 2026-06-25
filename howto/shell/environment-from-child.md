You can't do this directly — child processes **cannot modify the parent's environment** by design in Unix. But there are standard workarounds:

## 1. `source` / `.` (simplest)

Run the script in the **current shell process** instead of a subprocess:

```bash
source child.sh
# or equivalently:
. child.sh
```

The child script runs in the same process, so any `export VAR=value` affects the current shell. Downside: the child can also see and modify everything else in your shell.

---

## 2. Output + `eval` (subprocess approach)

Have the child print `export` statements, then `eval` them in the parent:

**child.sh:**
```bash
#!/bin/bash
echo "export FOO=bar"
echo "export BAZ=qux"
```

**parent.sh:**
```bash
eval "$(./child.sh)"
echo "$FOO"  # bar
```

This is clean and subprocess-safe. The child does actual work and only prints the vars it wants to export.

---

## 3. Temp file

Child writes vars to a file; parent sources it:

**child.sh:**
```bash
#!/bin/bash
echo "export FOO=bar" > /tmp/child_env.sh
```

**parent.sh:**
```bash
./child.sh
source /tmp/child_env.sh
echo "$FOO"
```

Useful when the child also has non-env output you don't want to `eval`.

---

## 4. Named pipe / `mktemp` for robustness

Same as #3 but with `mktemp` to avoid collisions in concurrent scripts:

```bash
ENVFILE=$(mktemp)
./child.sh "$ENVFILE"   # child writes: echo "export X=1" >> "$1"
source "$ENVFILE"
rm "$ENVFILE"
```

---

## Which to use?

| Approach | Subprocess? | Stdout free? | Concurrent-safe? |
|---|---|---|---|
| `source` | No | Yes | N/A |
| `eval $(child)` | Yes | No | Yes |
| Temp file | Yes | Yes | With `mktemp` |

**`eval $(./child.sh)`** is the most common pattern for exactly this use case.
