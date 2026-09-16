# etcd-nfs

An NFSv3 server that exposes an etcd v3 keyspace as a filesystem. It runs in
userspace, needs no kernel extensions, and is mounted with the operating
system's built-in NFS client on macOS and Linux. `/` in keys is the directory
delimiter:

```
etcdctl put /app/config/db 'host=x'   →   <mountpoint>/app/config/db
```

Built on [willscott/go-nfs](https://github.com/willscott/go-nfs) and the
official etcd v3 client.

- [Build](#build)
- [Run the server](#run-the-server)
- [macOS](#macos)
- [Linux](#linux)
- [Access from other machines](#access-from-other-machines)
- [Troubleshooting](#troubleshooting)
- [How keys map to files](#how-keys-map-to-files)
- [Semantics and limits](#semantics-and-limits)
- [Flags](#flags)
- [Development](#development)

## Build

Requires Go 1.24 or newer.

```sh
git clone <this repository> etcd-nfs && cd etcd-nfs
go mod tidy
go build -o etcd-nfs ./cmd/etcd-nfs
```

Run `go build` from the repository root (the directory containing `go.mod`).
For a static Linux binary, e.g. for Alpine or containers:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o etcd-nfs ./cmd/etcd-nfs
```

## Run the server

```sh
./etcd-nfs -endpoints http://10.0.0.5:2379
```

On startup it prints the prefix, the etcd endpoints, the listen address, and
a mount command for the current platform:

```
serving etcd prefix "/" from http://10.0.0.5:2379 on 127.0.0.1:12049
mount with: sudo mount -t nfs -o vers=3,proto=tcp,addr=127.0.0.1,...
```

Common options:

```sh
# expose only one subtree of the keyspace (recommended on shared clusters)
./etcd-nfs -endpoints http://10.0.0.5:2379 -prefix /nfs/

# look, don't touch
./etcd-nfs -endpoints http://10.0.0.5:2379 -readonly

# TLS and authentication, same flags and variables as etcdctl
./etcd-nfs -endpoints https://etcd.example:2379 \
  -cacert ca.pem -cert client.pem -key client-key.pem -user alice:secret
```

`ETCDCTL_ENDPOINTS`, `ETCDCTL_CACERT`, `ETCDCTL_CERT`, `ETCDCTL_KEY`,
`ETCDCTL_USER` and `ETCDCTL_PASSWORD` are honored, so an environment that
works for `etcdctl` works here.

> **The mount is the keyspace.** With the default `-prefix /`, every key that
> starts with `/` is visible, and `rm -r` on the mount deletes real keys. Use
> `-prefix` or `-readonly` when the cluster holds anything else.

The MOUNT and NFS protocols share one TCP port (default `12049`), so no
`rpcbind`/portmapper is involved. Keep the server running for as long as the
filesystem is mounted.

## macOS

### Mount

```sh
mkdir -p ~/etcd
sudo mount -t nfs -o vers=3,tcp,port=12049,mountport=12049,nolocks,noacl,soft \
  127.0.0.1:/ ~/etcd
```

| option | why |
|---|---|
| `vers=3,tcp` | the server speaks NFSv3 over TCP only |
| `port=…,mountport=…` | MOUNT and NFS share one port; no portmapper lookup |
| `nolocks` | NLM locking is not served |
| `noacl` | skip the NFS ACL side protocol |
| `soft` | I/O fails instead of hanging if the server stops |

Mount a subtree by naming it: `127.0.0.1:/app/config`.

Entries are reported as owned by the user running `etcd-nfs` (see `-uid`,
`-gid`), so run the server as yourself and the files in `~/etcd` are yours.

Some applications (SQLite, some editors) insist on file locks. For those,
replace `nolocks` with `locallocks`: locks are then enforced on this Mac only,
not across machines.

### Unmount

```sh
sudo umount ~/etcd
diskutil unmount force ~/etcd      # if the server is already gone
```

### Finder metadata

Finder writes `.DS_Store` files, and copying files with extended attributes
creates `._name` AppleDouble files; both become keys in etcd. To avoid them:

```sh
./etcd-nfs -deny-macos-metadata ...                              # refuse them on the server
defaults write com.apple.desktopservices DSDontWriteNetworkStores true  # no .DS_Store on network volumes
cp -X src ~/etcd/dst                                              # copy without extended attributes
```

### Start the server at login

`~/Library/LaunchAgents/io.github.etcd-nfs.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.github.etcd-nfs</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/etcd-nfs</string>
    <string>-endpoints</string><string>http://10.0.0.5:2379</string>
    <string>-deny-macos-metadata</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/tmp/etcd-nfs.log</string>
</dict>
</plist>
```

```sh
launchctl load ~/Library/LaunchAgents/io.github.etcd-nfs.plist
```

Then mount as above.

## Linux

### Install the NFS client

| distribution | package |
|---|---|
| Debian, Ubuntu | `apt install nfs-common` |
| Fedora, RHEL, Rocky | `dnf install nfs-utils` |
| Alpine | `apk add nfs-utils` |
| Arch | `pacman -S nfs-utils` |

The package is optional if you use the mount command below: it passes the
server address explicitly (`addr=`, `mountaddr=`), which lets the kernel mount
without the `mount.nfs` helper. The kernel needs NFS client support
(`modprobe nfs`).

### Mount

```sh
sudo mkdir -p /mnt/etcd
sudo mount -t nfs \
  -o vers=3,proto=tcp,addr=127.0.0.1,port=12049,mountaddr=127.0.0.1,mountport=12049,mountproto=tcp,nolock,noacl,soft \
  127.0.0.1:/ /mnt/etcd
```

| option | why |
|---|---|
| `vers=3,proto=tcp` | NFSv3 over TCP only; otherwise the client tries v4 first |
| `addr=`, `mountaddr=` | server IP; required when `mount.nfs` is not installed |
| `port=`, `mountport=`, `mountproto=tcp` | MOUNT and NFS share one port; no rpcbind lookup |
| `nolock` | NLM locking is not served; without it the mount waits for `rpc.statd` |
| `noacl` | skip the NFS ACL side protocol |
| `soft` | I/O fails instead of hanging if the server stops |

Replace all three `127.0.0.1` with the server's IP when it runs elsewhere, and
use `proto=tcp6,mountproto=tcp6` for an IPv6 address. Mount a subtree by
naming it: `127.0.0.1:/app/config`.

Check it:

```sh
ls -la /mnt/etcd
echo hello > /mnt/etcd/test.txt
etcdctl get /test.txt              # key = -prefix + path
```

### Unmount

```sh
sudo umount /mnt/etcd
sudo umount -l /mnt/etcd           # if the server is already gone
```

### Seeing changes made directly in etcd

The Linux client caches attributes for up to 60 seconds, so a value changed
with `etcdctl put` can look stale through the mount for a while (writes made
through the mount are unaffected). Trade performance for freshness with
`actimeo=1` (revalidate every second) or `noac` (no attribute cache).

### Mount at boot with systemd

`/etc/systemd/system/etcd-nfs.service`:

```ini
[Unit]
Description=etcd-nfs NFS server
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/etcd-nfs -endpoints http://10.0.0.5:2379
Restart=on-failure
DynamicUser=yes

[Install]
WantedBy=multi-user.target
```

`/etc/fstab`:

```
127.0.0.1:/  /mnt/etcd  nfs  vers=3,proto=tcp,addr=127.0.0.1,port=12049,mountaddr=127.0.0.1,mountport=12049,mountproto=tcp,nolock,noacl,soft,_netdev,x-systemd.requires=etcd-nfs.service  0 0
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now etcd-nfs
sudo mount /mnt/etcd
```

With `DynamicUser=yes`, entries are owned by a transient uid; add `-uid` and
`-gid` to `ExecStart` to report a real owner.

### Mount at boot on Alpine (OpenRC)

`/etc/init.d/etcd-nfs`:

```sh
#!/sbin/openrc-run
name="etcd-nfs"
command="/usr/local/bin/etcd-nfs"
command_args="-endpoints http://10.0.0.5:2379"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"
output_log="/var/log/etcd-nfs.log"
error_log="/var/log/etcd-nfs.log"

depend() {
	need net
	before netmount
}
```

```sh
chmod +x /etc/init.d/etcd-nfs
rc-update add etcd-nfs default
rc-update add netmount default
rc-service etcd-nfs start
```

Add the same `/etc/fstab` line as above without
`x-systemd.requires=etcd-nfs.service`; `netmount` mounts it after the server
starts.

### Containers

Mounting inside a container needs `--privileged` or `--cap-add SYS_ADMIN`, and
the `nfs` module must be loaded on the host kernel.

## Access from other machines

The server uses NFS null authentication and listens on loopback by default.
To serve other hosts, either tunnel it:

```sh
ssh -N -L 12049:127.0.0.1:12049 user@server    # then mount 127.0.0.1 locally
```

or listen on the network and restrict the port with a firewall:

```sh
./etcd-nfs -listen 0.0.0.0:12049 -endpoints http://10.0.0.5:2379
```

## Troubleshooting

| symptom | cause and fix |
|---|---|
| `fsconfig() failed: NFS: Server address does not match proto= option` (Linux) | `mount.nfs` is not installed and the options lack the address. Add `addr=IP,mountaddr=IP`, or install the NFS client package. |
| `Permission denied` on `cd`/`ls` right after mounting | a build from before the file-handle alignment fix. Rebuild, restart the server, remount. |
| `Stale file handle` | a handle for a path longer than 63 bytes outlived a server restart, or the mount predates an upgrade. Unmount (`umount -l` / `diskutil unmount force`) and mount again. |
| `mount: ... Connection refused` | `etcd-nfs` is not listening at that address and port. |
| server exits with `connecting to etcd at ...: context deadline exceeded` | etcd is unreachable at `-endpoints`. Check with `etcdctl --endpoints=... endpoint health`. |
| `unknown filesystem type 'nfs'` / `No such device` (Linux) | NFS client kernel support missing: `modprobe nfs`. |
| writes fail with `File too large` | the file would exceed `-max-file-size` (one etcd value). |
| `mv` of a directory fails with an I/O error | the subtree does not fit in one etcd transaction (`-max-txn-ops`, `-max-request-bytes`). |
| files changed with `etcdctl` look stale (Linux) | attribute caching; mount with `actimeo=1` or `noac`. |

For anything else, run the server with `-log-level debug`: it logs every NFS
request and the error it returned.

## How keys map to files

Relative to `-prefix` (default `/`):

| key | presented as |
|---|---|
| `<prefix>a/b/c` | regular file `a/b/c`; its content is the value |
| `<prefix>a/b/` | marker for directory `a/b` (empty value) |

- Directories are implicit: `a` and `a/b` exist while any key starts with
  `<prefix>a/b/`. `mkdir` writes a marker so empty directories persist.
- Removing or moving the last entry out of a directory writes its marker, so
  the directory does not vanish from under a client.
- If both `<prefix>a` and `<prefix>a/…` exist, `a` is a directory and the value
  of `<prefix>a` is hidden.
- Keys with empty or dot segments (`a//b`, `a/./b`) are not listed.
- The root prefix's own key and keys outside the prefix are never shown.

## Semantics and limits

- **Atomic, concurrent-safe mutations.** Create, write, truncate, remove and
  rename are each one etcd transaction guarded by revision compares, retried
  with jittered backoff on conflict. Concurrent writers (other mounts,
  `etcdctl`) never lose each other's updates.
- **Whole-value writes.** Each NFS WRITE rewrites the key's full value, so
  files are capped at `-max-file-size` (default 1 MiB, below etcd's default
  1.5 MiB request limit). This is a filesystem view of configuration-sized
  data, not bulk storage.
- **Directory rename** moves the whole subtree in one transaction; it must fit
  in `-max-txn-ops` and `-max-request-bytes`. etcd cannot detect keys deleted
  from a range, so a delete that races a directory rename can reappear at the
  destination.
- **Leases** attached to keys are preserved across writes and renames.
- **Listings** read keys only, pinned to one revision, and skip over each
  subdirectory's contents, so cost scales with the number of entries shown.
- **Timestamps.** etcd stores none. A file's mtime is the time the server
  first observed the key's revision: stable while unchanged, strictly later
  after any change, which keeps client caches correct. A directory's mtime
  advances with any write to the cluster. Times reset to the server's start
  time after a restart.
- **File handles** encode the path (up to 63 bytes), so mounts survive server
  restarts. Longer paths use an in-memory table and go stale on restart.
- **Metadata.** chmod, chown and utimes succeed but are not stored. Owner and
  modes come from `-uid`, `-gid`, `-file-mode`, `-dir-mode`.
- **`df`** shows etcd's database size against its quota.
- **Not supported:** symlinks, hard links, device files, NLM locks, NFSv4,
  authentication, and EXCLUSIVE-mode create (UNCHECKED and GUARDED work).

## Flags

| flag | default | description |
|---|---|---|
| `-listen` | `127.0.0.1:12049` | NFS and MOUNT listen address |
| `-endpoints` | `$ETCDCTL_ENDPOINTS` or `http://127.0.0.1:2379` | comma-separated etcd endpoints |
| `-cacert`, `-cert`, `-key` | `$ETCDCTL_CACERT` … | etcd TLS files |
| `-user` | `$ETCDCTL_USER` | `user[:password]`; password may come from `$ETCDCTL_PASSWORD` |
| `-prefix` | `/` | etcd key prefix mapped to the filesystem root |
| `-readonly` | `false` | reject all modifications |
| `-uid`, `-gid` | server's uid/gid | owner reported for all entries |
| `-file-mode`, `-dir-mode` | `0644`, `0755` | permission bits reported |
| `-max-file-size` | `1048576` | maximum file size in bytes |
| `-max-request-bytes` | `1572864` | must match etcd `--max-request-bytes` |
| `-max-txn-ops` | `128` | must match etcd `--max-txn-ops` |
| `-quota-bytes` | `2147483648` | size reported by `df` if etcd does not report its quota |
| `-deny-macos-metadata` | `false` | refuse `.DS_Store`, `._*` and similar |
| `-dial-timeout` | `5s` | etcd connection timeout |
| `-request-timeout` | `10s` | timeout per filesystem operation |
| `-log-level` | `info` | `error`, `warn`, `info`, `debug`, `trace` |

## Development

```
cmd/etcd-nfs/   command-line server
etcdfs/         go-billy filesystem over etcd
  fs.go           options, lookup, shared helpers
  open.go         open, create, truncate
  file.go         file handles: read, write, seek
  dir.go          listing, mkdir, remove
  rename.go       file and directory rename
  keys.go         path <-> key mapping
  clock.go        revision -> timestamp mapping
  fileinfo.go     attributes
nfsserver/      go-nfs handler: mounts, file handles, FSSTAT
```

Tests run against a real etcd and are skipped when `ETCD_ENDPOINTS` is unset:

```sh
etcd --data-dir /tmp/etcd-test &
ETCD_ENDPOINTS=http://127.0.0.1:2379 go test -race ./...
```

`etcdfs` tests cover the filesystem directly: paging and subtree skipping in
large directories, shadowed and unrepresentable keys, rename edge cases,
leases, and concurrent writers. `nfsserver` tests speak the NFSv3 wire
protocol with go-nfs-client: mounts and sub-mounts, multi-RPC reads and
writes, READDIRPLUS, mkdir/rmdir/rename, ACCESS and SETATTR on handles of
every length, handle stability across restarts, and FSSTAT.

Tested with the Linux kernel NFS client (Alpine). The macOS instructions use
the standard `mount_nfs` options for userspace NFSv3 servers.
