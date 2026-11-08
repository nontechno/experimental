# sshnfs configuration reference

Every entry in the JSON config file is described here: what it does, its
default, the values it accepts, when you would change it, and what changes when
you do. Each entry can also be set with a command-line flag, shown next to its
name. The one exception is `mounts`, which exists only in the file.

- [How configuration is resolved](#how-configuration-is-resolved)
- [Value formats](#value-formats)
- [Quick reference](#quick-reference)
- [Entries](#entries)
  - [Identity: what to export](#identity-what-to-export)
  - [Authentication](#authentication)
  - [Host key verification](#host-key-verification)
  - [~/.ssh/config integration](#sshconfig-integration)
  - [Jump hosts and proxy commands](#jump-hosts-and-proxy-commands)
  - [Timeouts and liveness](#timeouts-and-liveness)
  - [SFTP tuning](#sftp-tuning)
  - [Reconnection](#reconnection)
  - [NFS server](#nfs-server)
  - [Caching](#caching)
  - [Ownership](#ownership)
  - [Local mount](#local-mount)
  - [Logging](#logging)
  - [Multiple mounts](#multiple-mounts)
- [Complete examples](#complete-examples)
- [Troubleshooting by symptom](#troubleshooting-by-symptom)

---

## How configuration is resolved

Settings are applied in this order; later layers win:

1. **Built-in defaults** (`sshnfs -example-config` prints them).
2. **`~/.ssh/config`**, for the settings it can supply (see
   [~/.ssh/config integration](#sshconfig-integration)). It only fills in what
   nothing below sets.
3. **The config file** given with `-config`.
4. **Command-line flags.**
5. **Entries of a `mounts` array**, which override everything above for that
   one mount. Flags therefore act as defaults shared by all mounts; a key
   written inside a mount entry beats the same flag.

The `~/.ssh/config` layer is special. It applies only to keys that you did
*not* set explicitly in the file, by flag, or in `user@host` on the command
line. "Explicitly" means the key is present, even with its default value: a
config file containing `"port": 22` keeps `Port 2222` in `~/.ssh/config` from
applying.

To see the result of all of this without connecting anywhere:

```sh
sshnfs -config ~/.config/sshnfs.json -print-config
```

`-print-config` replaces secrets with `***`. Its output is a template, not a
working config: a file that still contains `***` is rejected.

Unknown keys are errors, so a typo such as `"mount_piont"` is reported rather
than silently ignored.

## Value formats

| Type | Format | Examples |
|---|---|---|
| string | JSON string | `"buildhost"` |
| integer | JSON number | `22`, `-1` |
| boolean | `true` / `false` | `true` |
| duration | a Go duration string, **or** a number of seconds | `"30s"`, `"1m30s"`, `"500ms"`, `"2h"`, `15`, `0.5` |
| list | JSON array of strings | `["~/.ssh/id_ed25519", "~/.ssh/id_rsa"]` |
| path | string; a leading `~` or `~/` is expanded to your home directory | `"~/.ssh/known_hosts"` |

Duration units: `ns`, `us`, `ms`, `s`, `m`, `h`. For most timeouts `0` has a
specific meaning (disabled, or "use the default"), described with each entry.

---

## Quick reference

| Key | Flag | Default | Section |
|---|---|---|---|
| `name` | `-name` | *(empty)* | [Identity](#name) |
| `host` | `-host` | *(none, required)* | [Identity](#host) |
| `port` | `-port` | `22` | [Identity](#port) |
| `user` | `-user` | your local user name | [Identity](#user) |
| `remote_path` | `-remote` | *(remote login directory)* | [Identity](#remote_path) |
| `identity_files` | `-i` | `[]` (tries `~/.ssh/id_ed25519`, `id_ecdsa`, `id_rsa`) | [Auth](#identity_files) |
| `identity_passphrase` | `-passphrase` | *(empty)* | [Auth](#identity_passphrase) |
| `password` | `-password` | *(empty)* | [Auth](#password) |
| `ask_password` | `-ask-password` | `false` | [Auth](#ask_password) |
| `use_agent` | `-agent` | `true` | [Auth](#use_agent) |
| `known_hosts_file` | `-known-hosts` | `"~/.ssh/known_hosts"` | [Host keys](#known_hosts_file) |
| `insecure_ignore_host_key` | `-insecure` | `false` | [Host keys](#insecure_ignore_host_key) |
| `use_ssh_config` | `-use-ssh-config` | `true` | [ssh config](#use_ssh_config) |
| `ssh_config_file` | `-F` | `"~/.ssh/config"` | [ssh config](#ssh_config_file) |
| `proxy_jump` | `-J` | *(empty)* | [Proxy](#proxy_jump) |
| `proxy_command` | `-proxy-command` | *(empty)* | [Proxy](#proxy_command) |
| `connect_timeout` | `-connect-timeout` | `"15s"` | [Timeouts](#connect_timeout) |
| `op_timeout` | `-op-timeout` | `"30s"` | [Timeouts](#op_timeout) |
| `op_max_time` | `-op-max-time` | `"10m"` | [Timeouts](#op_max_time) |
| `keepalive_interval` | `-keepalive` | `"30s"` | [Timeouts](#keepalive_interval) |
| `keepalive_timeout` | `-keepalive-timeout` | `"15s"` | [Timeouts](#keepalive_timeout) |
| `max_packet` | `-max-packet` | `32768` | [SFTP](#max_packet) |
| `max_concurrent_requests` | `-max-concurrent` | `64` | [SFTP](#max_concurrent_requests) |
| `reconnect` | `-reconnect` | `true` | [Reconnection](#reconnect) |
| `reconnect_delay` | `-reconnect-delay` | `"5s"` | [Reconnection](#reconnect_delay) |
| `reconnect_max_delay` | `-reconnect-max-delay` | `"1m"` | [Reconnection](#reconnect_max_delay) |
| `reconnect_max_attempts` | `-reconnect-attempts` | `0` (forever) | [Reconnection](#reconnect_max_attempts) |
| `listen` | `-listen` | `"127.0.0.1:0"` | [NFS](#listen) |
| `handle_cache_limit` | `-handle-cache` | `1000000` | [NFS](#handle_cache_limit) |
| `read_only` | `-read-only` | `false` | [NFS](#read_only) |
| `require_privileged_port` | `-require-privileged-port` | `true` on macOS, `false` elsewhere | [NFS](#require_privileged_port) |
| `attr_cache_ttl` | `-attr-cache` | `"1s"` | [Caching](#attr_cache_ttl) |
| `dir_cache_ttl` | `-dir-cache` | `"1s"` | [Caching](#dir_cache_ttl) |
| `open_file_idle` | `-open-file-idle` | `"30s"` | [Caching](#open_file_idle) |
| `open_file_limit` | `-open-file-limit` | `64` | [Caching](#open_file_limit) |
| `uid` | `-uid` | your local uid | [Ownership](#uid-and-gid) |
| `gid` | `-gid` | your local primary gid | [Ownership](#uid-and-gid) |
| `mount_point` | `-mount` | *(empty: serve only)* | [Mount](#mount_point) |
| `mount_options` | `-mount-options` | OS-specific, see below | [Mount](#mount_options) |
| `mount_sudo` | `-mount-sudo` | `false` | [Mount](#mount_sudo) |
| `unmount_on_exit` | `-unmount-on-exit` | `true` | [Mount](#unmount_on_exit) |
| `mount_timeout` | `-mount-timeout` | `"1m"` | [Mount](#mount_timeout) |
| `unmount_timeout` | `-unmount-timeout` | `"30s"` | [Mount](#unmount_timeout) |
| `log_level` | `-log-level` | `"info"` | [Logging](#log_level) |
| `mounts` | *(file only)* | *(absent)* | [Multiple mounts](#mounts) |

---

## Entries

### Identity: what to export

#### `name`
**Flag:** `-name` · **Type:** string · **Default:** empty

A label for this mount, used as the prefix of its log lines, e.g.
`[warn] [build] ssh connection lost`.

- **When to set it:** with a `mounts` array, so you can tell the mounts apart
  in the log. With a single mount it is cosmetic.
- **If empty:** the label falls back to the `~/.ssh/config` alias, then
  `host:remote_path`, then `host`, then `#1`, `#2`, ….

```json
"name": "build"
```

#### `host`
**Flag:** `-host`, or the `host` in `user@host:/path` · **Type:** string · **Required**

The SSH server: a DNS name, an IP address, or an alias from `~/.ssh/config`.
An alias is looked up first. If it has a `HostName`, that is where sshnfs
connects, and the alias name is kept for log messages.

```jsonc
// one of:
"host": "buildhost"            // alias or DNS name
"host": "203.0.113.7"          // IPv4
"host": "2001:db8::7"          // IPv6, no brackets here
```

- **Effect:** the connection target. It is also the key used to look up the
  host's key in `known_hosts`, and its other settings in `~/.ssh/config`.

#### `port`
**Flag:** `-port` · **Type:** integer 1–65535 · **Default:** `22`

The SSH port on the server.

- **When to set it:** when the server listens on a non-standard port and
  `~/.ssh/config` does not already say so.
- **Note:** writing `"port": 22` explicitly stops a `Port` line in
  `~/.ssh/config` from applying. Leave the key out if you want ssh config to
  decide.

#### `user`
**Flag:** `-user`, or the `user@` in `user@host:/path` · **Type:** string · **Default:** your local user name

The remote login name.

- **When to set it:** when it differs from your local name and `~/.ssh/config`
  does not provide a `User`.
- **Effect:** remote file permissions are enforced as this user. This is the
  only real access control (see [`uid` and `gid`](#uid-and-gid)).

#### `remote_path`
**Flag:** `-remote`, or the `:/path` in `user@host:/path` · **Type:** string · **Default:** empty (the login directory)

The directory on the server to export. It becomes the root of the mount.

| Value | Exports |
|---|---|
| `""` or omitted | the remote login directory (usually `$HOME`) |
| `"~"` | the same |
| `"~/projects"` | `projects` under the remote home |
| `"work"` | `work` relative to the login directory |
| `"/srv/data"` | that absolute path |

- **Validation at startup:** the path must exist and be a directory. A path on
  a hung remote filesystem fails after `op_timeout` rather than hanging.
- **Effect:** clients cannot reach anything outside this directory; `..` is
  confined to it. Symbolic links are passed through as they are, though, so a
  link pointing outside is followed by the client on its own side.

---

### Authentication

Methods are tried in this order: **ssh-agent → key files → password**.

#### `identity_files`
**Flag:** `-i` (repeatable, or comma separated) · **Type:** list of paths · **Default:** `[]`

Private key files to authenticate with.

- **If empty:** `~/.ssh/id_ed25519`, `~/.ssh/id_ecdsa` and `~/.ssh/id_rsa` are
  tried, whichever exist. A default key that cannot be loaded is skipped.
- **If set:** exactly these keys are used, and a key that cannot be read or
  decrypted is a **fatal** error. A wrong path fails loudly instead of quietly
  falling back to a password.
- **From `~/.ssh/config`:** `IdentityFile` lines are used when this key is not
  set explicitly. Files listed there that do not exist are skipped, as ssh
  does.
- **On the command line:** the first `-i` *replaces* the file's list; further
  `-i` flags add to it.

```jsonc
// one of:
"identity_files": ["~/.ssh/id_ed25519"]
"identity_files": ["~/.ssh/id_work", "~/.ssh/id_legacy_rsa"]
```

#### `identity_passphrase`
**Flag:** `-passphrase` · **Env:** `SSHNFS_PASSPHRASE` · **Type:** string · **Default:** empty

The passphrase for encrypted key files. The same value is tried for every key.

- **When to set it:** when you run sshnfs without a terminal (launchd, cron)
  and your key is encrypted and not in an agent.
- **If empty:** an encrypted key prompts on the terminal during the *first*
  connection only. The decrypted key is kept in memory, so reconnects never
  prompt. Without a terminal the key is skipped (or is fatal if it was listed
  in `identity_files`).
- **Prefer:** ssh-agent. Failing that, the environment variable: it is safer
  than the flag, which other users can see in `ps`, and than storing the
  passphrase in a file.

#### `password`
**Flag:** `-password` · **Env:** `SSHNFS_PASSWORD` · **Type:** string · **Default:** empty

The password for password and keyboard-interactive authentication.

- **When to set it:** only for servers that do not accept keys. Use the
  environment variable rather than the file or the flag.
- **Effect:** offered after the agent and keys. Keyboard-interactive prompts
  are all answered with this value, so a server asking for a one-time code as
  well will fail.

#### `ask_password`
**Flag:** `-ask-password` · **Type:** boolean · **Default:** `false`

Prompt for the password on the terminal when none is configured.

- **When to set it:** interactive use with a password-only server.
- **Effect:** prompts during the first connection only, once per
  `user@host:port`, including jump hosts. The answer is remembered for
  reconnects. A reconnect that would need a password nobody entered fails with
  a message rather than waiting on a prompt nobody can see.

#### `use_agent`
**Flag:** `-agent` · **Type:** boolean · **Default:** `true`

Use the ssh-agent at `$SSH_AUTH_SOCK`.

- **When to turn it off:** when the agent holds many keys and the server
  disconnects after "too many authentication failures", or when you want to
  force a specific key from `identity_files`.
- **Effect:** each exchange with the agent is bounded by `connect_timeout`, so
  a wedged agent fails the handshake rather than hanging it.

---

### Host key verification

#### `known_hosts_file`
**Flag:** `-known-hosts` · **Type:** path · **Default:** `"~/.ssh/known_hosts"`

The file used to verify the server's host key, and that of every jump host.

- **From `~/.ssh/config`:** `UserKnownHostsFile`, when this key is not set
  explicitly. Of several listed files, the first one that exists is used.
- **Effect:** an unknown host fails with the exact `ssh-keyscan` command to add
  it. A changed key fails as a mismatch. Only host key types already on file
  for the host are negotiated, so a server that prefers a different key type
  does not cause a false mismatch.
- **Required** unless `insecure_ignore_host_key` is set.

#### `insecure_ignore_host_key`
**Flag:** `-insecure` · **Type:** boolean · **Default:** `false`

Accept any host key without checking.

- **When to set it:** throwaway test machines, never anything that matters.
  It makes a man-in-the-middle attack trivial.
- **From `~/.ssh/config`:** `StrictHostKeyChecking no` (or `off`) turns it on
  when this key is not set explicitly.
- **Effect:** a warning is logged on every connection.

---

### ~/.ssh/config integration

#### `use_ssh_config`
**Flag:** `-use-ssh-config` · **Type:** boolean · **Default:** `true`

Read host settings from the ssh client configuration.

When on, `host` is looked up as an alias. These keywords fill in the
corresponding entries that you did not set explicitly:

| ssh_config keyword | Fills in |
|---|---|
| `HostName` | the address actually dialed (`host` stays as the alias) |
| `Port` | `port` |
| `User` | `user` |
| `IdentityFile` (repeatable) | `identity_files` |
| `UserKnownHostsFile` | `known_hosts_file` |
| `StrictHostKeyChecking no`/`off` | `insecure_ignore_host_key` |
| `ProxyJump` | `proxy_jump` |
| `ProxyCommand` | `proxy_command` |
| `ConnectTimeout` | `connect_timeout` (a value of `0` is ignored) |
| `ServerAliveInterval` | `keepalive_interval` |

`%h`, `%p`, `%r`, `%n`, `%d`, `%l`, `%L` and `%%` are expanded. `Host` patterns
(`build*`, `!except`) and `Include` work. The user file is read first, then
`/etc/ssh/ssh_config`; the first file to set a keyword wins.

- **When to turn it off:** to make a config file fully self-contained, or to
  rule out ssh config as the cause of surprising behaviour.

#### `ssh_config_file`
**Flag:** `-F` · **Type:** path · **Default:** `"~/.ssh/config"`

Which ssh client config file to read.

- **Effect:** a missing default file is fine. A file you name explicitly that
  does not exist is an error.

---

### Jump hosts and proxy commands

#### `proxy_jump`
**Flag:** `-J` · **Type:** string · **Default:** empty

Reach the server through one or more SSH bastions, like `ssh -J`. Comma
separated, outermost first, each `[user@]host[:port]`:

```jsonc
// one of:
"proxy_jump": "bastion"
"proxy_jump": "jump1,ops@jump2:2222"
"proxy_jump": "[2001:db8::1]:22"
"proxy_jump": "none"
```

- **How hops are resolved:** each hop is looked up in `~/.ssh/config` for its
  `HostName`, `Port`, `User` and `IdentityFile`. Anything still missing comes
  from this mount's own settings. A hop with its own `ProxyJump` is expanded in
  place, so a chain of bastions does not have to be spelled out. Cycles are
  reported, and chains are capped at 8 hops.
- **Shared with the target:** every hop is checked against the same
  `known_hosts_file`, with the same `use_agent`, `identity_passphrase` and
  `connect_timeout`.
- **`"none"`:** cancels a `ProxyJump` inherited from `~/.ssh/config` or from
  the top level of the file.
- **Precedence:** if both are set, `proxy_jump` wins over `proxy_command`.
- **Effect:** no external `ssh` process and no local port forwarding are
  involved. Reconnection rebuilds the whole chain.

#### `proxy_command`
**Flag:** `-proxy-command` · **Type:** string · **Default:** empty

A shell command whose stdin and stdout carry the SSH connection, like ssh's
`ProxyCommand`. It runs through `/bin/sh` with `%h`, `%p` and `%r` expanded.

```jsonc
// one of:
"proxy_command": "cloudflared access ssh --hostname %h"
"proxy_command": "nc -X 5 -x socks.example.com:1080 %h %p"
"proxy_command": "none"
```

- **Effect:** the command's stderr is copied into the log as warnings, which is
  often the only clue when it fails. The handshake over it is still bounded by
  `connect_timeout`.
- **Security:** this executes an arbitrary shell command. Treat config files
  as code.

---

### Timeouts and liveness

Every network and process interaction has a time limit; these entries set
them. The one deliberate exception is a passphrase or password prompt, which
waits for a person, and only during the first connection.

#### `connect_timeout`
**Flag:** `-connect-timeout` · **Type:** duration · **Default:** `"15s"`

The limit for each step of connecting: the TCP connect, each SSH handshake
(target and every jump host), starting the SFTP subsystem, and each ssh-agent
exchange.

- **When to raise it:** high-latency links, or slow `proxy_command`s (an
  interactive SSO login, for example).
- **When to lower it:** you want an unreachable host to fail faster at
  startup.
- **`0` or negative:** means the default, not "no limit". `ConnectTimeout 0` in
  `~/.ssh/config` is ignored for the same reason.
- **Effect:** with N jump hosts a full dial can take up to about
  `(2N + 2) × connect_timeout`.

#### `op_timeout`
**Flag:** `-op-timeout` · **Type:** duration · **Default:** `"30s"`

Give up on an SFTP request when **the server has sent nothing at all** for
this long.

This is a silence timeout, not a duration limit. A listing of a huge directory
over a slow link, which is many round trips, is not cut off as long as answers
keep arriving.

- **Why it exists:** when the exported path is on a hung or locked filesystem
  on the server, `sftp-server` blocks inside that `stat` or `open`, but sshd
  keeps answering keepalives, so the link looks healthy. And `sftp-server`
  handles one request at a time, so one stuck request freezes the whole mount.
- **What happens on timeout:** the SSH session is dropped. The stuck request,
  and anything else in flight, fails with an I/O error, and reconnection
  starts a fresh `sftp-server`. The request is not retried, since the same path
  would most likely stall again.
- **It also bounds:** waiting for a reconnect already in progress, and the
  remote checks at startup.
- **When to raise it:** servers that are legitimately slow to answer single
  requests, such as cold tape or archive storage, or a `stat` on a slow
  network filesystem on the server side.
- **When to lower it:** you would rather see errors quickly than wait.
- **`0`:** waits forever. This brings back the original hang; not recommended.

#### `op_max_time`
**Flag:** `-op-max-time` · **Type:** duration · **Default:** `"10m"`

A hard cap on a single SFTP request, however busy the session is.

- **Why it exists:** some servers answer several requests at once. On those,
  traffic for other files keeps the session from ever going quiet, so
  `op_timeout` alone would never catch one request that is stuck. (OpenSSH's
  `sftp-server` is not one of them.)
- **Effect on timeout:** the same as `op_timeout`.
- **`0`:** no cap.

#### `keepalive_interval`
**Flag:** `-keepalive` · **Type:** duration · **Default:** `"30s"`

How often to send an SSH keepalive (`keepalive@openssh.com`).

- **From `~/.ssh/config`:** `ServerAliveInterval`.
- **Why:** it detects a link that went away, and keeps NAT gateways and
  firewalls from dropping an idle connection.
- **When to lower it:** behind NAT gateways that forget idle connections
  quickly, or to notice drops sooner.
- **`0`:** disables keepalives. An idle dead link is then noticed only on the
  next use.

#### `keepalive_timeout`
**Flag:** `-keepalive-timeout` · **Type:** duration · **Default:** `"15s"`

How long to wait for a keepalive's reply before treating the connection as
lost.

- **Why:** on a link that silently stops delivering packets, a keepalive
  without a limit waits until the kernel gives up on TCP, which can take a
  quarter of an hour.
- **Effect:** worst-case detection of a dead link is about
  `keepalive_interval + keepalive_timeout`.
- **`0` or negative:** means the default.

---

### SFTP tuning

#### `max_packet`
**Flag:** `-max-packet` · **Type:** integer ≥ 1024 · **Default:** `32768`

The largest SFTP data packet, in bytes.

- **When to raise it:** high-bandwidth, high-latency links where throughput
  matters. OpenSSH accepts packets up to about 256 KiB, e.g. `262144`.
- **Caution:** 32768 is the size every SFTP server must support. Larger
  values can fail on other servers.

#### `max_concurrent_requests`
**Flag:** `-max-concurrent` · **Type:** integer · **Default:** `64`

How many SFTP read or write requests may be outstanding at once for a single
file, in large transfers.

- **When to raise it:** long fat pipes, where more requests in flight hide the
  latency.
- **When to lower it:** small or embedded servers that struggle with
  pipelining.

---

### Reconnection

#### `reconnect`
**Flag:** `-reconnect` · **Type:** boolean · **Default:** `true`

Reconnect in the background as soon as the connection is lost.

- **`true`:** an idle mount heals itself. Pooled handles and caches are
  dropped on reconnect, so clients see fresh state.
- **`false`:** nothing happens until the next filesystem request, which then
  reconnects.

#### `reconnect_delay`
**Flag:** `-reconnect-delay` · **Type:** duration · **Default:** `"5s"`

The pause before the first reconnection attempt. Each failed attempt doubles
it, up to `reconnect_max_delay`.

- **When to lower it:** you want the fastest possible recovery.
- **When to raise it:** to avoid hammering a server that is rebooting.

#### `reconnect_max_delay`
**Flag:** `-reconnect-max-delay` · **Type:** duration · **Default:** `"1m"`

The ceiling for the doubling backoff. It must be at least `reconnect_delay`,
unless it is `0`, which means the built-in ceiling of one hour (doubling with no
limit at all would soon wait days between attempts).

#### `reconnect_max_attempts`
**Flag:** `-reconnect-attempts` · **Type:** integer · **Default:** `0`

Stop the background loop after this many failed attempts. `0` means never stop.

- **Effect after giving up:** the next filesystem request still tries to
  reconnect, so the mount can recover later; only the background retrying
  stops.

---

### NFS server

#### `listen`
**Flag:** `-listen` · **Type:** `host:port` · **Default:** `"127.0.0.1:0"`

Where the NFS server listens. Port `0` picks a free port, which is what you
want; the mount command is given the actual port.

- **When to use a fixed port:** when you mount by hand and want the command to
  stay the same, e.g. `"127.0.0.1:20490"`. Two mounts cannot share a fixed
  port; that is rejected at startup.
- **Security:** the NFS server does no authentication. A non-loopback address
  makes the export available to anyone who can reach it, and a warning is
  logged. Keep it on `127.0.0.1`.

#### `handle_cache_limit`
**Flag:** `-handle-cache` · **Type:** integer ≥ 2 · **Default:** `1000000`

How many NFS file handles to remember. An NFS handle is how a client refers to
a file; sshnfs maps each one to a remote path.

- **Too small:** clients get "stale file handle" errors for files whose handle
  was forgotten.
- **When to lower it:** only on very memory-constrained machines; each entry
  is small.

#### `read_only`
**Flag:** `-read-only` · **Type:** boolean · **Default:** `false`

Refuse every modifying operation: write, create, remove, rename, mkdir,
symlink, chmod, chown, chtimes.

- **When to set it:** archives, backups, anything you only read, or as a safety
  net against accidental deletion.
- **Effect:** enforced in sshnfs itself, whatever the remote permissions are.
  Free space is reported as zero.

#### `require_privileged_port`
**Flag:** `-require-privileged-port` · **Type:** boolean · **Default:** `true` on macOS, `false` elsewhere

Accept NFS connections only from source ports below 1024.

- **Why:** loopback keeps the network out, but not other users of the same
  machine; any local account could mount your export. Only root can bind
  ports below 1024, and the kernel's NFS client uses one when mounted with
  `resvport`. So this admits the kernel and turns away ordinary local users.
- **Effect on mounting:** `resvport` is added to the mount options and any
  `noresvport` is removed. A hand-made mount with `noresvport` is refused, and
  the refusal is logged.
- **When to turn it off:** when mounting with a user-space NFS client, which
  cannot use a privileged port, or with a kernel NFS client configured to use
  unprivileged ports.

---

### Caching

go-nfs opens, reads or writes, and closes the file for *every* NFS READ and
WRITE request. Over SFTP that would cost two extra round trips per 128 KiB,
so sshnfs caches.

#### `attr_cache_ttl`
**Flag:** `-attr-cache` · **Type:** duration · **Default:** `"1s"`

How long file attributes (size, times, mode) are reused.

- **When to raise it:** read-mostly data that only you change, on a
  high-latency link. `"30s"` makes `ls -l` and builds much faster.
- **`0`:** always ask the server. Use it when something else changes the
  remote files and you must see changes immediately.
- **Effect:** any change made through sshnfs updates the cache, so your own
  changes are always visible at once. Only changes made *on the server* by
  someone else can appear late, by up to this long.

#### `dir_cache_ttl`
**Flag:** `-dir-cache` · **Type:** duration · **Default:** `"1s"`

How long directory listings are reused. The same advice applies as for
`attr_cache_ttl`. Creating, removing or renaming a name drops the containing
directory's listing at once.

#### `open_file_idle`
**Flag:** `-open-file-idle` · **Type:** duration · **Default:** `"30s"`

How long an unused remote file handle is kept open for reuse.

- **When to lower it:** the server limits open files, or another process on
  the server needs to delete or move files you recently read. Some servers,
  such as Windows OpenSSH, refuse to delete open files.
- **When to raise it:** files are read in bursts with pauses in between.
- **`0`:** handles are never closed for being idle, only when
  `open_file_limit` is exceeded.

#### `open_file_limit`
**Flag:** `-open-file-limit` · **Type:** integer ≥ 1 · **Default:** `64`

How many remote file handles to keep open at most. The least recently used
idle handle is closed first. Handles in active use are never closed, so the
limit can be exceeded briefly.

---

### Ownership

#### `uid` and `gid`
**Flags:** `-uid`, `-gid` · **Type:** integer · **Default:** your local uid and primary gid

The owner and group reported for every file.

- **Why:** the remote ids usually mean nothing locally, so by default every
  file appears to be yours, and tools like `git` and editors don't complain.
- **`-1`:** pass the remote numeric ids through unchanged.
- **This is cosmetic.** Permissions are enforced by the server, as the SSH
  `user`. Reporting files as yours does not let you write files that the
  remote user cannot.

```jsonc
"uid": 501, "gid": 20      // a typical first macOS user
"uid": -1, "gid": -1       // show the server's own ids
```

---

### Local mount

#### `mount_point`
**Flag:** `-mount`, or the second positional argument · **Type:** path · **Default:** empty

The local directory to mount the export on.

- **If empty:** sshnfs only serves, and prints the exact `mount` command for
  you to run.
- **Validation:** the directory must exist and be a directory. It is checked
  with a time limit (`mount_timeout`), so a dead NFS mount left on it by a
  crashed run produces an error suggesting `umount -f` rather than a hang.
- Two mounts cannot share a mount point.

#### `mount_options`
**Flag:** `-mount-options` · **Type:** string · **Default:** OS-specific

The options passed to `mount -t nfs -o`. Defaults:

```
macOS: vers=3,tcp,soft,intr,timeo=30,retrans=3,rsize=131072,wsize=131072,locallocks,resvport,actimeo=1
Linux: vers=3,tcp,soft,timeo=30,retrans=3,rsize=131072,wsize=131072,nolock,noacl
```

`port=` and `mountport=` are always added, with the port actually in use. With
`require_privileged_port`, `resvport` is forced and `noresvport` removed.

The options that matter most:

| Option | Meaning | Advice |
|---|---|---|
| `soft` / `hard` | `soft`: the client gives up and returns an error after `timeo × retrans`. `hard`: it retries forever, and processes hang until the server answers. | **Use `soft`.** With `hard`, any remote stall freezes Finder, shells and editors in the kernel, where sshnfs's timeouts cannot reach them. |
| `timeo=N` | Per-try timeout, in tenths of a second (`30` = 3 s). | Should exceed typical request latency. Keep it below `op_timeout` so the client retries while sshnfs recovers. |
| `retrans=N` | Tries before `soft` gives up. | `3` is fine. |
| `rsize`/`wsize` | Transfer size per request. | `131072` matches go-nfs well. |
| `locallocks` (macOS) / `nolock` (Linux) | Do file locking locally; sshnfs has no NFS lock server. | Keep it, or `flock` fails. |
| `actimeo=N` | Kernel attribute cache, in seconds. | Stacks with `attr_cache_ttl`. Use `0` when you need instant consistency. |
| `resvport` / `noresvport` | Whether the client connects from a privileged port. | Managed for you by `require_privileged_port`. |
| `vers=3,tcp` | NFS version 3 over TCP. | Required; do not change. |

#### `mount_sudo`
**Flag:** `-mount-sudo` · **Type:** boolean · **Default:** `false`

Run `mount` and `umount` through `sudo`.

- **Why:** mounting NFS needs root on macOS. This elevates only those two
  commands, while SSH keeps using *your* agent, keys and `known_hosts`. That is
  usually better than running all of sshnfs as root.
- **Tip:** a sudoers rule letting you run `/sbin/mount` and `/sbin/umount`
  without a password lets it run unattended.

#### `unmount_on_exit`
**Flag:** `-unmount-on-exit` · **Type:** boolean · **Default:** `true`

Unmount when sshnfs stops (Ctrl-C, SIGTERM, SIGHUP, or a fatal error).

- **Why keep it on:** once sshnfs exits, the mount has no server behind it, and
  any access to it errors (`soft`) or hangs (`hard`).
- **If unmounting fails** (e.g. a shell is still `cd`'d inside), the exact
  `umount -f` command to run afterwards is logged.

#### `mount_timeout`
**Flag:** `-mount-timeout` · **Type:** duration · **Default:** `"1m"`

The limit for the `mount` command, and for checking at startup that the mount
point responds.

- **When to raise it:** never needed in normal use. A `sudo` password prompt
  counts against it, so raise it if you type slowly.
- **`0` or negative:** means the default.

#### `unmount_timeout`
**Flag:** `-unmount-timeout` · **Type:** duration · **Default:** `"30s"`

The limit for each unmount attempt. Up to three are made: a plain `umount`,
then forced variants. The commands are killed when their time is up, even if a
child process started by `sudo` lingers.

- **`0` or negative:** means the default.

---

### Logging

#### `log_level`
**Flag:** `-log-level` · **Type:** string · **Default:** `"info"`

One of `panic`, `fatal`, `error`, `warn`, `info`, `debug`, `trace`.

- `info` shows connects, reconnects, mounts and timeouts.
- `debug` adds key selection, dial steps, skipped config and retries that were
  reconciled.
- `trace` adds every NFS request (very verbose).
- **Process-wide:** the top-level value applies to the whole process. A
  `log_level` inside a `mounts` entry has no effect.
- **Effect:** logging never blocks. If stderr stops accepting output, lines are
  dropped and counted instead of stalling the program.

---

### Multiple mounts

#### `mounts`
**File only** · **Type:** array of objects · **Default:** absent

Several exports in one process, each with its own SSH connection, NFS
listener, caches, reconnection loop and log prefix.

**Inheritance:** each entry starts as a copy of the top-level object, so
shared settings are written once. Any key may appear in an entry and overrides
the inherited value for that mount only. Command-line flags apply to every
mount, but a key inside an entry beats them.

**Rules:**

- An entry cannot contain `mounts` (no nesting), and the array cannot be empty.
- A `user@host:/path` on the command line cannot be combined with `mounts`.
- Mount points and fixed listen ports must be unique across entries.
- Startup is all or nothing: if any mount fails to come up, the ones already
  mounted are unmounted and sshnfs exits.

```json
{
  "user": "sport",
  "mount_sudo": true,
  "mounts": [
    { "name": "build", "host": "buildhost", "remote_path": "/srv/work",
      "mount_point": "/Users/sport/mnt/build" },
    { "name": "logs",  "host": "logserver", "remote_path": "/var/log",
      "mount_point": "/Users/sport/mnt/logs", "read_only": true }
  ]
}
```

---

## Complete examples

### Minimal: one host from `~/.ssh/config`

```json
{
  "host": "buildhost",
  "remote_path": "/srv/work",
  "mount_point": "~/mnt/build",
  "mount_sudo": true
}
```

Everything else comes from the defaults and from the `Host buildhost` block in
`~/.ssh/config`.

### Behind a bastion, with a dedicated key

```json
{
  "host": "10.0.4.12",
  "user": "deploy",
  "identity_files": ["~/.ssh/id_deploy"],
  "proxy_jump": "ops@bastion.example.com",
  "remote_path": "/opt/app",
  "mount_point": "~/mnt/app",
  "mount_sudo": true
}
```

### Read-only archive over a slow, high-latency link

```json
{
  "host": "nas.example.com",
  "remote_path": "/volume1/archive",
  "mount_point": "~/mnt/archive",
  "mount_sudo": true,
  "read_only": true,
  "attr_cache_ttl": "60s",
  "dir_cache_ttl": "60s",
  "open_file_idle": "5m",
  "max_packet": 262144,
  "max_concurrent_requests": 128,
  "op_timeout": "60s",
  "mount_options": "vers=3,tcp,soft,intr,timeo=50,retrans=3,rsize=131072,wsize=131072,locallocks,resvport,actimeo=60"
}
```

Long caches are safe here because nothing changes the files; read-only and the
large packet size suit bulk reads; the longer timeouts tolerate the latency.

### Shared, live-edited directory (see others' changes at once)

```json
{
  "host": "devbox",
  "remote_path": "~/shared",
  "mount_point": "~/mnt/shared",
  "mount_sudo": true,
  "attr_cache_ttl": 0,
  "dir_cache_ttl": 0,
  "open_file_idle": "5s",
  "mount_options": "vers=3,tcp,soft,intr,timeo=30,retrans=3,rsize=131072,wsize=131072,locallocks,resvport,actimeo=0"
}
```

### Flaky network (Wi-Fi, VPN, laptop sleep)

```json
{
  "host": "buildhost",
  "mount_point": "~/mnt/build",
  "mount_sudo": true,
  "keepalive_interval": "10s",
  "keepalive_timeout": "10s",
  "reconnect_delay": "1s",
  "reconnect_max_delay": "15s",
  "op_timeout": "20s"
}
```

Dead links are noticed within about 20 seconds and recovery retries quickly.

### Unattended (launchd), no terminal

```json
{
  "host": "buildhost",
  "identity_files": ["~/.ssh/id_unattended"],
  "use_agent": false,
  "mount_point": "/Users/sport/mnt/build",
  "mount_sudo": true,
  "log_level": "info"
}
```

Use a key without a passphrase, or supply `SSHNFS_PASSPHRASE` in the job's
environment. Allow `mount`/`umount` in sudoers without a password. Nothing may
prompt: a prompt without a terminal fails.

### Several hosts, shared credentials

```json
{
  "user": "sport",
  "identity_files": ["~/.ssh/id_ed25519"],
  "mount_sudo": true,
  "log_level": "info",
  "mounts": [
    { "name": "build", "host": "buildhost", "remote_path": "/srv/work",
      "mount_point": "/Users/sport/mnt/build" },
    { "name": "archive", "host": "nas.example.com", "port": 2222,
      "user": "backup", "identity_files": ["~/.ssh/id_nas"],
      "proxy_jump": "bastion.example.com",
      "remote_path": "/volume1/archive",
      "mount_point": "/Users/sport/mnt/archive",
      "read_only": true, "attr_cache_ttl": "30s", "dir_cache_ttl": "30s" }
  ]
}
```

---

## Troubleshooting by symptom

| Symptom | Likely cause | Entries to look at |
|---|---|---|
| Finder or shells freeze on the mount | `hard` in `mount_options` | `mount_options`: use `soft` |
| Requests fail with I/O errors after ~30 s, log says "no reply from the server" | A path on the server is stuck (hung or locked filesystem) | Fix the server side; `op_timeout` controls how long to wait |
| A large directory listing fails | Server silent for longer than `op_timeout` between replies | Raise `op_timeout` |
| "host … is not in known_hosts" | First connection to this host | Run the `ssh-keyscan` command from the message; `known_hosts_file` |
| "too many authentication failures" | The agent offers too many keys | `use_agent: false` and a specific `identity_files` |
| Startup hangs then fails at the mount point | A dead NFS mount from an earlier crash | `sudo umount -f <mount point>`; `mount_timeout` |
| Mount refused, log says "not from a privileged port" | Mounted by hand with `noresvport`, or a user-space client | Use the printed mount command, or `require_privileged_port: false` |
| Changes made on the server show up late | Caching | Lower `attr_cache_ttl`, `dir_cache_ttl` and `actimeo` |
| Slow `ls -l` or builds over a slow link | Caches too short | Raise `attr_cache_ttl` and `dir_cache_ttl` |
| The server refuses to delete files you recently read | Pooled handles keep them open (some servers, e.g. Windows) | Lower `open_file_idle` |
| Mount stays dead after the network returns | Background reconnect gave up | `reconnect_max_attempts: 0`, `reconnect: true` |
| Idle mount drops behind NAT | NAT gateway forgets idle connections | Lower `keepalive_interval` |
| "stale file handle" errors on large trees | Handle cache too small | Raise `handle_cache_limit` |
