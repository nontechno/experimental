# bbolt-nfs

An NFSv3 server that exposes a [bbolt](https://github.com/etcd-io/bbolt)
database as a read-write filesystem. Pure Go, no cgo, no FUSE, no kernel
module on the server side — Linux and macOS mount it with their built-in NFS
clients.

| bbolt        | filesystem      |
| ------------ | --------------- |
| bucket       | directory       |
| nested bucket| subdirectory    |
| key          | file name       |
| value        | file content    |

```
$ cat ~/bolt/config/db.yaml
host: localhost
$ echo 'host: prod' > ~/bolt/config/db.yaml   # rewrites the value of key "db.yaml"
$ mkdir ~/bolt/config/tls                     # creates a nested bucket
```

## Build

Requires Go 1.23 or newer.

```bash
go mod download
CGO_ENABLED=0 go build -o bbolt-nfs ./cmd/bbolt-nfs
```

Builds verified for `linux/amd64`, `linux/arm64`, `darwin/amd64` and
`darwin/arm64` with `CGO_ENABLED=0`.

## Run the server

```bash
./bbolt-nfs -db ./data.db -bucket root
```

If the file does not exist, or exists but is zero-length, a fresh bbolt
database is initialized in place at startup; otherwise the existing one is
opened as-is. `-readonly` is the exception, since it has nothing to open and
says so. The server prints the address it is listening on and the exact mount
command for each platform. Useful flags:

| Flag | Meaning |
| --- | --- |
| `-db PATH` | database file (required) |
| `-bucket NAME` | root the filesystem inside this top-level bucket; needed for files at the top level (see below) |
| `-listen ADDR` | default `127.0.0.1:12049` |
| `-readonly` | open the database read-only and reject every write |
| `-uid`, `-gid` | owner reported for every entry (defaults to the server's own) |
| `-file-mode`, `-dir-mode` | permission bits reported (default `0644` / `0755`) |
| `-max-file-size` | largest value, default 8 MiB |
| `-deny-macos-metadata` | refuse `.DS_Store`, `._*` and friends |
| `-log-level` | `error`, `info`, `debug`, `trace` |

**The root bucket matters.** bbolt stores nested buckets at the database root
but no keys, so without `-bucket` the top level of the mount accepts
directories only and `touch /mnt/bolt/file` fails with a permission error.
Files inside subdirectories work either way. Pass `-bucket NAME` to root the
filesystem inside a bucket, and the top level behaves like any other
directory.

## Mount on Linux

```bash
sudo apt install nfs-common          # or dnf install nfs-utils
sudo mkdir -p /mnt/bolt
sudo mount -t nfs -o vers=3,tcp,port=12049,mountport=12049,nolock,soft \
    127.0.0.1:/ /mnt/bolt
```

`port=` and `mountport=` are both required: the server does not register with
`rpcbind`, so the client has to be told where to find it. `nolock` is required
because the server does not implement the NLM locking protocol. `soft` makes
I/O fail with an error instead of hanging if the server goes away.

To let a non-root user own the files, start the server with `-uid $(id -u)
-gid $(id -g)`.

Unmount with `sudo umount /mnt/bolt`.

Persistent mount in `/etc/fstab` (the server must be running before this is
mounted):

```
127.0.0.1:/  /mnt/bolt  nfs  vers=3,tcp,port=12049,mountport=12049,nolock,soft,noauto,user  0  0
```

As a systemd user service, `~/.config/systemd/user/bbolt-nfs.service`:

```ini
[Unit]
Description=bbolt NFS server

[Service]
ExecStart=%h/bin/bbolt-nfs -db %h/data.db -bucket root -uid 1000 -gid 1000
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user enable --now bbolt-nfs
```

## Mount on macOS

```bash
mkdir -p ~/bolt
sudo mount -t nfs -o vers=3,tcp,port=12049,mountport=12049,nolocks,noacl,soft \
    127.0.0.1:/ ~/bolt
```

macOS spells the option `nolocks` (plural), unlike Linux. `noacl` stops the
client asking for ACL attributes the server does not provide. Add
`rsize=65536,wsize=65536` if you move larger files.

Unmount with `sudo umount ~/bolt`, or `diskutil unmount force ~/bolt` if
Finder is holding it.

Finder writes `.DS_Store` and `._*` files into any directory it displays,
which lands them in your database as keys. Either browse from the terminal,
run the server with `-deny-macos-metadata`, or turn the behaviour off
globally:

```bash
defaults write com.apple.desktopservices DSDontWriteNetworkStores -bool true
killall Finder
```

macOS's automounter can mount on demand instead. Add to `/etc/auto_master`:

```
/-  auto_bbolt
```

and create `/etc/auto_bbolt`:

```
/Users/you/bolt -vers=3,tcp,port=12049,mountport=12049,nolocks,noacl,soft 127.0.0.1:/
```

then `sudo automount -cv`.

## Try it

```bash
./bbolt-nfs -db /tmp/demo.db -bucket root &
sudo mount -t nfs -o vers=3,tcp,port=12049,mountport=12049,nolock,soft 127.0.0.1:/ /mnt/bolt

mkdir /mnt/bolt/config
echo 'host: localhost' > /mnt/bolt/config/db.yaml
ls -l /mnt/bolt/config

# the same data, straight out of bbolt
go run go.etcd.io/bbolt/cmd/bbolt@v1.4.3 get /tmp/demo.db root config db.yaml
```

Mounting a subdirectory works too — `127.0.0.1:/config` exports just that
bucket.

## Design

- **Atomicity.** Every filesystem call runs inside a single bbolt
  transaction. A write, truncate, create, remove or rename either completes or
  leaves the database exactly as it was, including a directory rename, which
  copies a whole subtree in one transaction. There is no journal to replay and
  no half-written value after a crash.
- **Serialization.** bbolt allows one writer and many readers, and the
  database itself does the serializing, so concurrent NFS clients cannot
  interleave two writes to the same key.
- **No ambiguity in the mapping.** A bbolt bucket holds keys and nested
  buckets in one namespace, so a name is either a file or a directory —
  exactly like a directory entry. Nothing is escaped, no marker keys are
  added, and an empty directory is just an empty bucket. A database written by
  another program is readable as-is, and everything this server writes is
  ordinary keys and buckets.
- **File handles survive a restart.** Paths up to 62 bytes are encoded
  directly in the NFS handle, so restarting the server does not make an
  existing mount go stale. Handles are framed as
  `[kind][length][payload][padding]` and are always a multiple of 4 bytes:
  an NFS handle is an XDR opaque, and the decoder go-nfs uses reads the
  declared length without skipping the padding after it, so an unaligned
  handle shifts every field parsed after it — ACCESS then returns a garbage
  mask and clients report "permission denied" on a directory that lists
  fine. Longer paths fall back to an in-memory table that
  does not survive a restart; a client holding one of those gets a stale
  handle error and re-resolves the path.
- **Timestamps.** bbolt stores none. The server records modification times in
  memory, which is authoritative because bbolt holds an exclusive lock on the
  file while the server runs. Times are strictly increasing, so a client never
  mistakes changed data for unchanged. A restart moves every time forward,
  which costs one round of cache revalidation.
- **Writes rewrite the whole value.** Each NFS WRITE is a read-modify-write of
  the entire value, so a file written in chunks costs O(n²) copying. Measured
  here with 1 MiB chunks (what a Linux mount sends): 1 MiB at 150 MiB/s, 8 MiB
  at 67 MiB/s, 32 MiB at 19 MiB/s. The database file grows too, since every
  rewrite allocates fresh pages and the freed ones are reused rather than
  returned to the OS — a 32 MiB file left a 148 MiB database. Good for
  configuration and small documents, wrong for bulk data.

## Exclusive create

If the log shows

```
[ERROR] failing create to indicate lack of support for 'exclusive' mode.
```

the file was still created. NFSv3 has three create modes, and a client opening
with `O_EXCL` asks for `EXCLUSIVE` first — a mode whose point is making a
retransmitted create idempotent, which requires the server to persist an
8-byte verifier per file. go-nfs does not implement it and answers
`NFS3ERR_NOTSUPP`; the Linux client then retries with `GUARDED`, which has the
semantics `O_EXCL` actually needs (fail if the name exists). The server logs
the first attempt as an error even though the exchange is ordinary
negotiation, so `bbolt-nfs` downgrades that one message to debug level. Check
that the file appeared before treating it as a failure — and if you see an
`O_EXCL` create genuinely fail on macOS, that would mean its client does not
retry the way Linux does.

## Limitations

- No symlinks, hard links, or file locking (mount with `nolock` / `nolocks`).
  NFSv3 `EXCLUSIVE` create is not implemented either; see above for why that
  is harmless.
- Modes, owners and timestamps set by the client are accepted so that `cp`,
  `touch` and editors work, but they are not stored. Every entry reports the
  uid, gid and mode the server was started with.
- Keys that cannot be filenames — empty, containing `/` or NUL, or longer than
  255 bytes — are skipped when listing and refused when creating. They stay in
  the database untouched; use the bbolt CLI to reach them.
- A file cannot live at the database root without `-bucket`, as described
  above.
- `-max-file-size` (8 MiB by default) rejects larger files with EFBIG; the
  server log names the flag. Raise it if you need to, keeping the cost above
  in mind. Copying a source tree that contains a generated C amalgamation or
  a vendored blob is the usual way to meet this limit.
- Directory renames are bounded by `-max-rename-entries` and
  `-max-rename-bytes`; a larger subtree fails with EFBIG rather than
  half-moving.
- bbolt takes an exclusive file lock, so only one `bbolt-nfs` can serve a
  database, and `-readonly` cannot attach to a file another process already
  has open.

## Security

There is no authentication: NFS AUTH_NULL means anyone who can reach the port
gets full access to the database. Keep `-listen` on loopback. If you need it
on a network, tunnel it (`ssh -L 12049:127.0.0.1:12049 host`) rather than
binding a public address. `-readonly` is the one hard guarantee available,
and it also opens the database file read-only.

## Tests

```bash
go test ./...          # unit tests plus end-to-end tests over the NFS wire protocol
go test -race ./...
```

The end-to-end tests run a real server and drive it with an NFSv3 client,
covering create/read/write/rename/remove, `readdirplus`, `setattr` truncation,
subdirectory mounts, read-only exports, and handle stability across a server
restart. They do not exercise the macOS or Linux kernel clients, which is the
one thing worth trying by hand before relying on this.
