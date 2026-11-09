List all port-listening processes

**How it works**

1. Parse `/proc/net/{tcp,tcp6,udp,udp6}` for local address, state, uid, and the socket **inode**.
2. Walk `/proc/<pid>/fd/*` once, `readlink` each fd, and match `socket:[<inode>]` against the wanted set.
3. Join back to `/proc/<pid>/{comm,exe,cmdline}` and render.

**Details worth knowing**

- Addresses in procfs are printed as `%08X` over `__be32` words, so each 4-byte group is byte-swapped on little-endian hosts. The parser reverses per-group, then unmaps `::ffff:a.b.c.d` to plain v4.
- A socket can map to *several* PIDs — forking servers inherit the listening fd, and SO_REUSEPORT lets independent processes bind the same port. Each owner gets its own row rather than being collapsed.
- `cmdline` is NUL-separated; args containing whitespace get `strconv.Quote`d so the output stays unambiguous. Kernel threads (empty cmdline) print as `[comm]`, like `ps`.
- Without root you can't read other users' `fd/` directories, so those sockets still list but with `-`/`?` in the PID/COMMAND columns; it prints a note to stderr in that case.
- `tcp6`/`udp6` absent (kernel or netns without IPv6) is skipped silently via `fs.ErrNotExist`.

**Usage**

```
go build -o lsports . && sudo ./lsports
  -a       all TCP sockets, not just LISTEN
  -udp     include bound UDP sockets
  -4 / -6  address family filter
  -json    machine-readable output
  -proc    alternate procfs mount (useful for testing, or /proc of a netns)
```

Verified against live listeners plus synthetic fixtures for v6/v4-mapped/link-local:

```
PROTO  LOCAL ADDRESS   STATE   USER  PID   COMMAND
tcp    *:2024          LISTEN  root  -     ?
tcp    127.0.0.1:8931  LISTEN  root  1354  python3 -m http.server 8931 --bind 127.0.0.1
udp    *:9413          BOUND   root  1377  python3 -c "import socket..."
```

Two extensions that'd fit naturally given the fd-scan is already there: reading `/proc/<pid>/ns/net` to group results 
by network namespace (relevant if you're chasing container listeners from the host), 
and pulling `/proc/net/unix` for AF_UNIX sockets.
