# Mounting QCOW2 Images on Oracle Linux

## Prerequisites

### Install required packages

```bash
sudo dnf install qemu-img libguestfs-tools
```

For kernel module support (NBD — Network Block Device):

```bash
sudo dnf install qemu-kvm
```

### Load the NBD kernel module

```bash
sudo modprobe nbd max_part=8
```

To make it persistent across reboots:

```bash
echo "nbd max_part=8" | sudo tee /etc/modules-load.d/nbd.conf
```

> **Oracle Linux note:** On OL8/OL9, the `nbd` module may not be included in the default UEK (Unbreakable Enterprise Kernel) initramfs. If `modprobe nbd` fails, verify the module exists:
> ```bash
> find /lib/modules/$(uname -r) -name 'nbd.ko*'
> ```
> If missing, try installing `kernel-uek-modules` or switch to the RHCK kernel.

---

## Method 1: NBD (Network Block Device) — Most flexible

Best for: full disk images with partition tables, LVM, etc.

### Mount

```bash
# Connect the image to an NBD device
sudo qemu-nbd --connect=/dev/nbd0 /path/to/image.qcow2

# Check what partitions are inside
sudo fdisk -l /dev/nbd0
lsblk /dev/nbd0

# Mount a specific partition (e.g., partition 1)
sudo mkdir -p /mnt/qcow2
sudo mount /dev/nbd0p1 /mnt/qcow2

# If LVM is inside the image:
sudo vgscan
sudo vgchange -ay
sudo mount /dev/VolumeGroupName/LogicalVolumeName /mnt/qcow2
```

### Unmount

```bash
sudo umount /mnt/qcow2

# If LVM was used:
sudo vgchange -an VolumeGroupName

# Disconnect the NBD device — critical step
sudo qemu-nbd --disconnect /dev/nbd0
sudo rmmod nbd   # optional, if you want to unload the module
```

> ⚠️ Always disconnect NBD before removing the module or the image file. Skipping `--disconnect` can corrupt the image.

---

## Method 2: `guestmount` (libguestfs) — Safer, no root needed

Best for: inspecting images without modifying the host's device tree; works even without NBD.

```bash
# Install if not already done
sudo dnf install libguestfs-tools

# Mount (read-only is strongly recommended for safety)
sudo guestmount -a /path/to/image.qcow2 -i --ro /mnt/qcow2
# -i = inspect and auto-detect root partition
# --ro = read-only (omit if you need write access)
```

### Unmount

```bash
sudo guestunmount /mnt/qcow2
# or
sudo fusermount -u /mnt/qcow2
```

> **Oracle Linux note:** `libguestfs` on OL8/OL9 requires a working KVM appliance. If you're on a VM (nested virt or no `/dev/kvm`), set:
> ```bash
> export LIBGUESTFS_BACKEND=direct    # try this first
> # or
> export LIBGUESTFS_BACKEND=qemu      # fallback, slower
> ```
> You may also need:
> ```bash
> sudo dnf install supermin
> ```

---

## Method 3: Convert to raw + loop device — No extra packages

Best for: simple single-partition images or when NBD/libguestfs aren't available.

```bash
# Convert qcow2 → raw
qemu-img convert -f qcow2 -O raw /path/to/image.qcow2 /tmp/image.raw

# Find the partition offset (look for the Linux partition's Start sector)
fdisk -l /tmp/image.raw
# Multiply Start sector × 512 to get byte offset
# Example: Start=2048 → offset=$((2048 * 512)) = 1048576

sudo mount -o loop,offset=1048576 /tmp/image.raw /mnt/qcow2
```

### Unmount

```bash
sudo umount /mnt/qcow2
rm /tmp/image.raw   # clean up the raw copy
```

---

## Quick reference

| Method | Root required | Writes supported | LVM support | Speed | Best for |
|---|---|---|---|---|---|
| NBD | Yes | Yes | Yes | Fast | General use |
| guestmount | Optional | Yes (risky) | Yes | Slower | Safe inspection |
| raw+loop | Yes | Yes | Partial | Needs disk space | No extra packages |

---

## Troubleshooting

**`nbd` device busy after a crash:**
```bash
sudo qemu-nbd --disconnect /dev/nbd0
sudo partprobe /dev/nbd0
```

**`guestmount` hangs or fails:**
```bash
export LIBGUESTFS_DEBUG=1 LIBGUESTFS_TRACE=1
guestmount ...   # verbose output will show the failure point
```

**SELinux blocking mount (common on OL):**
```bash
sudo ausearch -m avc -ts recent | grep qemu
# Temporary workaround:
sudo setenforce 0
# Proper fix: audit2allow or set correct context on the image file
sudo chcon -t svirt_image_t /path/to/image.qcow2
```
