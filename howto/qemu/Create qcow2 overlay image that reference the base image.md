# qemu-img: Create qcow2 overlay image that reference the base image

The `-b` flag sets the backing file path, which gets stored as a **relative or absolute path string** inside the qcow2 header.

```bash
# relative path (overlay must stay in same dir as base)
qemu-img create -f qcow2 -b base.qcow2 -F qcow2 vm1.qcow2

# absolute path (overlay can live anywhere)
qemu-img create -f qcow2 -b /images/base.qcow2 -F qcow2 /vms/vm1/disk.qcow2
```

**Inspect what got stored:**

```bash
qemu-img info vm1.qcow2
# backing file: base.qcow2
# backing file format: qcow2
```

**If you move files around**, the backing path breaks. Fix it with:

```bash
qemu-img rebase -b /new/path/base.qcow2 -F qcow2 vm1.qcow2
```

`-F qcow2` (capital F) is the backing file *format* — always specify it explicitly to avoid QEMU having to probe the format, which matters for security in automated/CVM contexts.
