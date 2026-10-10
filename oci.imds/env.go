package main

// env.go gathers local, host-side evidence about the environment the program
// is running in (cloud provider, hypervisor, bare metal vs VM, container,
// confidential-computing features, ...) and optionally probes the metadata
// endpoints of other clouds. Everything is best-effort: unreadable or missing
// sources are skipped silently.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- types

type Environment struct {
	Runtime        map[string]any    `json:"runtime"`
	Account        map[string]any    `json:"account,omitempty"`
	OS             map[string]string `json:"os,omitempty"`
	Kernel         map[string]string `json:"kernel,omitempty"`
	Hostname       map[string]string `json:"hostname,omitempty"`
	DMI            map[string]string `json:"dmi,omitempty"`
	DMIUnreadable  []string          `json:"dmiUnreadable,omitempty"`
	CPU            map[string]any    `json:"cpu,omitempty"`
	Resources      map[string]any    `json:"resources,omitempty"`
	Virtualization map[string]any    `json:"virtualization,omitempty"`
	Confidential   map[string]any    `json:"confidentialComputing,omitempty"`
	Firmware       map[string]any    `json:"firmware,omitempty"`
	Container      map[string]any    `json:"container,omitempty"`
	Network        map[string]any    `json:"network,omitempty"`
	PCI            map[string]any    `json:"pci,omitempty"`
	CloudInit      map[string]any    `json:"cloudInit,omitempty"`
	MarkerFiles    map[string]bool   `json:"markerFiles,omitempty"`
	Devices        map[string]bool   `json:"devices,omitempty"`
	EnvVars        map[string]string `json:"envVars,omitempty"`
	CloudProbes    []probeResult     `json:"cloudProbes,omitempty"`
	Indicators     []indicator       `json:"indicators,omitempty"`
	Verdict        *verdict          `json:"verdict,omitempty"`
}

type indicator struct {
	Provider string `json:"provider"`
	Signal   string `json:"signal"`
	Evidence string `json:"evidence"`
	Weight   int    `json:"weight"`
}

type probeResult struct {
	Provider string            `json:"provider"`
	URL      string            `json:"url"`
	Detected bool              `json:"detected"`
	Status   int               `json:"httpStatus,omitempty"`
	Data     map[string]string `json:"data,omitempty"`
	Error    string            `json:"error,omitempty"`
}

type verdict struct {
	Provider              string         `json:"provider"`
	Confidence            string         `json:"confidence"`
	Scores                map[string]int `json:"scores,omitempty"`
	Virtualization        string         `json:"virtualization"`
	Hypervisor            string         `json:"hypervisor,omitempty"`
	ContainerRuntime      string         `json:"containerRuntime"`
	ConfidentialComputing []string       `json:"confidentialComputing"`
	OCIShape              string         `json:"ociShape,omitempty"`
	Notes                 []string       `json:"notes,omitempty"`
}

type envOptions struct {
	Root             string // filesystem root for local probing ("/" normally)
	Probe            bool   // probe other clouds' metadata endpoints
	ProbeTimeout     time.Duration
	ProbeBudget      time.Duration // overall cap for the whole probe phase
	ProbeBase        string        // base URL for AWS/GCP/Azure/DO/Hetzner probes
	ProbeAlibabaBase string
	RunCommands      bool   // allow running systemd-detect-virt
	Attest           bool   // fetch the SNP attestation report from /dev/sev-guest when present
	AttestDevice     string // device path (default /dev/sev-guest)
	AttestNonce      []byte // up to 64 bytes embedded as REPORT_DATA
}

type collector struct {
	root      string
	env       *Environment
	flags     map[string]bool
	nameserv  []string
	search    []string
	hostnames []string
	ntp       []string
	leases    string
}

// ---------------------------------------------------------------- helpers

func (c *collector) p(path string) string { return filepath.Join(c.root, path) }

func (c *collector) rel(full string) string {
	return "/" + strings.TrimLeft(strings.TrimPrefix(full, filepath.Clean(c.root)), "/")
}

// read returns the trimmed contents of a file (capped at 4 MiB).
func (c *collector) read(path string) (string, bool) {
	f, err := os.Open(c.p(path))
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil && len(b) == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " ")), true
}

func (c *collector) exists(path string) bool {
	_, err := os.Stat(c.p(path))
	return err == nil
}

func (c *collector) ind(provider, signal, evidence string, weight int) {
	c.env.Indicators = append(c.env.Indicators, indicator{provider, signal, evidence, weight})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "...(truncated)"
	}
	return s
}

func parseKV(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[k] = strings.Trim(v, `"'`)
	}
	return m
}

func providerFromName(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "oracle"):
		return "oci"
	case strings.Contains(s, "ec2"), strings.Contains(s, "aws"), strings.Contains(s, "amazon"):
		return "aws"
	case strings.Contains(s, "gce"), strings.Contains(s, "gcp"), strings.Contains(s, "google"):
		return "gcp"
	case strings.Contains(s, "azure"):
		return "azure"
	case strings.Contains(s, "digitalocean"):
		return "digitalocean"
	case strings.Contains(s, "hetzner"):
		return "hetzner"
	case strings.Contains(s, "aliyun"), strings.Contains(s, "alibaba"):
		return "alibaba"
	case strings.Contains(s, "tencent"):
		return "tencent"
	case strings.Contains(s, "vultr"):
		return "vultr"
	}
	return ""
}

func runCmd(ctx context.Context, name string, args ...string) (string, bool) {
	if _, err := exec.LookPath(name); err != nil {
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.WaitDelay = 500 * time.Millisecond // never wait on a grandchild that keeps the pipe open
	out, _ := cmd.Output()                 // non-zero exit is normal ("none")
	return strings.TrimSpace(string(out)), true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- entry point

func collectEnvironment(ctx context.Context, o envOptions) *Environment {
	if o.Root == "" {
		o.Root = "/"
	}
	c := &collector{root: o.Root, env: &Environment{}, flags: map[string]bool{}}

	c.runtimeInfo()
	c.account()
	c.osInfo()
	c.hostnameInfo()
	c.dmi()
	c.cpu()
	c.resources()
	c.virtualization(ctx, o)
	c.confidential()
	c.confidentialExtra(ctx, o)
	c.firmware()
	c.container(ctx, o)
	c.network()
	c.pci()
	c.cloudInit()
	c.markers()
	c.envVars()
	if o.Probe {
		c.probes(ctx, o)
	}
	c.textIndicators()
	return c.env
}

// ---------------------------------------------------------------- collectors

func (c *collector) runtimeInfo() {
	m := map[string]any{
		"goos":      runtime.GOOS,
		"goarch":    runtime.GOARCH,
		"goVersion": runtime.Version(),
		"uid":       os.Getuid(),
		"euid":      os.Geteuid(),
		"pid":       os.Getpid(),
		"ppid":      os.Getppid(),
	}
	if c.root != "/" {
		m["fsRoot"] = c.root
	}
	if os.Geteuid() != 0 {
		m["note"] = "not running as root: some DMI fields (serial, uuid) and system files may be unreadable"
	}
	c.env.Runtime = m
}

func (c *collector) osInfo() {
	if s, ok := c.read("/etc/os-release"); ok {
		c.env.OS = parseKV(s)
	} else if s, ok := c.read("/usr/lib/os-release"); ok {
		c.env.OS = parseKV(s)
	}
	k := map[string]string{}
	for key, path := range map[string]string{
		"release": "/proc/sys/kernel/osrelease",
		"version": "/proc/sys/kernel/version",
		"proc":    "/proc/version",
		"cmdline": "/proc/cmdline",
	} {
		if s, ok := c.read(path); ok {
			k[key] = s
		}
	}
	if s, ok := c.read("/proc/uptime"); ok {
		if f := strings.Fields(s); len(f) > 0 {
			k["uptimeSeconds"] = f[0]
		}
	}
	if len(k) > 0 {
		c.env.Kernel = k
	}
}

func (c *collector) hostnameInfo() {
	m := map[string]string{}
	if h, err := os.Hostname(); err == nil && h != "" {
		m["os.Hostname"] = h
		c.hostnames = append(c.hostnames, h)
	}
	if s, ok := c.read("/etc/hostname"); ok {
		m["/etc/hostname"] = s
		c.hostnames = append(c.hostnames, s)
	}
	if s, ok := c.read("/proc/sys/kernel/domainname"); ok && s != "(none)" {
		m["domainname"] = s
	}
	if len(m) > 0 {
		c.env.Hostname = m
	}
}

var dmiFields = []string{
	"sys_vendor", "product_name", "product_version", "product_family", "product_sku",
	"product_serial", "product_uuid",
	"board_vendor", "board_name", "board_version", "board_serial", "board_asset_tag",
	"chassis_vendor", "chassis_type", "chassis_version", "chassis_serial", "chassis_asset_tag",
	"bios_vendor", "bios_version", "bios_date", "bios_release",
}

type dmiRule struct {
	field, mode, val, provider string
	weight                     int
}

var dmiRules = []dmiRule{
	{"chassis_asset_tag", "contains", "oraclecloud.com", "oci", 3},
	{"sys_vendor", "contains", "oracle", "oci", 1},
	{"product_name", "contains", "oracle server", "oci", 1},
	{"board_asset_tag", "prefix", "i-", "aws", 3},
	{"sys_vendor", "contains", "amazon ec2", "aws", 3},
	{"bios_vendor", "contains", "amazon ec2", "aws", 2},
	{"product_uuid", "prefix", "ec2", "aws", 2},
	{"product_name", "equals", "google compute engine", "gcp", 3},
	{"sys_vendor", "contains", "google", "gcp", 2},
	{"chassis_asset_tag", "equals", "7783-7084-3265-9085-8269-3286-77", "azure", 3},
	{"sys_vendor", "contains", "microsoft corporation", "azure", 1},
	{"sys_vendor", "contains", "digitalocean", "digitalocean", 3},
	{"sys_vendor", "contains", "hetzner", "hetzner", 3},
	{"sys_vendor", "contains", "alibaba cloud", "alibaba", 3},
	{"sys_vendor", "contains", "tencent", "tencent", 3},
	{"sys_vendor", "contains", "vultr", "vultr", 3},
}

func (c *collector) dmi() {
	dir := "/sys/class/dmi/id"
	if !c.exists(dir) {
		dir = "/sys/devices/virtual/dmi/id"
	}
	m := map[string]string{}
	var unreadable []string
	for _, f := range dmiFields {
		path := dir + "/" + f
		if s, ok := c.read(path); ok {
			m[f] = s
		} else if c.exists(path) {
			unreadable = append(unreadable, f)
		}
	}
	if len(m) > 0 {
		c.env.DMI = m
	}
	c.env.DMIUnreadable = unreadable

	for _, r := range dmiRules {
		v := strings.ToLower(m[r.field])
		if v == "" {
			continue
		}
		hit := false
		switch r.mode {
		case "contains":
			hit = strings.Contains(v, r.val)
		case "prefix":
			hit = strings.HasPrefix(v, r.val)
		case "equals":
			hit = v == r.val
		}
		if hit {
			c.ind(r.provider, "dmi."+r.field, m[r.field], r.weight)
		}
	}
	if s, ok := c.read("/sys/hypervisor/uuid"); ok && strings.HasPrefix(strings.ToLower(s), "ec2") {
		c.ind("aws", "sys/hypervisor/uuid", s, 2)
	}
}

func (c *collector) cpu() {
	cpu := map[string]any{"arch": runtime.GOARCH}
	s, ok := c.read("/proc/cpuinfo")
	if !ok {
		c.env.CPU = cpu
		return
	}
	first := map[string]string{}
	var flags, bugs []string
	procs := 0
	for _, line := range strings.Split(s, "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "processor" {
			procs++
		}
		if procs > 1 {
			continue // details of the first processor only; keep counting
		}
		switch k {
		case "flags", "Features":
			flags = strings.Fields(v)
		case "bugs":
			bugs = strings.Fields(v)
		default:
			first[k] = v
		}
	}
	for _, f := range flags {
		c.flags[f] = true
	}
	var notable []string
	for _, f := range []string{"hypervisor", "sev", "sev_es", "sev_snp", "sme", "tdx_guest", "vmx", "svm"} {
		if c.flags[f] {
			notable = append(notable, f)
		}
	}
	cpu["processorsInCpuinfo"] = procs
	cpu["firstProcessor"] = first
	cpu["flags"] = flags
	if len(bugs) > 0 {
		cpu["bugs"] = bugs
	}
	cpu["notableFlags"] = notable
	c.env.CPU = cpu
}

func (c *collector) resources() {
	r := map[string]any{"goNumCPU": runtime.NumCPU()}
	if s, ok := c.read("/proc/meminfo"); ok {
		for _, line := range strings.Split(s, "\n") {
			k, v, found := strings.Cut(line, ":")
			if !found || (k != "MemTotal" && k != "SwapTotal") {
				continue
			}
			if fs := strings.Fields(v); len(fs) > 0 {
				if n, err := strconv.ParseInt(fs[0], 10, 64); err == nil {
					r[k+"KiB"] = n
				}
			}
		}
	}
	if nodes, _ := filepath.Glob(c.p("/sys/devices/system/node/node[0-9]*")); nodes != nil {
		r["numaNodes"] = len(nodes)
	}
	if s, ok := c.read("/sys/devices/system/cpu/online"); ok {
		r["cpusOnline"] = s
	}
	c.env.Resources = r
}

func (c *collector) virtualization(ctx context.Context, o envOptions) {
	v := map[string]any{
		"hypervisorCpuFlag": c.flags["hypervisor"],
	}
	if arch := runtime.GOARCH; arch != "amd64" && arch != "386" {
		v["hypervisorCpuFlagNote"] = "flag is x86-only; not meaningful on " + arch
	}
	if s, ok := c.read("/sys/hypervisor/type"); ok {
		v["sysHypervisorType"] = s
	}
	if s, ok := c.read("/sys/hypervisor/uuid"); ok {
		v["sysHypervisorUUID"] = s
	}
	for name, path := range map[string]string{
		"kvmDevice":      "/dev/kvm",
		"xenProc":        "/proc/xen",
		"hypervVmbus":    "/sys/bus/vmbus",
		"virtioBus":      "/sys/bus/virtio",
		"kvmAmdModule":   "/sys/module/kvm_amd",
		"kvmIntelModule": "/sys/module/kvm_intel",
	} {
		v[name] = c.exists(path)
	}
	if c.exists("/sys/bus/vmbus") {
		c.ind("azure", "hyper-v vmbus present", "/sys/bus/vmbus", 1)
	}
	for _, p := range []string{"sev", "sev_es", "sev_snp", "nested"} {
		if s, ok := c.read("/sys/module/kvm_amd/parameters/" + p); ok {
			v["kvm_amd."+p] = s
		}
	}
	if o.RunCommands {
		if s, ok := runCmd(ctx, "systemd-detect-virt", "--vm"); ok {
			v["systemdDetectVirtVM"] = s
		}
	}
	c.env.Virtualization = v
}

func (c *collector) confidential() {
	cc := map[string]any{}
	for _, f := range []string{"sev", "sev_es", "sev_snp", "sme", "tdx_guest"} {
		cc["cpuFlag."+f] = c.flags[f]
	}
	devs := map[string]bool{}
	for _, d := range []string{
		"/dev/sev-guest", "/dev/sev", "/dev/tdx_guest", "/dev/tdx-guest",
		"/dev/tpm0", "/dev/tpmrm0", "/dev/kvm", "/sys/kernel/config/tsm/report",
	} {
		devs[d] = c.exists(d)
	}
	c.env.Devices = devs

	hostParam := func(n string) bool {
		s, ok := c.read("/sys/module/kvm_amd/parameters/" + n)
		return ok && (s == "Y" || s == "1")
	}
	hv := c.flags["hypervisor"]
	var tech []string
	switch {
	case devs["/dev/sev-guest"]:
		tech = append(tech, "amd-sev-snp-guest (/dev/sev-guest present)")
	case c.flags["sev_snp"] && hv:
		tech = append(tech, "amd-sev-snp (cpu flag inside a VM; likely guest)")
	case hostParam("sev_snp"):
		tech = append(tech, "amd-sev-snp-host (kvm_amd sev_snp enabled)")
	case c.flags["sev_snp"]:
		tech = append(tech, "amd-sev-snp-capable (cpu flag, no hypervisor flag)")
	}
	if len(tech) == 0 {
		if c.flags["sev_es"] {
			tech = append(tech, "amd-sev-es (cpu flag)")
		} else if c.flags["sev"] {
			tech = append(tech, "amd-sev (cpu flag)")
		}
	}
	if c.flags["tdx_guest"] || devs["/dev/tdx_guest"] || devs["/dev/tdx-guest"] {
		tech = append(tech, "intel-tdx-guest")
	}
	cc["technologies"] = tech
	if v, ok := c.read("/sys/class/tpm/tpm0/tpm_version_major"); ok {
		cc["tpmVersionMajor"] = v
	}
	c.env.Confidential = cc
}

const efiGlobalGUID = "8be4df61-93ca-11d2-aa0d-00e098032b8c"

func (c *collector) efiBool(name string) (val, ok bool) {
	b, err := os.ReadFile(c.p("/sys/firmware/efi/efivars/" + name + "-" + efiGlobalGUID))
	if err != nil || len(b) < 5 {
		return false, false // 4 attribute bytes + 1 value byte
	}
	return b[4] == 1, true
}

func (c *collector) firmware() {
	f := map[string]any{"efi": c.exists("/sys/firmware/efi")}
	if v, ok := c.efiBool("SecureBoot"); ok {
		f["secureBoot"] = v
	}
	if v, ok := c.efiBool("SetupMode"); ok {
		f["setupMode"] = v
	}
	c.env.Firmware = f
}

func (c *collector) container(ctx context.Context, o envOptions) {
	m := map[string]any{}
	dockerenv := c.exists("/.dockerenv")
	containerenv := c.exists("/run/.containerenv")
	m["dockerenv"] = dockerenv
	m["containerenv"] = containerenv
	if s, ok := c.read("/run/.containerenv"); ok && s != "" {
		m["containerenvContents"] = truncate(s, 1024)
	}
	cg1, _ := c.read("/proc/1/cgroup")
	cgs, _ := c.read("/proc/self/cgroup")
	if cg1 != "" {
		m["cgroupPid1"] = truncate(cg1, 1024)
	}
	if cgs != "" {
		m["cgroupSelf"] = truncate(cgs, 1024)
	}
	if s, ok := c.read("/proc/1/comm"); ok {
		m["pid1Comm"] = s
	}
	if v := os.Getenv("container"); v != "" {
		m["envContainer"] = v
	}
	k8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
	if k8sHost != "" {
		m["kubernetesServiceHost"] = k8sHost
	}
	if s, ok := c.read("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); ok {
		m["kubernetesNamespace"] = s // namespace only; the token is never read
	}
	if c.exists("/sys/fs/cgroup/cgroup.controllers") {
		m["cgroupVersion"] = 2
		if s, ok := c.read("/sys/fs/cgroup/cpu.max"); ok {
			m["cpuMax"] = s
		}
		if s, ok := c.read("/sys/fs/cgroup/memory.max"); ok {
			m["memoryMax"] = s
		}
	} else if c.exists("/sys/fs/cgroup") {
		m["cgroupVersion"] = 1
		if s, ok := c.read("/sys/fs/cgroup/cpu/cpu.cfs_quota_us"); ok {
			m["cpuCfsQuotaUs"] = s
		}
		if s, ok := c.read("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok {
			m["memoryLimitBytes"] = s
		}
	}
	sd := ""
	if o.RunCommands {
		if s, ok := runCmd(ctx, "systemd-detect-virt", "--container"); ok {
			sd = s
			m["systemdDetectVirtContainer"] = s
		}
	}

	joined := strings.ToLower(cg1 + "\n" + cgs)
	kind := "none-detected"
	switch {
	case k8sHost != "" || strings.Contains(joined, "kubepods"):
		kind = "kubernetes"
	case dockerenv || strings.Contains(joined, "docker"):
		kind = "docker"
	case containerenv || strings.Contains(joined, "libpod"):
		kind = "podman"
	case strings.Contains(joined, "lxc"):
		kind = "lxc"
	case strings.Contains(joined, "containerd"):
		kind = "containerd"
	case sd != "" && sd != "none":
		kind = sd
	}
	m["kind"] = kind
	c.env.Container = m
}

func (c *collector) network() {
	n := map[string]any{}

	if ifs, err := net.Interfaces(); err == nil {
		var list []map[string]any
		for _, ifc := range ifs {
			var addrs []string
			if as, err := ifc.Addrs(); err == nil {
				for _, a := range as {
					addrs = append(addrs, a.String())
				}
			}
			list = append(list, map[string]any{
				"name":  ifc.Name,
				"index": ifc.Index,
				"mtu":   ifc.MTU,
				"mac":   ifc.HardwareAddr.String(),
				"flags": ifc.Flags.String(),
				"addrs": addrs,
			})
		}
		n["interfaces"] = list
	}

	if s, ok := c.read("/etc/resolv.conf"); ok {
		dns := map[string]any{}
		var options []string
		for _, line := range strings.Split(s, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			switch f[0] {
			case "nameserver":
				c.nameserv = append(c.nameserv, f[1])
			case "search", "domain":
				c.search = append(c.search, f[1:]...)
			case "options":
				options = append(options, f[1:]...)
			}
		}
		dns["nameservers"] = c.nameserv
		dns["search"] = c.search
		if len(options) > 0 {
			dns["options"] = options
		}
		n["resolvConf"] = dns
	}

	// Default routes from /proc/net/route (IPv4, little-endian hex).
	if s, ok := c.read("/proc/net/route"); ok {
		var routes []map[string]string
		for i, line := range strings.Split(s, "\n") {
			f := strings.Fields(line)
			if i == 0 || len(f) < 3 || f[1] != "00000000" {
				continue
			}
			if v, err := strconv.ParseUint(f[2], 16, 32); err == nil {
				gw := net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24)).String()
				routes = append(routes, map[string]string{"iface": f[0], "gateway": gw})
			}
		}
		if routes != nil {
			n["defaultRoutes"] = routes
		}
	}

	// DHCP leases (domain names, DNS/NTP servers handed out by the cloud).
	leases := map[string]string{}
	var sb strings.Builder
	for _, pat := range []string{
		"/run/systemd/netif/leases/*", "/var/lib/dhcp/*.leases",
		"/var/lib/dhclient/*.lease", "/var/lib/NetworkManager/*.lease",
	} {
		files, _ := filepath.Glob(c.p(pat))
		for _, f := range files {
			if len(leases) >= 5 {
				break
			}
			if s, ok := c.read(c.rel(f)); ok && s != "" {
				leases[c.rel(f)] = truncate(s, 4096)
				sb.WriteString(strings.ToLower(s) + "\n")
			}
		}
	}
	if len(leases) > 0 {
		n["dhcpLeases"] = leases
		c.leases = sb.String()
	}

	// Time sources are a good cloud fingerprint (e.g. OCI 169.254.169.254).
	ts := map[string][]string{}
	for _, path := range []string{"/etc/chrony.conf", "/etc/chrony/chrony.conf", "/etc/ntp.conf"} {
		s, ok := c.read(path)
		if !ok {
			continue
		}
		var lines []string
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			for _, pfx := range []string{"server ", "pool ", "peer ", "refclock "} {
				if strings.HasPrefix(line, pfx) {
					lines = append(lines, line)
					c.ntp = append(c.ntp, strings.ToLower(line))
				}
			}
		}
		if len(lines) > 0 {
			ts[path] = lines
		}
	}
	if len(ts) > 0 {
		n["timeSources"] = ts
	}
	c.env.Network = n
}

func (c *collector) pci() {
	dirs, _ := filepath.Glob(c.p("/sys/bus/pci/devices/*"))
	if len(dirs) == 0 {
		return
	}
	ids := map[string]int{}
	for _, d := range dirs {
		r := c.rel(d)
		v, ok1 := c.read(r + "/vendor")
		dev, ok2 := c.read(r + "/device")
		if !ok1 || !ok2 {
			continue
		}
		ids[strings.TrimPrefix(v, "0x")+":"+strings.TrimPrefix(dev, "0x")]++
	}
	c.env.PCI = map[string]any{"deviceCount": len(dirs), "vendorDeviceIds": ids}
	for _, vp := range []struct{ vendor, provider, what string }{
		{"1d0f", "aws", "Amazon PCI vendor (ENA/NVMe)"},
		{"1ae0", "gcp", "Google PCI vendor (gVNIC)"},
		{"1414", "azure", "Microsoft PCI vendor"},
	} {
		for id := range ids {
			if strings.HasPrefix(id, vp.vendor+":") {
				c.ind(vp.provider, "pci.vendor", vp.what+" "+id, 1)
				break
			}
		}
	}
}

func (c *collector) cloudInit() {
	m := map[string]any{}
	if s, ok := c.read("/run/cloud-init/cloud-id"); ok && s != "" {
		m["cloudId"] = s
		if p := providerFromName(s); p != "" {
			c.ind(p, "cloud-init cloud-id", s, 3)
		}
	}
	if s, ok := c.read("/run/cloud-init/instance-data.json"); ok {
		var d map[string]any
		if json.Unmarshal([]byte(s), &d) == nil {
			if v1, ok := d["v1"].(map[string]any); ok {
				keep := map[string]any{}
				for _, k := range []string{
					"cloud_name", "cloud-name", "platform", "subplatform", "region",
					"availability_zone", "availability-zone", "instance_id", "instance-id",
					"local_hostname", "distro", "distro_release", "distro_version",
					"kernel_release", "machine",
				} {
					if v, ok := v1[k]; ok {
						keep[k] = v
					}
				}
				if len(keep) > 0 {
					m["instanceDataV1"] = keep
				}
				for _, k := range []string{"cloud_name", "cloud-name"} {
					if name, ok := v1[k].(string); ok {
						if p := providerFromName(name); p != "" {
							c.ind(p, "cloud-init cloud_name", name, 3)
						}
						break
					}
				}
			}
		}
	}
	if s, ok := c.read("/run/cloud-init/result.json"); ok {
		var d struct {
			V1 struct {
				Datasource string `json:"datasource"`
			} `json:"v1"`
		}
		if json.Unmarshal([]byte(s), &d) == nil && d.V1.Datasource != "" {
			m["datasource"] = d.V1.Datasource
			if p := providerFromName(d.V1.Datasource); p != "" {
				c.ind(p, "cloud-init datasource", d.V1.Datasource, 2)
			}
		}
	}
	if len(m) > 0 {
		c.env.CloudInit = m
	}
}

var markerRules = []struct {
	path, provider string
	weight         int
}{
	{"/etc/oci-hostname.conf", "oci", 1},
	{"/usr/lib/systemd/system/oracle-cloud-agent.service", "oci", 1},
	{"/var/lib/oracle-cloud-agent", "oci", 1},
	{"/usr/bin/amazon-ssm-agent", "aws", 1},
	{"/etc/amazon/ssm/amazon-ssm-agent.json", "aws", 1},
	{"/usr/bin/google_guest_agent", "gcp", 2},
	{"/usr/bin/google_osconfig_agent", "gcp", 1},
	{"/etc/default/instance_configs.cfg", "gcp", 1},
	{"/var/lib/waagent", "azure", 2},
	{"/etc/waagent.conf", "azure", 2},
	{"/usr/sbin/waagent", "azure", 1},
}

func (c *collector) markers() {
	m := map[string]bool{}
	for _, r := range markerRules {
		ok := c.exists(r.path)
		m[r.path] = ok
		if ok {
			c.ind(r.provider, "marker file", r.path, r.weight)
		}
	}
	c.env.MarkerFiles = m
}

// Environment variables whose value is safe to record verbatim.
var envValueAllow = map[string]bool{
	"HOSTNAME": true, "container": true,
	"KUBERNETES_SERVICE_HOST": true, "KUBERNETES_SERVICE_PORT": true,
	"AWS_REGION": true, "AWS_DEFAULT_REGION": true, "AWS_EXECUTION_ENV": true,
	"AWS_LAMBDA_FUNCTION_NAME":      true,
	"ECS_CONTAINER_METADATA_URI":    true,
	"ECS_CONTAINER_METADATA_URI_V4": true,
	"K_SERVICE":                     true, "K_REVISION": true, "K_CONFIGURATION": true,
	"GAE_ENV": true, "GAE_SERVICE": true, "GOOGLE_CLOUD_PROJECT": true, "GCP_PROJECT": true,
	"FUNCTIONS_WORKER_RUNTIME": true, "WEBSITE_SITE_NAME": true, "WEBSITE_INSTANCE_ID": true,
	"OCI_RESOURCE_PRINCIPAL_VERSION": true, "OCI_REGION_METADATA": true,
	"CI": true, "GITHUB_ACTIONS": true, "GITLAB_CI": true,
}

// Variables with these prefixes are listed by name only (values may be secrets).
var envNameOnlyPrefixes = []string{"OCI_", "AWS_", "AZURE_", "GOOGLE_", "GCP_", "KUBERNETES_", "ECS_"}

func (c *collector) envVars() {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		uk := strings.ToUpper(k)
		secretish := strings.Contains(uk, "TOKEN") || strings.Contains(uk, "SECRET") ||
			strings.Contains(uk, "KEY") || strings.Contains(uk, "PASSWORD") || strings.Contains(uk, "CREDENTIAL")
		switch {
		case envValueAllow[k] && !secretish:
			m[k] = v
		case envValueAllow[k] || hasAnyPrefix(k, envNameOnlyPrefixes):
			m[k] = "(set, value omitted)"
		}
	}
	for k := range m {
		switch {
		case k == "OCI_RESOURCE_PRINCIPAL_VERSION" || strings.HasPrefix(k, "OCI_RESOURCE_PRINCIPAL_"):
			c.ind("oci", "env var", k, 3)
		case strings.HasPrefix(k, "OCI_"):
			c.ind("oci", "env var", k, 1)
		case k == "AWS_EXECUTION_ENV" || k == "AWS_LAMBDA_FUNCTION_NAME" || strings.HasPrefix(k, "ECS_CONTAINER_METADATA_URI"):
			c.ind("aws", "env var", k, 2)
		case k == "K_SERVICE" || k == "K_REVISION" || strings.HasPrefix(k, "GAE_"):
			c.ind("gcp", "env var", k, 2)
		case k == "FUNCTIONS_WORKER_RUNTIME" || strings.HasPrefix(k, "WEBSITE_"):
			c.ind("azure", "env var", k, 2)
		}
	}
	if len(m) > 0 {
		c.env.EnvVars = m
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

type textRule struct {
	needle, provider string
	weight           int
}

var domainRules = []textRule{
	{"oraclevcn.com", "oci", 3},
	{"compute.internal", "aws", 2},
	{"ec2.internal", "aws", 2},
	{"google.internal", "gcp", 3},
	{"internal.cloudapp.net", "azure", 3},
}

var nameserverRules = []textRule{
	{"169.254.169.254", "oci", 1}, // also GCP's resolver, so deliberately weak
	{"169.254.169.254", "gcp", 1},
	{"169.254.169.253", "aws", 1},
	{"168.63.129.16", "azure", 2},
}

var ntpRules = []textRule{
	{"169.254.169.254", "oci", 2},
	{"169.254.169.123", "aws", 2},
	{"metadata.google.internal", "gcp", 2},
	{"ptp_hyperv", "azure", 2},
}

func (c *collector) textIndicators() {
	domainText := strings.ToLower(strings.Join(c.search, " ") + " " + strings.Join(c.hostnames, " ") + " " + c.leases)
	for _, r := range domainRules {
		if strings.Contains(domainText, r.needle) {
			c.ind(r.provider, "dns/hostname/dhcp domain", r.needle, r.weight)
		}
	}
	for _, ns := range c.nameserv {
		for _, r := range nameserverRules {
			if ns == r.needle {
				c.ind(r.provider, "resolver address", ns, r.weight)
			}
		}
	}
	ntpText := strings.Join(c.ntp, "\n")
	for _, r := range ntpRules {
		if strings.Contains(ntpText, r.needle) {
			c.ind(r.provider, "time source", r.needle, r.weight)
		}
	}
}

// ---------------------------------------------------------------- cloud probes

func httpDo(ctx context.Context, hc *http.Client, method, url string, hdr map[string]string) (string, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return "", 0, nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return strings.TrimSpace(string(b)), resp.StatusCode, resp.Header, nil
}

func (c *collector) probes(ctx context.Context, o envOptions) {
	// All probes run in parallel and share one overall budget on top of the
	// per-request timeout, so a tarpit endpoint can never stall the program.
	if o.ProbeBudget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.ProbeBudget)
		defer cancel()
	}
	hc := &http.Client{
		Timeout: o.ProbeTimeout, // covers dial, headers and body read
		Transport: &http.Transport{
			Proxy:                 nil, // link-local: never via a proxy
			DialContext:           (&net.Dialer{Timeout: o.ProbeTimeout}).DialContext,
			ResponseHeaderTimeout: o.ProbeTimeout,
			DisableKeepAlives:     true,
		},
	}
	base := strings.TrimRight(o.ProbeBase, "/")
	ali := strings.TrimRight(o.ProbeAlibabaBase, "/")
	fns := []func() probeResult{
		func() probeResult { return probeAWS(ctx, hc, base) },
		func() probeResult { return probeGCP(ctx, hc, base) },
		func() probeResult { return probeAzure(ctx, hc, base) },
		func() probeResult { return probeDigitalOcean(ctx, hc, base) },
		func() probeResult { return probeHetzner(ctx, hc, base) },
		func() probeResult { return probeAlibaba(ctx, hc, ali) },
	}
	res := make([]probeResult, len(fns))
	var wg sync.WaitGroup
	for i, f := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = f()
		}()
	}
	wg.Wait()
	c.env.CloudProbes = res
	for _, r := range res {
		if r.Detected {
			c.ind(r.Provider, "metadata endpoint answered", r.URL, 5)
		}
	}
}

func fetchInto(ctx context.Context, hc *http.Client, data map[string]string, base string, hdr map[string]string, paths []string) {
	for _, p := range paths {
		if v, st, _, err := httpDo(ctx, hc, http.MethodGet, base+"/"+p, hdr); err == nil && st == 200 {
			data[p] = v
		}
	}
}

func probeAWS(ctx context.Context, hc *http.Client, base string) probeResult {
	r := probeResult{Provider: "aws", URL: base + "/latest/meta-data/"}
	hdr := map[string]string{}
	tok, st, _, err := httpDo(ctx, hc, http.MethodPut, base+"/latest/api/token",
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "60"})
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if st == 200 && tok != "" {
		hdr["X-aws-ec2-metadata-token"] = tok
	}
	id, st, _, err := httpDo(ctx, hc, http.MethodGet, base+"/latest/meta-data/instance-id", hdr)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Status = st
	if st != 200 || !strings.HasPrefix(id, "i-") {
		return r
	}
	r.Detected = true
	r.Data = map[string]string{"instance-id": id}
	fetchInto(ctx, hc, r.Data, base+"/latest/meta-data", hdr, []string{
		"instance-type", "ami-id", "placement/region", "placement/availability-zone",
		"local-ipv4", "public-ipv4",
	})
	return r
}

func probeGCP(ctx context.Context, hc *http.Client, base string) probeResult {
	r := probeResult{Provider: "gcp", URL: base + "/computeMetadata/v1/"}
	hdr := map[string]string{"Metadata-Flavor": "Google"}
	id, st, h, err := httpDo(ctx, hc, http.MethodGet, base+"/computeMetadata/v1/instance/id", hdr)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Status = st
	if st != 200 || h.Get("Metadata-Flavor") != "Google" {
		return r
	}
	r.Detected = true
	r.Data = map[string]string{"instance/id": id}
	fetchInto(ctx, hc, r.Data, base+"/computeMetadata/v1", hdr, []string{
		"instance/zone", "instance/machine-type", "instance/name", "instance/hostname",
		"instance/cpu-platform", "project/project-id",
	})
	return r
}

var azureKeys = []string{
	"vmId", "vmSize", "location", "zone", "azEnvironment", "subscriptionId", "resourceGroupName",
	"name", "osType", "offer", "publisher", "sku", "version", "provider", "priority",
	"vmScaleSetName", "platformFaultDomain", "platformUpdateDomain", "placementGroupId",
	"securityProfile",
}

func probeAzure(ctx context.Context, hc *http.Client, base string) probeResult {
	url := base + "/metadata/instance/compute?api-version=2021-02-01&format=json"
	r := probeResult{Provider: "azure", URL: url}
	body, st, _, err := httpDo(ctx, hc, http.MethodGet, url, map[string]string{"Metadata": "true"})
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Status = st
	if st != 200 {
		return r
	}
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil || (m["vmId"] == nil && m["azEnvironment"] == nil) {
		return r
	}
	r.Detected = true
	r.Data = map[string]string{}
	for _, k := range azureKeys { // allowlist: userData/customData are never copied
		v, ok := m[k]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok {
			r.Data[k] = s
		} else if b, err := json.Marshal(v); err == nil {
			r.Data[k] = string(b)
		}
	}
	return r
}

func probeDigitalOcean(ctx context.Context, hc *http.Client, base string) probeResult {
	r := probeResult{Provider: "digitalocean", URL: base + "/metadata/v1/"}
	id, st, _, err := httpDo(ctx, hc, http.MethodGet, base+"/metadata/v1/id", nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Status = st
	if st != 200 || !isDigits(id) {
		return r
	}
	r.Detected = true
	r.Data = map[string]string{"id": id}
	fetchInto(ctx, hc, r.Data, base+"/metadata/v1", nil, []string{"hostname", "region"})
	return r
}

func probeHetzner(ctx context.Context, hc *http.Client, base string) probeResult {
	r := probeResult{Provider: "hetzner", URL: base + "/hetzner/v1/metadata"}
	id, st, _, err := httpDo(ctx, hc, http.MethodGet, base+"/hetzner/v1/metadata/instance-id", nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Status = st
	if st != 200 || !isDigits(id) {
		return r
	}
	r.Detected = true
	r.Data = map[string]string{"instance-id": id}
	fetchInto(ctx, hc, r.Data, base+"/hetzner/v1/metadata", nil, []string{"hostname", "region", "availability-zone"})
	return r
}

func probeAlibaba(ctx context.Context, hc *http.Client, base string) probeResult {
	r := probeResult{Provider: "alibaba", URL: base + "/latest/meta-data/"}
	id, st, _, err := httpDo(ctx, hc, http.MethodGet, base+"/latest/meta-data/instance-id", nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Status = st
	if st != 200 || !strings.HasPrefix(id, "i-") {
		return r
	}
	r.Detected = true
	r.Data = map[string]string{"instance-id": id}
	fetchInto(ctx, hc, r.Data, base+"/latest/meta-data", nil, []string{"region-id", "zone-id", "instance/instance-type", "image-id"})
	return r
}

// ---------------------------------------------------------------- verdict

func hypervisorFromDMI(dmi map[string]string) string {
	sv := strings.ToLower(dmi["sys_vendor"])
	pn := strings.ToLower(dmi["product_name"])
	bv := strings.ToLower(dmi["bios_vendor"])
	switch {
	case strings.Contains(sv, "qemu") || strings.Contains(pn, "standard pc") || strings.Contains(pn, "kvm"):
		return "qemu/kvm"
	case strings.Contains(sv, "amazon ec2") || strings.Contains(bv, "amazon ec2"):
		return "aws (nitro or xen)"
	case pn == "google compute engine":
		return "gce (kvm)"
	case strings.Contains(sv, "microsoft") && strings.Contains(pn, "virtual machine"):
		return "hyper-v"
	case strings.Contains(sv, "vmware") || strings.Contains(pn, "vmware"):
		return "vmware"
	case strings.Contains(pn, "virtualbox") || strings.Contains(sv, "innotek"):
		return "virtualbox"
	case strings.Contains(sv, "xen") || strings.Contains(bv, "xen"):
		return "xen"
	}
	return ""
}

// finalizeVerdict adds the OCI IMDS result as evidence, scores all indicators
// and fills env.Verdict. imdsOK means the OCI IMDS answered; instance is the
// raw /instance/ JSON (may be nil).
func finalizeVerdict(env *Environment, imdsAttempted, imdsOK bool, instance json.RawMessage) {
	v := &verdict{}
	if imdsOK {
		var inst struct {
			Shape string `json:"shape"`
		}
		_ = json.Unmarshal(instance, &inst)
		v.OCIShape = inst.Shape
		ev := "OCI IMDS /instance/ answered"
		if inst.Shape != "" {
			ev += " (shape " + inst.Shape + ")"
		}
		env.Indicators = append(env.Indicators, indicator{"oci", "oci imds", ev, 6})
		if env.Confidential != nil {
			if m := scanCCKeys(instance); len(m) > 0 {
				env.Confidential["imdsReported"] = m
			}
		}
	}

	sort.SliceStable(env.Indicators, func(i, j int) bool {
		a, b := env.Indicators[i], env.Indicators[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Weight > b.Weight
	})

	scores := map[string]int{}
	for _, in := range env.Indicators {
		scores[in.Provider] += in.Weight
	}
	type kv struct {
		k string
		v int
	}
	var ranked []kv
	for k, s := range scores {
		ranked = append(ranked, kv{k, s})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].v != ranked[j].v {
			return ranked[i].v > ranked[j].v
		}
		return ranked[i].k < ranked[j].k
	})
	v.Provider, v.Confidence = "unknown", "none"
	if len(ranked) > 0 {
		top := ranked[0]
		v.Provider = top.k
		switch {
		case top.v >= 5:
			v.Confidence = "high"
		case top.v >= 3:
			v.Confidence = "medium"
		default:
			v.Confidence = "low"
		}
		if len(ranked) > 1 && ranked[1].v == top.v {
			v.Confidence = "ambiguous"
			v.Notes = append(v.Notes, fmt.Sprintf("tie between %s and %s", top.k, ranked[1].k))
		}
		v.Scores = scores
	}

	// Virtualization.
	sd, _ := env.Virtualization["systemdDetectVirtVM"].(string)
	hv, _ := env.Virtualization["hypervisorCpuFlag"].(bool)
	v.Hypervisor = hypervisorFromDMI(env.DMI)
	switch {
	case strings.HasPrefix(v.OCIShape, "BM."):
		v.Virtualization = "bare-metal (per OCI shape " + v.OCIShape + ")"
	case v.OCIShape != "":
		v.Virtualization = "vm (per OCI shape " + v.OCIShape + ")"
	case sd != "" && sd != "none":
		v.Virtualization = "vm (" + sd + ")"
	case hv:
		v.Virtualization = "vm (hypervisor cpu flag)"
	case sd == "none" || runtime.GOARCH == "amd64":
		v.Virtualization = "bare-metal (likely)"
	default:
		v.Virtualization = "unknown (" + runtime.GOARCH + " exposes no hypervisor cpu flag)"
	}

	v.ContainerRuntime, _ = env.Container["kind"].(string)
	if v.ContainerRuntime == "" {
		v.ContainerRuntime = "unknown"
	}

	techs, _ := env.Confidential["technologies"].([]string)
	if len(techs) == 0 {
		techs = []string{"none-detected"}
	} else {
		v.Notes = append(v.Notes, "confidential-computing features seen: metadata and DMI come from the (untrusted) host; "+
			"bind trust to the hardware attestation report, not to IMDS")
	}
	v.ConfidentialComputing = techs

	if ar, ok := env.Confidential["snpAttestation"].(map[string]any); ok {
		if pol, ok := ar["policy"].(map[string]any); ok {
			if dbg, _ := pol["debug"].(bool); dbg {
				v.Notes = append(v.Notes, "SNP guest policy has DEBUG enabled: the hypervisor can read this guest, so it is NOT confidential")
			}
		}
		if m, _ := ar["measurement"].(string); m != "" {
			v.Notes = append(v.Notes, "SNP attestation report obtained (launch measurement "+truncate(m, 16)+"...); verify its signature chain (VCEK/VLEK -> ASK -> ARK) before trusting it")
		}
	} else if msg, ok := env.Confidential["snpAttestationError"].(string); ok {
		v.Notes = append(v.Notes, "SNP attestation report could not be obtained: "+msg)
	}
	if len(env.DMIUnreadable) > 0 {
		v.Notes = append(v.Notes, "some DMI fields were unreadable (run as root for serial/uuid): "+strings.Join(env.DMIUnreadable, ", "))
	}
	if imdsAttempted && !imdsOK {
		v.Notes = append(v.Notes, "OCI IMDS did not answer; provider verdict is based on local evidence only")
	}
	env.Verdict = v
}
