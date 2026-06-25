Use `$SHELL` or `#!/proc/self/exe` — but the right approach depends on what "compatible" means:

## 1. Use `$SHELL` (current user's login shell)

```bash
cat > /tmp/child.sh << EOF
#!$SHELL
echo "I run in $SHELL"
EOF
chmod +x /tmp/child.sh
```

`$SHELL` expands at write time, embedding e.g. `/bin/bash` into the shebang.

---

## 2. Use `/proc/self/exe` (exact same binary, Linux-only)

```bash
cat > /tmp/child.sh << 'EOF'
#!/proc/self/exe
echo "hello"
EOF
```

Wait — `/proc/self/exe` is the current process, not the shell. Better:

```bash
SHELL_BIN=$(readlink /proc/$$/exe)
cat > /tmp/child.sh << EOF
#!$SHELL_BIN
echo "running in $SHELL_BIN"
EOF
```

`$$` is the PID of the current shell, so `/proc/$$/exe` resolves to the exact shell binary (e.g. `/usr/bin/bash`, `/bin/dash`).

---

## 3. Detect shebang of the current script

If you want the child to match **the parent script's own shebang** (not the user's login shell):

```bash
SELF_SHELL=$(awk 'NR==1{print substr($1,3)}' "$0")
# Extracts "/bin/bash" from "#!/bin/bash"

cat > /tmp/child.sh << EOF
#!$SELF_SHELL
echo "same shell as parent"
EOF
chmod +x /tmp/child.sh
```

`$0` is the parent script's path. `substr($1,3)` strips the `#!` prefix.

---

## Summary

| Goal | Use |
|---|---|
| Same as user's default shell | `$SHELL` |
| Same binary as currently running | `readlink /proc/$$/exe` |
| Same as parent script's shebang | `awk` on `$0` |

**`readlink /proc/$$/exe`** is usually the most precise — it gives you the actual interpreter binary regardless of symlinks or `$SHELL` being set to something else.
