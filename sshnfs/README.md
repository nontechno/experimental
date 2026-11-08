# sshnfs

Mount remote directories over SSH on macOS without FUSE.

`sshnfs` opens an SFTP session to an SSH server, exposes it as a
[go-billy](https://github.com/go-git/go-billy) filesystem, serves that filesystem
over NFSv3 with [willscott/go-nfs](https://github.com/willscott/go-nfs) on a
loopback port, and then mounts that port locally. macOS has a perfectly good
in-kernel NFS client, so nothing has to be installed in the kernel — this is the
same trick `rclone nfsmount` and FUSE-T use.

Read/write, symlinks, permissions, timestamps and free-space reporting all work.

## Build

```sh
go mod tidy      # resolves go.sum
go build -o sshnfs .

# cross-compile from Linux for an Apple Silicon Mac
GOOS=darwin GOARCH=arm64 go build -o sshnfs-darwin-arm64 .
```

Requires Go 1.22 or newer. No cgo, no kernel extension, no macFUSE.

## Usage

```sh
# sshfs-style: mount a remote directory on a local one
sshnfs -mount-sudo build@buildhost:/srv/work ~/mnt/build

# host may be a ~/.ssh/config alias; HostName, Port, User and IdentityFile
# are taken from there
sshnfs -mount-sudo buildhost:/srv/work ~/mnt/build

# same thing with explicit flags
sshnfs -host buildhost -user build -remote /srv/work \
       -mount ~/mnt/build -mount-sudo

# just serve; mount it yourself (prints the exact mount command)
sshnfs -listen 127.0.0.1:20490 -read-only me@nas:/volume1

# everything from a file
sshnfs -config ~/.config/sshnfs.json
```

The remote path defaults to the login directory, so `sshnfs me@host` exports
`$HOME` on the server. `Ctrl-C` unmounts and disconnects.

### Mounting needs root on macOS

`mount_nfs` is privileged. Either run the whole program under `sudo`, or pass
`-mount-sudo` so only the `mount`/`umount` calls are elevated and the SSH side
keeps using your own agent, keys and `known_hosts`. The latter is usually what
you want.

If you would rather mount by hand, run without `-mount` and use the command
printed at startup:

```
mount -t nfs -o vers=3,tcp,soft,intr,timeo=30,retrans=3,rsize=131072,wsize=131072,\
locallocks,resvport,actimeo=1,port=20490,mountport=20490 127.0.0.1:/ ~/mnt/build
```

Both `port` and `mountport` are required: the server does not register with
rpcbind, so the client has to be told where to find it. `resvport` makes the
kernel connect from a reserved port, which the server requires on macOS (see
Security), and `locallocks` keeps `flock` working client-side (NFSv3 locking
is not implemented server-side).

## ~/.ssh/config

A host given on the command line or in the config file is looked up as an alias
first, so the settings you already keep for `ssh` are reused. These keywords are
honoured, with the usual `%h`/`%p`/`%r`/`%d` token expansion:

`HostName`, `Port`, `User`, `IdentityFile` (repeatable), `UserKnownHostsFile`,
`StrictHostKeyChecking`, `ConnectTimeout`, `ServerAliveInterval`.

`ProxyJump` and `ProxyCommand` are honoured too — see below. Anything else in
the file (`ControlMaster`, `ForwardAgent`, `RemoteCommand`) is ignored. The
user file is read first, then `/etc/ssh/ssh_config`; the
first file to define a keyword wins, as in ssh. `Include` and `Host` patterns
(`build*`, `!except`) work.

ssh config only fills in what you did not state yourself: a flag or a config
file key always wins. `IdentityFile` entries that do not exist on disk are
skipped rather than failing the connection. Use `-F path` to read a different
file (a path that does not exist is an error, unlike the default location) and
`-use-ssh-config=false` to ignore it entirely.

## Jump hosts and proxy commands

A bastion works the way it does with ssh:

```sh
# one hop
sshnfs -J bastion me@internal:/srv/data ~/mnt/data

# a chain, innermost last
sshnfs -J jump1,user@jump2:2222 me@internal:/srv/data ~/mnt/data

# anything that speaks the connection on stdin/stdout
sshnfs -proxy-command "cloudflared access ssh --hostname %h" me@internal:/srv
```

or in the config file, as `proxy_jump` and `proxy_command`.

Each hop is dialed through the one before it and its handshake runs over a
channel on that connection — no local port forwarding, no external `ssh`
process. A hop is itself looked up in `~/.ssh/config`, so `-J bastion` picks up
the `HostName`, `Port`, `User` and `IdentityFile` you already have for
`bastion`; anything still missing falls back to the target's own settings.
Host keys for every hop are checked against the same `known_hosts` file.

A jump host may have proxy settings of its own, and they are expanded in place.
Given

```
Host edge     { HostName … }
Host bastion  { ProxyJump edge }
Host inner    { ProxyJump bastion }
```

`sshnfs inner:/srv/data` dials `edge`, then `bastion` through `edge`, then
`inner` through `bastion` — one flat chain, nothing to spell out. A hop whose
config gives it a `ProxyCommand` instead is reached with that command, provided
nothing precedes it in the chain; if something does, the chain wins and the
command is skipped (logged at debug). Cycles are reported rather than followed,
chains are capped at eight hops and nesting at eight levels.

`ProxyCommand` runs through `/bin/sh` with the usual `%h`, `%p` and `%r` tokens
expanded, and its stderr is surfaced in the log — that output is usually the
only clue when a jump fails. Note that this means a config file can execute an
arbitrary shell command, exactly as ssh's own does.

If both are set, `ProxyJump` wins and `proxy_command` is dropped. Either may be
`"none"` to switch off a value inherited from the enclosing config or from
`~/.ssh/config`.

Reconnection rebuilds the entire chain, and the jump connections are torn down
with the session rather than leaking.

## Reconnection

If the link drops, the transport notices immediately for a clean disconnect and
within `-keepalive` for a black hole. With `-reconnect` (the default) a
background loop then redials on its own: after `-reconnect-delay`, doubling up
to `-reconnect-max-delay`, giving up after `-reconnect-attempts` if that is not
zero. Pooled file handles and cached attributes are dropped on reconnect, so
the client sees fresh state.

An idle mount therefore heals itself rather than waiting for someone to touch
it. With `-reconnect=false` the old behaviour remains: the next request
reconnects.

## Multiple mounts

A config file may carry a `mounts` array. Each entry inherits every setting from
the enclosing object, so shared credentials are written once:

```json
{
  "user": "sport",
  "identity_files": ["~/.ssh/id_ed25519"],
  "mount_sudo": true,
  "mounts": [
    { "name": "build",   "host": "buildhost", "remote_path": "/srv/work",
      "mount_point": "/Users/sport/mnt/build" },
    { "name": "archive", "host": "nas", "remote_path": "/volume1/archive",
      "mount_point": "/Users/sport/mnt/archive", "read_only": true }
  ]
}
```

Every mount gets its own SSH connection, NFS listener, caches and reconnection
loop, and its own log prefix. Flags on the command line apply to all of them as
shared defaults; a positional `host:/path` cannot be combined with a `mounts`
array. Conflicting mount points or fixed listen ports are rejected at startup,
and `"listen": "127.0.0.1:0"` (the default) gives each mount a free port.

If any mount fails to come up, the ones already mounted are unmounted and the
process exits, so you never end up half-mounted. `Ctrl-C` tears all of them
down.

## Configuration

Every setting can come from a JSON config file, a command-line flag, or both.
Precedence is **defaults → ~/.ssh/config → config file → flags**, and flags may
be interspersed with positional arguments.

```sh
sshnfs -example-config > ~/.config/sshnfs.json   # start from the defaults
sshnfs -config ~/.config/sshnfs.json -print-config  # show what would be used
```

See `config.example.json`, and [Config.md](Config.md) for every entry in detail:
what it does, when to set it, example values and their effects. Selected
options:

| Flag | Config key | Meaning |
|---|---|---|
| `-host`, `-port`, `-user`, `-remote` | `host`, `port`, `user`, `remote_path` | what to export |
| `-name` | `name` | label for this mount in logs |
| `-F`, `-use-ssh-config` | `ssh_config_file`, `use_ssh_config` | ssh client config lookup |
| `-J`, `-proxy-command` | `proxy_jump`, `proxy_command` | reach the host through a bastion |
| `-reconnect`, `-reconnect-delay`, `-reconnect-max-delay`, `-reconnect-attempts` | `reconnect`, … | background reconnection |
| `-i`, `-passphrase`, `-ask-password` | `identity_files`, … | authentication |
| `-agent` | `use_agent` | use `$SSH_AUTH_SOCK` (default on) |
| `-known-hosts`, `-insecure` | `known_hosts_file`, `insecure_ignore_host_key` | host key checking |
| `-listen` | `listen` | NFS listen address (default `127.0.0.1:0`) |
| `-read-only` | `read_only` | refuse every mutating operation |
| `-require-privileged-port` | `require_privileged_port` | accept NFS connections only from ports below 1024, i.e. the kernel; mounts with `resvport` (default on for macOS) |
| `-uid`, `-gid` | `uid`, `gid` | squash ownership; `-1` passes the remote ids through |
| `-connect-timeout` | `connect_timeout` | TCP connect, each SSH handshake, SFTP start, ssh-agent exchanges (default `15s`) |
| `-op-timeout` | `op_timeout` | give up on an SFTP request when the server has sent nothing for this long (default `30s`, `0` waits forever) |
| `-op-max-time` | `op_max_time` | give up on an SFTP request after this long, however busy (default `10m`, `0` = no limit) |
| `-keepalive`, `-keepalive-timeout` | `keepalive_interval`, `keepalive_timeout` | keepalive period and how long to wait for its reply (`30s`, `15s`) |
| `-mount-timeout`, `-unmount-timeout` | `mount_timeout`, `unmount_timeout` | `mount` plus the mount-point check, and each `umount` attempt (`60s`, `30s`) |
| `-attr-cache`, `-dir-cache` | `attr_cache_ttl`, `dir_cache_ttl` | metadata caching (`0` disables) |
| `-open-file-idle`, `-open-file-limit` | `open_file_idle`, `open_file_limit` | SFTP handle pool |
| `-mount`, `-mount-options`, `-mount-sudo` | `mount_point`, … | local mount |
| `-log-level` | `log_level` | `error`…`trace` |

Authentication is tried in order: ssh-agent, then `-i` keys (or
`~/.ssh/id_ed25519`, `id_ecdsa`, `id_rsa`), then password. Encrypted keys prompt
on the terminal if no passphrase was supplied. `-i` replaces the list from the
config file; repeating `-i` accumulates.

Secrets can come from `SSHNFS_PASSWORD` and `SSHNFS_PASSPHRASE`, which beats
`-password` on the command line since that is visible in `ps`. `-print-config`
redacts both, so its output is a template rather than a usable config file —
put the real values back before feeding it in.

## Why the caches exist

go-nfs opens, seeks, reads or writes, and closes the file on *every* READ and
WRITE RPC. Over SFTP that would be two extra round trips per 128 KiB. So:

- **Open handles are pooled** per path (separate read-only and read-write
  entries), refcounted, and closed after `-open-file-idle`. Each `billy.File`
  keeps its own offset and does positional I/O, so several concurrent requests
  share one remote handle safely.
- **Attributes and directory listings are cached** for `-attr-cache` /
  `-dir-cache`. Stat and lstat results are kept apart, so following a symlink
  never hides the fact that it is one. Anything that adds or removes a name
  invalidates the containing directory's listing; a plain write invalidates only
  the file's own attributes, so copying a large file does not re-read the
  directory on every 128 KiB. `READDIR` populates the attribute cache, which is
  what makes `ls -l` fast.

If something else is writing to the same remote directory and you need to see
its changes instantly, set both TTLs to `0`.

## Reliability

The SSH transport is kept alive with `keepalive@openssh.com` requests. If the
session dies, the next filesystem operation reconnects transparently, drops the
pooled handles and clears the caches. With `soft` in the mount options the
client returns errors rather than hanging while that happens; switch to `hard`
if you would rather have operations block until the link comes back.

`-connect-timeout` bounds the TCP connect, the SSH handshake *and* the SFTP
subsystem start, so a host that accepts connections and then stalls cannot wedge
the server. A value of `0`, including `ConnectTimeout 0` in `~/.ssh/config`,
means the default (15s), not "no timeout".

`-op-timeout` bounds every SFTP request once connected, including the closing
of file handles. It measures silence, not duration: it fires when the server
has sent nothing at all for that long, so a listing of a huge directory over a
slow link, which is many round trips, is not cut off as long as the answers
keep coming. `-op-max-time` caps a single request regardless. It matters only
for servers that answer several requests at once, where other traffic keeps
the session from ever going silent. This catches a failure that keepalives
cannot see. If the
remote path is on a hung or locked filesystem, `sftp-server` blocks in that
`stat` or `open`, but sshd keeps answering keepalives, so the link looks
healthy. And because `sftp-server` and go-nfs both handle one request at a
time, a single stuck request freezes the whole mount. When a request times out,
the session is dropped: the request (and anything else in flight) fails with
an I/O error, and reconnection brings up a fresh `sftp-server`. The path that
stalled will most likely stall again, so it is not retried. Each further access
to it costs one timeout and one reconnect, and briefly fails other requests that
were in flight at that moment. Set the timeout above your slowest legitimate
operation; `0` restores the old wait-forever behaviour.

Passphrases and passwords are only ever prompted for during the first
connection. Whatever was entered is remembered, per key file and per host, so
a reconnect, which happens in the background, never waits on the terminal.
Use `SSHNFS_PASSPHRASE`, `SSHNFS_PASSWORD` or ssh-agent for keys that were not
needed the first time.

Keepalives wait at most `-keepalive-timeout` for their answer. Without that, a
link that silently stops delivering packets would go unnoticed until the kernel
gives up on the TCP connection, which can take a quarter of an hour. The
connection to ssh-agent shares the handshake's deadline, so a wedged agent
fails the handshake rather than hanging it. A request that finds a dial already
in progress waits for it no longer than `-op-timeout`. The mount point is
checked with `-mount-timeout`, so a dead NFS mount left on it by a crashed run
produces an error that suggests `umount -f` rather than a hang at startup.

Logging never blocks. Lines go through a bounded queue to stderr, and if stderr
stops accepting them (a terminal paused with Ctrl-S, a pipe nobody reads), they
are dropped and counted rather than stalling whatever was logging.

Shutdown closes the SSH connection before the file handles, so `Ctrl-C` does
not wait on a server that has stopped answering, and `mount`/`umount` are
killed after their timeout even when sudo's child holds on. If cleanup still
blocks, for instance `umount` of a `hard` mount, a second `Ctrl-C` exits
immediately. SIGTERM and SIGHUP (the terminal window closing) shut down the
same way; under `nohup` SIGHUP stays ignored. A closed stderr pipe no longer
kills the process. Ctrl-C at a passphrase prompt exits at once and restores the
terminal. A ProxyCommand started on a reconnect, or with no terminal, runs in
its own process group, so Ctrl-C does not cut the connection before the
unmount, and closing it kills everything the command started. A failed dial is remembered for a second, so an unreachable host
does not make every queued request pay its own full timeout.

## Security

The NFS server does no authentication — it accepts `AUTH_NULL` from anyone who
can reach it. It binds `127.0.0.1` by default; **do not** move it to `0.0.0.0`
on a shared machine (a warning is logged if you do).

Loopback keeps the network out, but not other users of the same machine: any
local account could mount your export. So on macOS, `-require-privileged-port`
is on by default. Connections are then accepted only from ports below 1024,
which only root can bind, so only the kernel's NFS client gets in. The mount is
made with `resvport`, and a `noresvport` in your own `mount_options` is dropped.
If you mount by hand, use the command printed at startup, or at least include
`resvport`; a mount with `noresvport` is refused. Turn the check off with
`-require-privileged-port=false`. On Linux it is off by default and can be
turned on the same way. Access control lives entirely on the SSH side: the export
is confined to the remote root you chose, and `..` cannot escape it.

`-uid`/`-gid` default to your own ids so remote files look like yours locally.
This is cosmetic — the server enforces permissions as your SSH user, not as the
uid the client sends.

## Known limitations

- **Retried operations after a dropped connection.** When the connection dies
  mid-request the request is retried once. If the first attempt had already
  taken effect, a remove, rename or symlink is recognised as done rather than
  reported as "no such file" or "exists". An exclusive create can still
  report "exists" for a file it just created.

- **One stuck NFS connection, one goroutine.** go-nfs answers one request at a
  time per client connection. If a client connection dies while a reply is
  being written, go-nfs leaks that connection's goroutine. That is a go-nfs
  issue; the kernel simply opens a new connection.
- **Prompts wait for you.** A passphrase or password prompt during the first
  connection has no timeout, since it waits for a person. It is never shown
  again after that.

- **No server-side locking.** NLM is not implemented; use `locallocks`.
- **No hard links**, no mknod, no fifos — SFTP has no portable equivalent.
- **Modes on creation** are applied with a follow-up `chmod`, because SFTP's
  open carries no mode. A file therefore exists briefly with the server's umask.
- **File ids** are an FNV-64a hash of the path, since SFTP exposes no inodes.
  Renaming a file changes its NFS file id.
- **Atime and mtime** are set together; NFS `SETATTR` for one of them alone will
  move both.
- **COMMIT does not fsync.** go-nfs answers WRITE with `FILE_SYNC` and treats
  COMMIT as already satisfied. SFTP acknowledges every write, so the data has
  reached the remote server, but nothing calls `fsync` there: a crash of the
  remote *host* can lose writes the client was told were stable.
- **Rename replaces the destination**, as NFS requires. `posix-rename@openssh.com`
  does this atomically. If the server lacks that extension the code falls back
  to a plain rename, and only removes an existing destination when the rename
  is refused — never on some other error.
- **Truncation via SETATTR** goes through go-nfs's `O_WRONLY|O_EXCL` open. That
  works on OpenSSH, where `O_EXCL` without `O_CREAT` is ignored; a stricter
  SFTP server may reject it.
- **Symlink targets are returned verbatim**, like a kernel NFS server. An
  absolute target that points inside the export still resolves against the
  client's root, not the mount point — there is no rewriting that would fix
  that, so none is attempted.
- Requires an SFTP subsystem on the server. `statvfs@openssh.com` is used for
  free-space reporting when available; without it the export reports a fixed
  1 TiB. Free space is cached for 5 seconds.

## Tests

`integration_test.go` runs the full stack — real sshd, real SFTP, real NFS RPCs
via `go-nfs-client` — covering read/write round trips over 1 MiB, directory
operations, rename, setattr, symlinks, read-only enforcement, cache
invalidation, uid squashing, path containment, and transparent reconnection
after the SSH session drops. `fixes_test.go` adds regressions for the sharper
edges: create not widening an existing file's mode, `MkdirAll` leaving an
existing directory alone, stat/lstat cache separation, rename semantics, dial
backoff, secret redaction, flag-over-file precedence and pool eviction under
concurrent load. `features_test.go` covers alias resolution and precedence
against a real `ssh_config`, connecting through an alias, background reconnect
after the transport is killed, giving up after the attempt limit, and two
mounts serving different roots with independent read-only settings.
`proxy_test.go` runs real one- and two-hop `ProxyJump` chains and a
`ProxyCommand` against the test sshd, and checks that failures name the hop,
that a stalled proxy command still times out, and that reconnection rebuilds
the chain.

```sh
# against any sshd you can key into on 127.0.0.1:2222
SSHNFS_TEST_USER=$USER SSHNFS_TEST_KEY=~/.ssh/id_ed25519 go test -race ./...
```
