```
Oracle linux based vm, running in oci.
I need to move ssh server to a diff port, accessible through public ip address.
Besides changing sshd config and adding appropriate egress route - what else is needed? Firewall?
```

Yes, OCI has two layers of firewall to deal with:

**1. OCI Security List (or Network Security Group)**
In your VCN's subnet security list, you need to add an ingress rule allowing TCP on your new port from `0.0.0.0/0` (or your source IP). This is the OCI-level firewall, separate from anything on the VM itself.

**2. OS-level firewall (`firewalld`)**
Oracle Linux uses `firewalld` by default. You need to allow the new port there too:

```bash
firewall-cmd --permanent --add-port=2222/tcp
firewall-cmd --reload
```

Optionally remove the default SSH rule if you want to lock it down:
```bash
firewall-cmd --permanent --remove-service=ssh
firewall-cmd --reload
```

**3. SELinux** (often overlooked)
Oracle Linux ships with SELinux enforcing. SSH is only allowed to bind to ports labeled `ssh_port_t`. You need to add your new port:

```bash
semanage port -a -t ssh_port_t -p tcp 2222
```

If `semanage` isn't available: `dnf install policycoreutils-python-utils`.

**Order of operations:**
1. Add SELinux label for the port
2. Update `/etc/ssh/sshd_config` (`Port 2222`)
3. Open `firewalld`
4. Open OCI Security List/NSG
5. Restart sshd: `systemctl restart sshd`
6. **Test in a new session before closing your existing one**

The SELinux step is the one that most commonly catches people off guard — sshd will silently fail to bind or outright refuse to start without it.
