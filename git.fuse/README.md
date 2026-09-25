# gitmount

gitmount mounts one branch of a remote git repository as a read/write FUSE
filesystem. Changes are committed automatically and pushed to the remote.
Upstream changes are pulled in periodically.

It is a single static Go binary. **No git installation is needed:** all git
work (clone, fetch, commit, push) runs in-process through
[go-git](https://github.com/go-git/go-git). At runtime the only external
program it uses is `fusermount3`.

## How it works

gitmount keeps a private clone in `state.dir`. The git metadata
(`state.dir/git`) is stored *outside* the checked-out files
(`state.dir/worktree`). The checked-out files are exposed at `mount.path`
through a go-fuse loopback filesystem, so file behavior is that of the local
disk.

Every mutating FUSE operation takes a shared lock and marks the tree dirty.
That includes write, create, rename, unlink and truncate. Repository
operations take the exclusive lock, so git always sees a consistent tree, and
no FUSE operation is ever half-applied during a commit or a pull. All
repository work runs in one goroutine.

**Commit.** A commit happens once changes have been quiet for
`commit.quiet_period` and no file is open for writing. It also happens
unconditionally once changes are `commit.max_delay` old. Everything git would
track is committed, and `.gitignore` is respected. The commit message lists
the changed files.

**Pull.** gitmount fetches every `sync.fetch_interval` and after each commit.
When upstream has moved, gitmount compares the files each side changed since
the common ancestor:

* *No overlap:* upstream's changed files are written into the work tree, and
  your unpushed changes are re-committed on top of upstream as one commit.
  This is a file-level rebase.
* *Same file changed differently on both sides,* or upstream would overwrite
  a local untracked or ignored file: **nothing is modified**. Pulling is
  suspended until restart and the conflict is logged. Local commits
  continue.

Pulling only happens while no file is open for writing. Replacing files under
an open write handle would silently lose those writes.

**Push.** Pushes are never forced. If a push is rejected because someone else
pushed first, gitmount fetches, integrates, and retries.

## Safety measures

* **Repository internals are unreachable.** The mount contains no `.git`, and
  creating `.git` (any case) anywhere is refused.
* **Symlinks git would reject are refused.** Symlinks named `.gitmodules`,
  `.gitattributes`, `.gitignore` or `.mailmap` are refused, because git
  rejects them.
* **Writes into the work tree are confined.** They go through Go's
  `os.Root`, which refuses anything that would escape the directory or
  follow a symlink.
* **Unsafe paths from the remote are refused.** This covers `..`, absolute
  paths, `.git` components and gitmount's temp-file prefix.
* **Pulled files are written atomically.** Each is written to a temp file,
  fsynced, then renamed into place. Readers never see partial content, and a
  crash can never leave a truncated file for the next commit.
* **Pulls either apply fully or not at all.** Everything that could make a
  pull fail part-way is checked before the first file is touched.
* **Credentials are handled carefully.**
  * They are never read from the URL; a URL containing a password is
    rejected.
  * Secret files must be mode 0600 or stricter.
  * SSH host keys are always verified against an explicit known_hosts file.
* **Permissions are checked against the caller.** Mounts use
  `default_permissions`, so the kernel checks the calling process's
  permissions. This matters with `allow_other`.
* **Nothing bypasses the lock.** Kernel FUSE passthrough, `copy_file_range`
  and ioctls are not exposed.
* **One instance per state directory.** An exclusive `flock` on the state
  dir prevents two instances from sharing it.
* **Crash recovery.** On restart, gitmount removes leftover temp files and
  rebuilds a corrupt index from HEAD. Anything uncommitted is then committed.
* **Graceful shutdown.** On SIGTERM, gitmount retries a clean unmount, then
  falls back to a lazy unmount; processes with open files keep working until
  they close them. It then makes a final commit and push. Unpushed commits
  are pushed on the next start.

## Build

Requires Go 1.25 or newer, because current go-git and some `os.Root`
functions need it.

    go mod tidy        # generates go.sum
    go test ./...
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gitmount .

The binary is static and can be built on any Linux (or cross-compiled) and
copied to the target host.

## Install on Oracle Linux 8/9

    sudo dnf install -y fuse3
    sudo install -m 0755 gitmount /usr/local/bin/
    sudo useradd --system --home-dir /var/lib/gitmount --create-home gitmount
    sudo install -d -o gitmount -g gitmount /srv/config-repo
    sudo install -d /etc/gitmount
    sudo cp gitmount.example.toml /etc/gitmount/config-repo.toml   # then edit it
    sudo cp gitmount@.service /etc/systemd/system/
    sudo systemctl daemon-reload
    sudo systemctl enable --now gitmount@config-repo
    journalctl -u gitmount@config-repo -f

Notes:

* The unit's `ExecStopPost` assumes the mountpoint is `/srv/<instance>`.
  Adjust it if yours differs.
* `allow_other = true` requires `user_allow_other` in `/etc/fuse.conf`.
* **SELinux:** FUSE mounts are labelled `fusefs_t`. Confined services that
  read the mount need their boolean enabled, for example
  `setsebool -P httpd_use_fusefs 1`.

## Credentials

### SSH deploy key (recommended)

    sudo -u gitmount install -d -m 700 /var/lib/gitmount/.ssh
    sudo -u gitmount ssh-keygen -t ed25519 -N '' -C gitmount@$(hostname) \
         -f /var/lib/gitmount/.ssh/config-repo
    # Host key: take it from your git host's published fingerprints, or
    # scan and then verify it against them:
    ssh-keyscan -t ed25519 github.com | sudo -u gitmount tee /var/lib/gitmount/.ssh/known_hosts
    sudo chmod 600 /var/lib/gitmount/.ssh/known_hosts

Add the `.pub` file to the repository as a deploy key with write access,
then set this in the config:

    [auth]
    method = "ssh"
    ssh_key_file = "/var/lib/gitmount/.ssh/config-repo"
    known_hosts_file = "/var/lib/gitmount/.ssh/known_hosts"

For non-standard ports, the known_hosts entry uses the `[host]:port` form.

### HTTPS token

    [repo]
    url = "https://github.com/example/config-repo.git"
    [auth]
    method = "token"
    username = "x-access-token"
    token_file = "/etc/gitmount/config-repo.token"   # mode 0600, owned by gitmount

On Oracle Linux 9 you can keep the token root-owned and let systemd hand it
to the service. Uncomment `LoadCredential=` in the unit and point
`token_file` at `/run/credentials/gitmount@<instance>.service/git-token`.

Use `ca_bundle_file` for an internal CA. HTTPS proxies are taken from the
usual `HTTPS_PROXY`/`NO_PROXY` environment variables; set them with
`Environment=` in the unit.

Secrets are re-read for every fetch and push, so rotation needs no restart.

## Behavior to be aware of

* **Only what git tracks is synced:** file content, the executable bit, and
  symlinks. Other permission bits, ownership, xattrs, timestamps, empty
  directories, and files matched by `.gitignore` stay local only.
* **Files written by gitmount belong to the service user.** Checked-out and
  pulled files are 0644/0755 and owned by the service user, so with
  `allow_other` other users can read them but not write.
* **A file held open for writing forever** (e.g. a log file) is still
  committed every `max_delay`. Upstream changes are not applied until it is
  closed.
* **After a pull, attributes and listings may be briefly stale.** They can
  lag for up to `entry_timeout`/`attr_timeout`. File contents are revalidated
  on each open.
* **Unpushed local commits get squashed** into one when upstream moves ahead,
  and the history is a series of automatic commits.
* **Git submodules are not supported.** The clone or pull fails with a clear
  error.
* **Not for very large repos.** Each commit scans and hashes the work tree
  (go-git's status), which is fine for config-sized repositories but slow for
  very large ones.
* **Supported URLs:** `ssh://`, scp-style `user@host:path`, and `https://`.
  `file://` and local paths are not supported, because go-git needs a git
  binary for those.

## Resolving a conflict

The log shows `pulling suspended ... conflict: ...` with the affected paths.
Local changes are safe: they are committed in the state dir and still visible
in the mount.

To resolve without a git installation on the host:

1. In the mount, make the conflicting files match what you want to keep.
   Copy in upstream's version if you want theirs.
2. Push the same content upstream by any means, for example with the git
   host's web editor or from a workstation clone. Once both sides have the
   same content, the conflict disappears.
3. Restart the service: `systemctl restart gitmount@config-repo`.

For an untracked or ignored local file that upstream now adds, delete or
rename the local file and restart.

To start over from scratch instead, stop the service, copy anything you
still need out of `state.dir/worktree`, delete the state dir, and start
again. A fresh clone is made.

## Tests

    go test ./...          # unit tests: config validation, conflict and path rules

The end-to-end behavior was tested against real SSH (OpenSSH) and HTTPS
(git http-backend) servers, with no git binary visible to gitmount:

* local edits, renames and deletes, exec bit and symlinks
* pulls, including a directory replaced by a file
* diverged histories combined without loss
* pulls deferred while a file is open for writing
* clobber protection for ignored files
* true conflicts, which modify nothing
* recovery after `kill -9`, a corrupt index and leftover temp files
* lazy unmount with open files
* the single-instance lock
* concurrent writers during upstream pushes, under the race detector
