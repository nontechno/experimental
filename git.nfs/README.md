# gitnfs

`gitnfs` serves one remote Git branch as a writable NFSv3 filesystem. It uses Go libraries for Git and NFS; no local `git` executable is needed. NFS mutations are serialized, committed to the configured branch, and pushed before the NFS operation reports success. Concurrent external updates that make a push non-fast-forward are reported as errors; the service never force-pushes.

The service exports the configured working tree at NFS path `/`. Git's object database is kept separately in `git_directory`. The working tree and Git metadata persist across restarts. The first startup clones the selected branch; later starts reopen that checkout. Use one service instance per checkout and branch.

## Build and run

Go 1.25 or newer is required. Copy `gitnfs.example.yaml` to a private config file, set the repository, branch, directories, commit identity, and Git authentication, then run:

```sh
chmod 600 gitnfs.yaml
go run . -config gitnfs.yaml
```

Supported Git authentication is public/no-auth, HTTPS token or username/password, and SSH private key with optional passphrase plus a configured `known_hosts_file`. The SSH user defaults to `git`. Keep config and key files readable only by the service account. HTTPS tokens should have only the repository permissions needed to read and push the chosen branch.

For a non-loopback listener, set `allowed_clients` to explicit client CIDRs. The listener rejects connections outside those CIDRs. NFSv3 AUTH_NULL does not authenticate NFS users or encrypt traffic, so expose it only on a trusted private network or through a protected tunnel. The implementation only accepts mounts of `/`.

## Mount

Start the service first. On Linux (including Oracle Linux), with NFS client utilities installed:

```sh
sudo mkdir -p /mnt/project
sudo mount -t nfs -o vers=3,proto=tcp,port=2049,mountport=2049,nolock,noacl server:/ /mnt/project
```

On macOS:

```sh
sudo mkdir -p /Volumes/project
sudo mount -t nfs -o vers=3,tcp,port=2049,mountport=2049,nolocks localhost:/ /Volumes/project
```

Unmount with `umount /mnt/project` or `umount /Volumes/project`.

## Behavior and limits

- Each write and metadata/tree mutation can cause a Git commit and network push. This favors durable, simple write-through behavior over bulk-write performance; workloads that issue many small writes will be slow.
- If the remote is unavailable or rejects a push, the NFS operation returns an error. The local checkout may already contain a commit; subsequent writes retry pushing the current branch tip. No automatic merge or force-push is attempted.
- Repositories containing symbolic links are refused, and creating symlinks over NFS is disabled to prevent paths from escaping the exported directory. Hard links, special files, ownership changes, ACLs, and NFSv4 are not supported.
- Git does not store empty directories, ownership, or timestamps. Empty directories exist in the live checkout but disappear after a restart unless they contain a file; Git tracks only whether a file is executable, not its full Unix permission bits.
- NFS and remote Git writes are not a multi-client transaction system. Do not run multiple service instances against the same work directory or branch.
- This uses `willscott/go-nfs`, whose NFSv3 implementation describes itself as minimally tested. Run it first in a controlled environment and validate the mount/client behavior on the target Linux or macOS release before relying on it for important data.

## Verify

```sh
go test ./...
go vet ./...
GOOS=darwin GOARCH=arm64 go build -buildvcs=false -o /tmp/gitnfs-darwin .
GOOS=linux GOARCH=amd64 go build -buildvcs=false -o /tmp/gitnfs-linux .
```
