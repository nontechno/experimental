Here's a complete breakdown of every OpenSSH `ssh` CLI option, grouped by purpose:

---

## Basic Connection

| Flag | Description |
|------|-------------|
| `destination` | `[user@]host` — the target. Can also be a URI like `ssh://user@host:port` |
| `-p port` | Remote port (default: 22). Use when the server listens on a non-standard port |
| `-l login_name` | Username to log in as. Equivalent to `user@host` prefix |
| `-4` | Force IPv4 only |
| `-6` | Force IPv6 only |

---

## Authentication

| Flag | Description |
|------|-------------|
| `-i identity_file` | Private key file to use for pubkey auth. Can be specified multiple times for fallback |
| `-I pkcs11` | PKCS#11 shared library to load keys from (e.g. hardware tokens, smart cards) |
| `-K` | Enable GSSAPI auth + credential forwarding (Kerberos environments) |
| `-k` | Disable GSSAPI credential forwarding |
| `-A` | Forward the local ssh-agent to the remote host — lets you chain hops without copying keys |
| `-a` | Explicitly disable agent forwarding (good for untrusted hosts) |

---

## Port Forwarding & Tunneling

| Flag | Description |
|------|-------------|
| `-L [bind:]lport:host:rport` | **Local forward** — traffic to `localhost:lport` tunnels to `host:rport` via the SSH server. Classic use: expose a remote DB locally |
| `-R [bind:]rport:host:lport` | **Remote forward** — server-side port forwards back to your local machine. Useful for exposing a local service through a firewall |
| `-D [bind:]port` | **Dynamic (SOCKS5)** — turns SSH into a proxy. Your browser can use `localhost:port` as a SOCKS5 proxy, routing all traffic through the remote |
| `-W host:port` | Forward stdin/stdout to `host:port` on the remote. Used for **ProxyJump** plumbing — `ssh` itself becomes the transport layer |
| `-w local_tun[:remote_tun]` | Request **TUN device forwarding** — creates a layer-3 VPN tunnel between client and server |
| `-N` | Don't execute a remote command. Essential when you're only doing port forwarding — keeps the connection alive doing just tunneling |
| `-f` | Go to background after authentication. Combine with `-N` for background tunnel: `ssh -fN -L ...` |
| `-g` | Allow *remote* hosts (not just localhost) to connect to locally forwarded ports — needed when sharing a tunnel with others on the network |

---

## Jump Hosts / Proxying

| Flag | Description |
|------|-------------|
| `-J [user@]host[:port]` | **Jump host** (ProxyJump). SSH to the jump host first, then forward to the destination. Cleaner than `-o ProxyCommand`. Can chain: `-J hop1,hop2` |

---

## Terminal & Session Behavior

| Flag | Description |
|------|-------------|
| `-t` | Force PTY allocation. Required when you need an interactive terminal through a hop, or to run `sudo`, `tmux`, `vim`, etc. via scripted calls |
| `-T` | Disable PTY allocation. Use when piping commands — prevents garbled output from control characters |
| `-e escape_char` | Set the escape character (default `~`). `~.` disconnects, `~C` opens a command line mid-session, `~Z` suspends. Set to `none` for fully transparent sessions |

---

## Multiplexing / Connection Sharing

| Flag | Description |
|------|-------------|
| `-M` | Put client into **master mode** — shares the connection so subsequent `ssh` calls to the same host reuse it instantly |
| `-S ctl_path` | Path to the control socket for a shared connection. Use with `-M` or to connect to an existing master |
| `-O ctl_cmd` | Control a running master: `check`, `forward`, `cancel`, `exit`, `stop` |

---

## Crypto & Protocol

| Flag | Description |
|------|-------------|
| `-c cipher_spec` | Override the cipher (e.g. `aes128-ctr`). Comma-separated priority list |
| `-m mac_spec` | Override MAC algorithm list (message integrity, e.g. `hmac-sha2-256`) |
| `-C` | Enable compression. Useful on slow/high-latency links; counterproductive on fast connections |
| `-Q query_option` | Query supported algorithms: `cipher`, `mac`, `kex`, `key`, `sig`, etc. Great for debugging capability mismatches |

---

## Configuration & Debugging

| Flag | Description |
|------|-------------|
| `-F configfile` | Use an alternate config file instead of `~/.ssh/config`. Handy for project-specific configs or testing |
| `-o option` | Pass any `ssh_config(5)` directive inline, e.g. `-o StrictHostKeyChecking=no` or `-o ConnectTimeout=5` |
| `-G` | Print the effective configuration that would be used for a connection, then exit — great for debugging your `~/.ssh/config` |
| `-v` | Verbose output (debug level 1). Up to `-vvv` for increasingly detailed protocol tracing |
| `-q` | Quiet mode — suppress warnings and diagnostic messages |
| `-E log_file` | Append debug logs to a file instead of stderr. Combine with `-v` |
| `-y` | Send log output to syslog instead of stderr |
| `-V` | Print version and exit |

---

## Network Binding

| Flag | Description |
|------|-------------|
| `-b bind_address` | Source address to use for the outgoing connection. Useful on multi-homed hosts to control which interface is used |
| `-B bind_interface` | Like `-b` but takes an interface name instead of an address |

---

## X11 Forwarding

| Flag | Description |
|------|-------------|
| `-X` | Enable X11 forwarding (sandboxed via SECURITY extension) |
| `-x` | Disable X11 forwarding |
| `-Y` | Enable **trusted** X11 forwarding (no SECURITY restrictions). Needed for some apps but less safe — the remote can capture keystrokes |

---

## Most Useful Day-to-Day Combos

```sh
# Background SOCKS proxy
ssh -fND 1080 user@bastion

# Jump through a bastion to a private host
ssh -J user@bastion user@private-host

# Persistent background local port forward (e.g. RDS over SSH)
ssh -fNL 5432:db.internal:5432 user@bastion

# Debug a failing connection
ssh -vvv -F /dev/null -o StrictHostKeyChecking=no user@host

# Print effective config for a host
ssh -G myhost
```
