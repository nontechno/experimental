# Install via Homebrew (recommended):

```bash
brew install --cask macfuse
```

After installing, macOS will block the kernel extension until you approve it:

1. Go to **System Settings → Privacy & Security**
2. Scroll down — you'll see a message about "System software from developer 'Benjamin Fleischer' was blocked"
3. Click **Allow**
4. You'll likely need to **restart** for the extension to load

To verify it's working after reboot:

```bash
kextstat | grep -i fuse
```


#  sshfs with macFUSE

Install sshfs (it's a separate cask, built against macFUSE):

```bash
brew install --cask sshfs
```

Mount a remote directory:

```bash
mkdir -p ~/remote-mount
sshfs user@host:/remote/path ~/remote-mount \
  -o volname=RemoteMount \
  -o reconnect \
  -o ServerAliveInterval=15 \
  -o ServerAliveCountMax=3 \
  -o follow_symlinks \
  -o defer_permissions
```

Unmount:

```bash
umount ~/remote-mount
# or if busy:
diskutil unmount force ~/remote-mount
```

Common tweaks:
- `-o IdentityFile=~/.ssh/id_ed25519` to pin a key
- `-o Compression=yes` over slow links
- `-o cache_timeout=115` to tune attribute caching
- Add an entry to `~/.ssh/config` for the host so you can just do `sshfs host:path mountpoint`
