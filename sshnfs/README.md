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
locallocks,noresvport,actimeo=1,port=20490,mountport=20490 127.0.0.1:/ ~/mnt/build
```

Both `port` and `mountport` are required: the server does not register with
rpcbind, so the client has to be told where to find it. `noresvport` avoids
needing a privileged source port, and `locallocks` keeps `flock` working
client-side (NFSv3 locking is not implemented server-side).

## ~/.ssh/config

A host given on the command line or in the config file is looked up as an alias
first, so the settings you already keep for `ssh` are reused. These keywords are
honoured, with the usual `%h`/`%p`/`%r`/`%d` token expansion:

`HostName`, `Port`, `User`, `IdentityFile` (repeatable), `UserKnownHostsFile`,
`StrictHostKeyChecking`, `ConnectTimeout`, `ServerAliveInterval`.

Anything else in the file — `ProxyJump`, `ProxyCommand`, `ControlMaster`,
`ForwardAgent` — is ignored, so a host that only works through a jump host will
not work here. The user file is read first, then `/etc/ssh/ssh_config`; the
first file to define a keyword wins, as in ssh. `Include` and `Host` patterns
(`build*`, `!except`) work.

ssh config only fills in what you did not state yourself: a flag or a config
file key always wins. `IdentityFile` entries that do not exist on disk are
skipped rather than failing the connection. Use `-F path` to read a different
file (a path that does not exist is an error, unlike the default location) and
`-use-ssh-config=false` to ignore it entirely.

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

See `config.example.json`. Selected options:

| Flag | Config key | Meaning |
|---|---|---|
| `-host`, `-port`, `-user`, `-remote` | `host`, `port`, `user`, `remote_path` | what to export |
| `-name` | `name` | label for this mount in logs |
| `-F`, `-use-ssh-config` | `ssh_config_file`, `use_ssh_config` | ssh client config lookup |
| `-reconnect`, `-reconnect-delay`, `-reconnect-max-delay`, `-reconnect-attempts` | `reconnect`, … | background reconnection |
| `-i`, `-passphrase`, `-ask-password` | `identity_files`, … | authentication |
| `-agent` | `use_agent` | use `$SSH_AUTH_SOCK` (default on) |
| `-known-hosts`, `-insecure` | `known_hosts_file`, `insecure_ignore_host_key` | host key checking |
| `-listen` | `listen` | NFS listen address (default `127.0.0.1:0`) |
| `-read-only` | `read_only` | refuse every mutating operation |
| `-uid`, `-gid` | `uid`, `gid` | squash ownership; `-1` passes the remote ids through |
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
the server. A failed dial is remembered for a second, so an unreachable host
does not make every queued request pay its own full timeout.

## Security

The NFS server does no authentication — it accepts `AUTH_NULL` from anyone who
can reach it. It binds `127.0.0.1` by default; **do not** move it to `0.0.0.0`
on a shared machine. Access control lives entirely on the SSH side: the export
is confined to the remote root you chose, and `..` cannot escape it.

`-uid`/`-gid` default to your own ids so remote files look like yours locally.
This is cosmetic — the server enforces permissions as your SSH user, not as the
uid the client sends.

## Known limitations

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
  1 TiB.

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

```sh
# against any sshd you can key into on 127.0.0.1:2222
SSHNFS_TEST_USER=$USER SSHNFS_TEST_KEY=~/.ssh/id_ed25519 go test -race ./...
```
