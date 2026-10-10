package main

// account.go reports which account the program runs under: the OS user and
// groups, the process security context (capabilities, LSM, namespaces, login
// session), where credentials are likely to live (existence only), and, when
// the instance-principal certificate was fetched, the OCI identity it encodes.
// No secret is ever read: only existence checks, public config keys and the
// *public* leaf certificate.

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var capNames = []string{
	"CHOWN", "DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "FSETID", "KILL", "SETGID", "SETUID",
	"SETPCAP", "LINUX_IMMUTABLE", "NET_BIND_SERVICE", "NET_BROADCAST", "NET_ADMIN", "NET_RAW",
	"IPC_LOCK", "IPC_OWNER", "SYS_MODULE", "SYS_RAWIO", "SYS_CHROOT", "SYS_PTRACE", "SYS_PACCT",
	"SYS_ADMIN", "SYS_BOOT", "SYS_NICE", "SYS_RESOURCE", "SYS_TIME", "SYS_TTY_CONFIG", "MKNOD",
	"LEASE", "AUDIT_WRITE", "AUDIT_CONTROL", "SETFCAP", "MAC_OVERRIDE", "MAC_ADMIN", "SYSLOG",
	"WAKE_ALARM", "BLOCK_SUSPEND", "AUDIT_READ", "PERFMON", "BPF", "CHECKPOINT_RESTORE",
}

func decodeCaps(hexMask string) []string {
	v, err := strconv.ParseUint(strings.TrimSpace(hexMask), 16, 64)
	if err != nil {
		return nil
	}
	var out []string
	for i := 0; i < 64; i++ {
		if v&(1<<uint(i)) == 0 {
			continue
		}
		if i < len(capNames) {
			out = append(out, "CAP_"+capNames[i])
		} else {
			out = append(out, "CAP_"+strconv.Itoa(i))
		}
	}
	return out
}

func (c *collector) account() {
	a := map[string]any{
		"uid":  os.Getuid(),
		"gid":  os.Getgid(),
		"euid": os.Geteuid(),
		"egid": os.Getegid(),
	}
	// Account names come straight from /etc/passwd and /etc/group (no password
	// hashes live there). os/user is deliberately avoided: with cgo it goes
	// through NSS, which can block on LDAP/SSSD with no timeout. Accounts that
	// only exist in a network directory are therefore reported numerically.
	home := ""
	groupNames := map[string]string{}
	if s, ok := c.read("/etc/group"); ok {
		for _, line := range strings.Split(s, "\n") {
			if f := strings.Split(line, ":"); len(f) >= 3 {
				groupNames[f[2]] = f[0]
			}
		}
	}
	found := false
	if s, ok := c.read("/etc/passwd"); ok {
		uid := strconv.Itoa(os.Getuid())
		for _, line := range strings.Split(s, "\n") {
			f := strings.Split(line, ":")
			if len(f) < 7 || f[2] != uid {
				continue
			}
			found = true
			a["username"] = f[0]
			if gecos, _, _ := strings.Cut(f[4], ","); gecos != "" {
				a["fullName"] = gecos
			}
			home = f[5]
			a["home"] = f[5]
			a["loginShell"] = f[6]
			if g, ok := groupNames[f[3]]; ok {
				a["primaryGroup"] = g
			}
			break
		}
	}
	if !found {
		a["lookupNote"] = "uid not found in /etc/passwd (network directory account?); names taken from the environment"
		if u := os.Getenv("USER"); u != "" {
			a["username"] = u
		} else if u := os.Getenv("LOGNAME"); u != "" {
			a["username"] = u
		}
	}
	if home == "" {
		home = os.Getenv("HOME")
		if home != "" {
			a["home"] = home
		}
	}

	// Supplementary groups.
	if gids, err := os.Getgroups(); err == nil {
		groups := []string{}
		for _, gid := range gids {
			name := strconv.Itoa(gid)
			if g, ok := groupNames[name]; ok {
				name = g + "(" + name + ")"
			}
			groups = append(groups, name)
		}
		a["groups"] = groups
	}

	// Kernel's view of the process: capabilities, seccomp, no_new_privs.
	if s, ok := c.read("/proc/self/status"); ok {
		st := map[string]string{}
		for _, line := range strings.Split(s, "\n") {
			k, v, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			switch k {
			case "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs", "Seccomp", "Seccomp_filters":
				st[k] = strings.TrimSpace(v)
			}
		}
		if len(st) > 0 {
			a["procStatus"] = st
		}
		if ce, ok := st["CapEff"]; ok {
			a["effectiveCapabilities"] = decodeCaps(ce)
		}
	}

	// Mandatory access control context.
	if s, ok := c.read("/proc/self/attr/current"); ok && s != "" {
		a["lsmContext"] = s // SELinux context or AppArmor profile
	}
	if s, ok := c.read("/sys/fs/selinux/enforce"); ok {
		a["selinuxEnforce"] = s
	}

	// Login session and namespaces.
	if s, ok := c.read("/proc/self/loginuid"); ok {
		a["loginUid"] = s // 4294967295 = unset (service / container)
	}
	if s, ok := c.read("/proc/self/sessionid"); ok {
		a["sessionId"] = s
	}
	if s, ok := c.read("/proc/self/uid_map"); ok {
		a["uidMap"] = s // "0 0 4294967295" = initial user namespace
	}
	if s, ok := c.read("/proc/self/gid_map"); ok {
		a["gidMap"] = s
	}

	// Process context.
	if exe, err := os.Executable(); err == nil {
		a["executable"] = exe
	}
	if wd, err := os.Getwd(); err == nil {
		a["cwd"] = wd
	}
	if s, ok := c.read("/proc/" + strconv.Itoa(os.Getppid()) + "/comm"); ok {
		a["parentProcess"] = s
	}
	if fi, err := os.Stdin.Stat(); err == nil {
		a["stdinIsTerminal"] = fi.Mode()&os.ModeCharDevice != 0
	}
	if l, err := os.Readlink(c.p("/proc/self/fd/0")); err == nil {
		a["stdinTarget"] = l
	}
	if s, ok := c.read("/proc/self/cgroup"); ok {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "0::") {
				a["cgroupPath"] = strings.TrimPrefix(line, "0::")
				break
			}
		}
	}

	// Account-related environment (values that are not secrets).
	envm := map[string]string{}
	for _, k := range []string{"USER", "LOGNAME", "HOME", "SHELL", "SUDO_USER", "SUDO_UID", "SUDO_GID"} {
		if v := os.Getenv(k); v != "" {
			envm[k] = v
		}
	}
	for _, k := range []string{"SSH_CONNECTION", "SSH_TTY", "XDG_SESSION_ID", "OCI_CLI_PROFILE", "AWS_PROFILE"} {
		if os.Getenv(k) != "" {
			envm[k] = "(set)"
		}
	}
	if len(envm) > 0 {
		a["env"] = envm
	}

	// Where credentials might live: existence only, never contents.
	// Scan the passwd home and $HOME (they can differ, e.g. under sudo or in tests).
	homes := []string{}
	for _, h := range []string{home, os.Getenv("HOME")} {
		if h != "" && !containsStr(homes, h) {
			homes = append(homes, h)
		}
	}
	if len(homes) > 0 {
		cred := map[string]bool{}
		profiles := map[string]map[string]string{}
		for _, h := range homes {
			for _, rel := range []string{
				".oci/config", ".aws/credentials", ".aws/config", ".config/gcloud",
				".azure", ".kube/config", ".docker/config.json", ".ssh/authorized_keys",
			} {
				if c.exists(filepath.Join(h, rel)) {
					cred[rel] = true
				} else if _, seen := cred[rel]; !seen {
					cred[rel] = false
				}
			}
			for name, kv := range c.ociConfigProfiles(filepath.Join(h, ".oci/config")) {
				profiles[name] = kv
			}
		}
		a["homesScanned"] = homes
		a["credentialFilesPresent"] = cred
		if len(profiles) > 0 {
			a["ociCliProfiles"] = profiles
		}
	}
	c.env.Account = a
}

// ociConfigProfiles lists the profiles in an OCI CLI config with their
// non-secret keys. Passphrases are never copied, and key files are never read.
func (c *collector) ociConfigProfiles(path string) map[string]map[string]string {
	s, ok := c.read(path)
	if !ok {
		return nil
	}
	allow := map[string]bool{
		"tenancy": true, "user": true, "region": true, "fingerprint": true,
		"key_file": true, "security_token_file": true, "compartment-id": true,
	}
	out := map[string]map[string]string{}
	cur := ""
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = strings.Trim(line, "[]")
			out[cur] = map[string]string{}
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found || cur == "" {
			continue
		}
		k = strings.TrimSpace(k)
		if allow[k] {
			out[cur][k] = strings.TrimSpace(v)
		}
	}
	return out
}

// ---------------------------------------------------------------- identity cert

type certInfo struct {
	Subject           string            `json:"subject"`
	Issuer            string            `json:"issuer"`
	SerialHex         string            `json:"serialHex"`
	NotBefore         string            `json:"notBefore"`
	NotAfter          string            `json:"notAfter"`
	Expired           bool              `json:"expired"`
	SHA256            string            `json:"sha256Fingerprint"`
	OrganizationalUn  []string          `json:"organizationalUnits,omitempty"`
	CommonName        string            `json:"commonName,omitempty"`
	CertType          string            `json:"certType,omitempty"`
	TenancyID         string            `json:"tenancyId,omitempty"`
	CompartmentID     string            `json:"compartmentId,omitempty"`
	InstanceID        string            `json:"instanceId,omitempty"`
	OtherOUAttributes map[string]string `json:"otherOuAttributes,omitempty"`
}

// parseIdentityCert decodes the first certificate in a PEM string (the public
// instance-principal leaf cert) and extracts the OCI identity it encodes:
// OUs of the form opc-tenant:<ocid>, opc-compartment:<ocid>, opc-instance:<ocid>.
func parseIdentityCert(pemText string) (*certInfo, error) {
	rest := []byte(pemText)
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			return nil, fmt.Errorf("no CERTIFICATE block found")
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(cert.Raw)
		ci := &certInfo{
			Subject:          cert.Subject.String(),
			Issuer:           cert.Issuer.String(),
			SerialHex:        cert.SerialNumber.Text(16),
			NotBefore:        cert.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:         cert.NotAfter.UTC().Format(time.RFC3339),
			Expired:          time.Now().After(cert.NotAfter),
			SHA256:           hex.EncodeToString(sum[:]),
			OrganizationalUn: cert.Subject.OrganizationalUnit,
			CommonName:       cert.Subject.CommonName,
		}
		for _, ou := range cert.Subject.OrganizationalUnit {
			k, v, found := strings.Cut(ou, ":")
			if !found {
				continue
			}
			switch k {
			case "opc-certtype":
				ci.CertType = v
			case "opc-tenant":
				ci.TenancyID = v
			case "opc-compartment":
				ci.CompartmentID = v
			case "opc-instance":
				ci.InstanceID = v
			default:
				if strings.HasPrefix(k, "opc-") {
					if ci.OtherOUAttributes == nil {
						ci.OtherOUAttributes = map[string]string{}
					}
					ci.OtherOUAttributes[k] = v
				}
			}
		}
		sort.Strings(ci.OrganizationalUn)
		return ci, nil
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
