Named parameters in shell scripts can be handled a few different ways depending on the style you want.

---

## `--key=value` style (long options)

```bash
#!/bin/bash
for arg in "$@"; do
  case $arg in
    --name=*)  NAME="${arg#*=}" ;;
    --port=*)  PORT="${arg#*=}" ;;
    --verbose) VERBOSE=1 ;;
    *)         echo "Unknown: $arg" ;;
  esac
done

echo "name=$NAME port=$PORT verbose=$VERBOSE"
```

```bash
./script.sh --name=alice --port=8080 --verbose
```

---

## `--key value` style (space-separated)

```bash
#!/bin/bash
while [[ $# -gt 0 ]]; do
  case $1 in
    --name)    NAME="$2"; shift 2 ;;
    --port)    PORT="$2"; shift 2 ;;
    --verbose) VERBOSE=1; shift ;;
    *)         echo "Unknown: $1"; shift ;;
  esac
done
```

---

## `getopts` (short flags only: `-n alice -p 8080`)

```bash
#!/bin/bash
while getopts "n:p:v" opt; do
  case $opt in
    n) NAME="$OPTARG" ;;
    p) PORT="$OPTARG" ;;
    v) VERBOSE=1 ;;
  esac
done
```

`getopts` is POSIX-standard but only supports single-character flags. For long options (`--name`), use the `while/case` patterns above.

---

## Defaults

Set defaults before your parsing loop:

```bash
NAME="default"
PORT=3000
VERBOSE=0
```

The `--key=value` style is the most common for scripts you'll call from other automation; `--key value` feels more familiar if you're used to Go's `flag` package style.
