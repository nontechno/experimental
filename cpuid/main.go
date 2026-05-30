//go:build linux && amd64

package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────

const (
	// AMD MSRs
	msrAMDSEVStatus = 0xC0010131 // SEV_STATUS: bits 0=SEV, 1=SEV-ES, 2=SEV-SNP

	// CPUID leaves
	cpuidLeafBasic       = 0x00000001
	cpuidLeafHypervisor  = 0x40000000
	cpuidLeafTDX         = 0x00000021 // Intel TDX enumeration
	cpuidLeafAMDSEV      = 0x8000001F // AMD SEV/SME feature flags
	cpuidLeafMaxExtended = 0x80000000

	// CPUID leaf 1 ECX bit 31: hypervisor present
	cpuidHypervisorBit = uint32(1 << 31)

	// AMD CPUID 0x8000001F EAX bits
	cpuidSMEBit    = 0
	cpuidSEVBit    = 1
	cpuidSEVESBit  = 3
	cpuidSEVSNPBit = 4
	cpuidVMPLBit   = 5
)

// ──────────────────────────────────────────────────────────────────────────────
// Lookup tables
// ──────────────────────────────────────────────────────────────────────────────

// hvSignatures maps the 12-byte CPUID hypervisor brand string (EBX+ECX+EDX
// from leaf 0x40000000) to a human-readable hypervisor name.
var hvSignatures = map[string]string{
	"KVMKVMKVM":    "KVM",
	"VMwareVMware": "VMware ESXi/Workstation",
	"XenVMMXenVMM": "Xen",
	"Microsoft Hv": "Microsoft Hyper-V",
	"VBoxVBoxVBox": "VirtualBox",
	"bhyve bhyve":  "bhyve",
	"TCGTCGTCGTCG": "QEMU/TCG (software)",
	"ACRNACRNACRN": "ACRN",
	"lrpepyh vr":   "Parallels Desktop",
	"IntelTDX":     "Intel TDX (Trust Domain)",
	"QNXQVMBSQG":   "QNX",
}

// dmiVMHints are substrings in DMI product_name / sys_vendor that suggest
// a virtual machine even without CPUID hypervisor bit.
var dmiVMHints = []struct{ sub, name string }{
	{"KVM", "KVM"},
	{"QEMU", "QEMU"},
	{"VirtualBox", "VirtualBox"},
	{"VMware", "VMware"},
	{"Virtual Machine", "Generic VM"},
	{"Hyper-V", "Hyper-V"},
	{"Bochs", "Bochs"},
	{"Standard PC", "QEMU Standard PC"},
	{"OpenStack", "OpenStack"},
}

// interestingFlags is a curated subset of /proc/cpuinfo flags with descriptions.
var interestingFlags = map[string]string{
	// ── Virtualisation ──────────────────────────────────────────────
	"hypervisor": "Running inside a hypervisor",
	"vmx":        "Intel VT-x (hardware VM)",
	"svm":        "AMD-V (hardware VM)",
	// ── AMD Confidential Computing ──────────────────────────────────
	"sme":     "AMD Secure Memory Encryption",
	"sev":     "AMD Secure Encrypted Virtualisation",
	"sev_es":  "AMD SEV-ES (Encrypted State)",
	"sev_snp": "AMD SEV-SNP (Secure Nested Paging)",
	// ── Intel Confidential Computing ────────────────────────────────
	"tdx_guest": "Intel TDX guest",
	// ── Security / Mitigations ───────────────────────────────────────
	"smep":     "SMEP (Supervisor Mode Execution Prevention)",
	"smap":     "SMAP (Supervisor Mode Access Prevention)",
	"umip":     "UMIP (User Mode Instruction Prevention)",
	"ibrs":     "IBRS (Indirect Branch Restricted Speculation)",
	"ibpb":     "IBPB (Indirect Branch Predictor Barrier)",
	"stibp":    "STIBP (Single Thread Indirect Branch Predictors)",
	"md_clear": "MD_CLEAR / VERW (MDS mitigation)",
	"srbds":    "SRBDS mitigation",
	"tsx_ctrl": "TSX control (TAA mitigation)",
	// ── Crypto ───────────────────────────────────────────────────────
	"aes":    "AES-NI hardware acceleration",
	"vaes":   "Vector AES (VAES, 256/512-bit)",
	"sha_ni": "SHA-1/256 hardware extensions",
	"rdrand": "RDRAND (HW random number generator)",
	"rdseed": "RDSEED (HW random seed)",
	"gfni":   "Galois Field NI (GCM acceleration)",
	// ── ISA Extensions ───────────────────────────────────────────────
	"avx":         "AVX (256-bit SIMD)",
	"avx2":        "AVX2 (256-bit integer SIMD)",
	"avx512f":     "AVX-512 Foundation",
	"avx512bw":    "AVX-512 Byte/Word",
	"avx512vl":    "AVX-512 Vector Length",
	"avx512_vnni": "AVX-512 VNNI (neural network)",
	"avx_vnni":    "AVX VNNI (non-512 path)",
	"amx_tile":    "AMX Tile (Intel matrix engine)",
	"amx_bf16":    "AMX BFloat16",
	// ── Memory ───────────────────────────────────────────────────────
	"pdpe1gb": "1 GiB huge page support",
	"pse":     "4 MiB page support",
	// ── Misc ─────────────────────────────────────────────────────────
	"smx":                "Safer Mode Extensions (Intel TXT/TPM)",
	"tsc_deadline_timer": "TSC deadline timer",
	"x2apic":             "x2APIC",
}

// chassis type codes (SMBIOS spec §7.4.1)
var chassisTypes = map[string]string{
	"1": "Other", "2": "Unknown", "3": "Desktop", "4": "Low Profile Desktop",
	"5": "Pizza Box", "6": "Mini Tower", "7": "Tower", "8": "Portable",
	"9": "Laptop", "10": "Notebook", "11": "Hand Held", "12": "Docking Station",
	"13": "All in One", "14": "Sub Notebook", "15": "Space Saving",
	"17": "Main Server Chassis", "23": "Rack Mount Chassis",
}

// ──────────────────────────────────────────────────────────────────────────────
// ANSI colour helpers
// ──────────────────────────────────────────────────────────────────────────────

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[34m"
	ansiCyan   = "\033[36m"
	ansiGray   = "\033[90m"
	ansiWhite  = "\033[97m"
)

func bold(s string) string   { return ansiBold + s + ansiReset }
func green(s string) string  { return ansiGreen + s + ansiReset }
func yellow(s string) string { return ansiYellow + s + ansiReset }
func red(s string) string    { return ansiRed + s + ansiReset }
func cyan(s string) string   { return ansiCyan + s + ansiReset }
func gray(s string) string   { return ansiGray + s + ansiReset }
func blue(s string) string   { return ansiBlue + s + ansiReset }

func yesNo(v bool) string {
	if v {
		return green("Yes")
	}
	return red("No")
}
func present(v bool) string {
	if v {
		return green("Present")
	}
	return gray("Not present")
}
func activeStr(v bool) string {
	if v {
		return green("Active")
	}
	return gray("Inactive")
}
func supportedStr(v bool) string {
	if v {
		return green("Supported")
	}
	return gray("Not supported")
}

// ──────────────────────────────────────────────────────────────────────────────
// /proc/cpuinfo
// ──────────────────────────────────────────────────────────────────────────────

type CPUInfo struct {
	ModelName      string
	Vendor         string
	Stepping       string
	MicroCode      string
	Flags          map[string]bool
	PhysicalIDs    map[string]bool // unique physical package IDs
	CoreIDs        map[string]bool // "physID:coreID" pairs
	CoresPerSocket int
	Siblings       int // logical CPUs per socket
	LogicalCPUs    int
	CPUMHz         string
	CacheSize      string
	APICID         string
}

func parseCPUInfo() CPUInfo {
	info := CPUInfo{
		Flags:       make(map[string]bool),
		PhysicalIDs: make(map[string]bool),
		CoreIDs:     make(map[string]bool),
	}

	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return info
	}
	defer f.Close()

	currentPhysID := "0"
	flagsParsed := false
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "model name":
			if info.ModelName == "" {
				info.ModelName = val
			}
		case "vendor_id":
			if info.Vendor == "" {
				info.Vendor = val
			}
		case "stepping":
			if info.Stepping == "" {
				info.Stepping = val
			}
		case "microcode":
			if info.MicroCode == "" {
				info.MicroCode = val
			}
		case "cpu MHz":
			if info.CPUMHz == "" {
				info.CPUMHz = val
			}
		case "cache size":
			if info.CacheSize == "" {
				info.CacheSize = val
			}
		case "flags", "Features":
			if !flagsParsed {
				for _, flag := range strings.Fields(val) {
					info.Flags[flag] = true
				}
				flagsParsed = true
			}
		case "physical id":
			currentPhysID = val
			info.PhysicalIDs[val] = true
		case "core id":
			info.CoreIDs[currentPhysID+":"+val] = true
		case "cpu cores":
			if n, err := strconv.Atoi(val); err == nil && info.CoresPerSocket == 0 {
				info.CoresPerSocket = n
			}
		case "siblings":
			if n, err := strconv.Atoi(val); err == nil && info.Siblings == 0 {
				info.Siblings = n
			}
		}
	}

	info.LogicalCPUs = runtime.NumCPU()
	return info
}

// ──────────────────────────────────────────────────────────────────────────────
// /proc/meminfo
// ──────────────────────────────────────────────────────────────────────────────

type MemInfo struct {
	MemTotal       uint64
	MemFree        uint64
	MemAvailable   uint64
	Buffers        uint64
	Cached         uint64
	SwapTotal      uint64
	SwapFree       uint64
	HugePageSize   uint64 // kB per huge page
	HugePagesTotal uint64
	HugePagesFree  uint64
	DirectMap4k    uint64
	DirectMap2M    uint64
	DirectMap1G    uint64
}

func parseMemInfo() MemInfo {
	var m MemInfo
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return m
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			m.MemTotal = val
		case "MemFree":
			m.MemFree = val
		case "MemAvailable":
			m.MemAvailable = val
		case "Buffers":
			m.Buffers = val
		case "Cached":
			m.Cached = val
		case "SwapTotal":
			m.SwapTotal = val
		case "SwapFree":
			m.SwapFree = val
		case "Hugepagesize":
			m.HugePageSize = val
		case "HugePages_Total":
			m.HugePagesTotal = val
		case "HugePages_Free":
			m.HugePagesFree = val
		case "DirectMap4k":
			m.DirectMap4k = val
		case "DirectMap2M":
			m.DirectMap2M = val
		case "DirectMap1G":
			m.DirectMap1G = val
		}
	}
	return m
}

// ──────────────────────────────────────────────────────────────────────────────
// DMI / SMBIOS  (/sys/class/dmi/id/)
// ──────────────────────────────────────────────────────────────────────────────

type DMIInfo struct {
	ProductName string
	ProductUUID string
	SysVendor   string
	BoardName   string
	BoardVendor string
	BIOSVendor  string
	BIOSVersion string
	BIOSRelease string
	ChassisType string
}

func parseDMI() DMIInfo {
	rd := func(field string) string {
		data, err := os.ReadFile("/sys/class/dmi/id/" + field)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	return DMIInfo{
		ProductName: rd("product_name"),
		ProductUUID: rd("product_uuid"),
		SysVendor:   rd("sys_vendor"),
		BoardName:   rd("board_name"),
		BoardVendor: rd("board_vendor"),
		BIOSVendor:  rd("bios_vendor"),
		BIOSVersion: rd("bios_version"),
		BIOSRelease: rd("bios_release"),
		ChassisType: rd("chassis_type"),
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// OS / kernel
// ──────────────────────────────────────────────────────────────────────────────

type OSInfo struct {
	Hostname      string
	KernelRelease string
	KernelVersion string
	Distro        string
	Cmdline       string
}

func parseOSInfo() OSInfo {
	var info OSInfo
	info.Hostname = readFile("/proc/sys/kernel/hostname")
	info.KernelRelease = readFile("/proc/sys/kernel/osrelease")
	info.KernelVersion = readFile("/proc/version")
	info.Cmdline = readFile("/proc/cmdline")

	// /etc/os-release
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(data)))
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				info.Distro = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
				break
			}
		}
	}
	return info
}

// ──────────────────────────────────────────────────────────────────────────────
// Container detection
// ──────────────────────────────────────────────────────────────────────────────

type ContainerInfo struct {
	Detected bool
	Runtime  string
	Evidence []string
}

func detectContainer() ContainerInfo {
	var info ContainerInfo
	add := func(runtime, evidence string) {
		info.Detected = true
		if info.Runtime == "" {
			info.Runtime = runtime
		}
		info.Evidence = append(info.Evidence, evidence)
	}

	// Docker
	if fileExists("/.dockerenv") {
		add("Docker", "/.dockerenv present")
	}

	// Podman
	if fileExists("/run/.containerenv") {
		add("Podman", "/run/.containerenv present")
	}

	// cgroup v1: /proc/1/cgroup content
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		for _, p := range []struct{ pat, name string }{
			{"docker", "Docker"},
			{"kubepods", "Kubernetes"},
			{"containerd", "containerd"},
			{"lxc", "LXC"},
			{"crio-", "CRI-O"},
			{"garden", "Cloud Foundry Garden"},
		} {
			if strings.Contains(content, p.pat) {
				add(p.name, fmt.Sprintf("/proc/1/cgroup contains %q", p.pat))
				break
			}
		}
	}

	// systemd nspawn / container manager
	if data, err := os.ReadFile("/run/host/container-manager"); err == nil {
		add(strings.TrimSpace(string(data)), "/run/host/container-manager present")
	}

	// $container env var (set by systemd-nspawn, podman, etc.)
	if v := os.Getenv("container"); v != "" {
		add(v, fmt.Sprintf("$container=%q env var", v))
	}

	// cgroup namespace isolation: if we're in a different cgroup ns than PID 1,
	// we're in a nested container.
	if selfNS, err1 := os.Readlink("/proc/self/ns/cgroup"); err1 == nil {
		if initNS, err2 := os.Readlink("/proc/1/ns/cgroup"); err2 == nil && selfNS != initNS {
			add("", "cgroup namespace differs from PID 1 (nested container)")
		}
	}

	// PID 1 being a container init shim
	if exe, err := os.Readlink("/proc/1/exe"); err == nil {
		for _, shim := range []string{"pause", "tini", "dumb-init", "s6-svscan"} {
			if strings.Contains(exe, shim) {
				add("", fmt.Sprintf("PID 1 exe contains %q: %s", shim, exe))
				break
			}
		}
	}

	if info.Runtime == "" && info.Detected {
		info.Runtime = "Unknown"
	}
	return info
}

// ──────────────────────────────────────────────────────────────────────────────
// Hypervisor detection
// ──────────────────────────────────────────────────────────────────────────────

type HypervisorInfo struct {
	Detected    bool
	CPUIDBitSet bool   // CPUID leaf 1 ECX[31]
	Signature   string // raw 12-byte brand string (printable)
	Name        string
	MaxHVLeaf   uint32
	Evidence    []string
}

func detectHypervisor(flags map[string]bool, dmi DMIInfo) HypervisorInfo {
	var info HypervisorInfo
	add := func(ev string) { info.Evidence = append(info.Evidence, ev) }

	// ── CPUID leaf 1: hypervisor present bit ─────────────────────────────────
	_, _, ecx1, _ := cpuid(cpuidLeafBasic, 0)
	if ecx1&cpuidHypervisorBit != 0 {
		info.Detected = true
		info.CPUIDBitSet = true
		add("CPUID leaf 1 ECX[31]=1 (hypervisor bit)")
	}

	// ── CPUID leaf 0x40000000: hypervisor brand string ───────────────────────
	eax40, ebx40, ecx40, edx40 := cpuid(cpuidLeafHypervisor, 0)
	if info.CPUIDBitSet {
		info.MaxHVLeaf = eax40
		b := make([]byte, 12)
		binary.LittleEndian.PutUint32(b[0:4], ebx40)
		binary.LittleEndian.PutUint32(b[4:8], ecx40)
		binary.LittleEndian.PutUint32(b[8:12], edx40)
		// Make printable
		for i, c := range b {
			if c < 0x20 || c >= 0x7F {
				b[i] = '.'
			}
		}
		info.Signature = strings.TrimRight(string(b), ".")
		// Match against known signatures
		trimSig := strings.TrimRight(info.Signature, " .")
		for pattern, name := range hvSignatures {
			if strings.HasPrefix(trimSig, pattern) || trimSig == pattern {
				info.Name = name
				break
			}
		}
		if info.Name == "" {
			info.Name = fmt.Sprintf("Unknown (%q)", info.Signature)
		}
		add(fmt.Sprintf("CPUID 0x40000000 brand: %q → %s", info.Signature, info.Name))
	}

	// ── /proc/cpuinfo "hypervisor" flag ──────────────────────────────────────
	if flags["hypervisor"] {
		info.Detected = true
		add(`/proc/cpuinfo "hypervisor" flag set`)
	}

	// ── DMI product name / vendor hints ──────────────────────────────────────
	for _, h := range dmiVMHints {
		if strings.Contains(dmi.ProductName, h.sub) || strings.Contains(dmi.SysVendor, h.sub) {
			info.Detected = true
			if info.Name == "" {
				info.Name = h.name
			}
			add(fmt.Sprintf("DMI product_name=%q matches %q", dmi.ProductName, h.sub))
			break
		}
	}

	// ── /sys/hypervisor/type (Xen exposes this) ───────────────────────────────
	if data, err := os.ReadFile("/sys/hypervisor/type"); err == nil {
		hvType := strings.TrimSpace(string(data))
		info.Detected = true
		if info.Name == "" {
			info.Name = "Xen"
		}
		add(fmt.Sprintf("/sys/hypervisor/type=%q", hvType))
	}

	// /proc/xen
	if fileExists("/proc/xen") {
		info.Detected = true
		if info.Name == "" {
			info.Name = "Xen"
		}
		add("/proc/xen exists")
	}

	// ── Hyper-V: extended CPUID interface ID (leaf 0x40000001) ───────────────
	if info.Name == "Microsoft Hyper-V" && info.MaxHVLeaf >= 0x40000001 {
		eax41, _, _, _ := cpuid(0x40000001, 0)
		add(fmt.Sprintf("Hyper-V interface signature: 0x%08X", eax41))
	}

	return info
}

// ──────────────────────────────────────────────────────────────────────────────
// Confidential computing / CVM
// ──────────────────────────────────────────────────────────────────────────────

type SEVDetails struct {
	// CPUID 0x8000001F capability bits (what the CPU/hypervisor advertises)
	SMECapable   bool
	SEVCapable   bool
	SEVESCapable bool
	SNPCapable   bool
	VMPLCapable  bool
	// Encrypted address bit position and physical address reduction
	EncAddrBit        uint32
	PhysAddrReduction uint32
	// /proc/cpuinfo flags (kernel-confirmed active features)
	FlagSEV    bool
	FlagSEVES  bool
	FlagSEVSNP bool
	// MSR 0xC0010131 (root only, requires modprobe msr)
	MSRRead    bool
	MSRSEVOn   bool
	MSRSEVESOn bool
	MSRSNPOn   bool
}

type CVMInfo struct {
	IsCVM       bool
	CVMType     string // "AMD SEV-SNP", "AMD SEV-ES", "AMD SEV", "Intel TDX", ""
	SEV         SEVDetails
	TDXGuest    bool // /dev/tdx-guest or CPUID evidence
	ACPIHasCCEL bool
	Evidence    []string
}

func detectCVM(flags map[string]bool) CVMInfo {
	var info CVMInfo
	add := func(ev string) { info.Evidence = append(info.Evidence, ev) }

	// ── AMD SEV capabilities via CPUID 0x8000001F ────────────────────────────
	// Safe to call on Intel too; returns 0 if leaf unsupported.
	eax8f, ebx8f, _, _ := cpuid(cpuidLeafAMDSEV, 0)
	info.SEV.SMECapable = eax8f>>cpuidSMEBit&1 == 1
	info.SEV.SEVCapable = eax8f>>cpuidSEVBit&1 == 1
	info.SEV.SEVESCapable = eax8f>>cpuidSEVESBit&1 == 1
	info.SEV.SNPCapable = eax8f>>cpuidSEVSNPBit&1 == 1
	info.SEV.VMPLCapable = eax8f>>cpuidVMPLBit&1 == 1
	info.SEV.EncAddrBit = ebx8f & 0x3F
	info.SEV.PhysAddrReduction = (ebx8f >> 6) & 0x3F

	// ── /proc/cpuinfo flags ───────────────────────────────────────────────────
	info.SEV.FlagSEV = flags["sev"]
	info.SEV.FlagSEVES = flags["sev_es"]
	info.SEV.FlagSEVSNP = flags["sev_snp"]

	if flags["sev_snp"] {
		info.IsCVM = true
		info.CVMType = "AMD SEV-SNP"
		add(`/proc/cpuinfo "sev_snp" flag present`)
	} else if flags["sev_es"] {
		info.IsCVM = true
		info.CVMType = "AMD SEV-ES"
		add(`/proc/cpuinfo "sev_es" flag present`)
	} else if flags["sev"] {
		info.IsCVM = true
		info.CVMType = "AMD SEV"
		add(`/proc/cpuinfo "sev" flag present`)
	}

	if flags["tdx_guest"] {
		info.IsCVM = true
		info.TDXGuest = true
		if info.CVMType == "" {
			info.CVMType = "Intel TDX"
		}
		add(`/proc/cpuinfo "tdx_guest" flag present`)
	}

	// ── AMD SEV_STATUS MSR 0xC0010131 ────────────────────────────────────────
	if msrVal, err := readMSR(0, msrAMDSEVStatus); err == nil {
		info.SEV.MSRRead = true
		info.SEV.MSRSEVOn = msrVal>>0&1 == 1
		info.SEV.MSRSEVESOn = msrVal>>1&1 == 1
		info.SEV.MSRSNPOn = msrVal>>2&1 == 1
		if info.SEV.MSRSNPOn {
			info.IsCVM = true
			if info.CVMType == "" {
				info.CVMType = "AMD SEV-SNP"
			}
			add("MSR 0xC0010131[2]=1 (SEV-SNP active)")
		} else if info.SEV.MSRSEVESOn {
			info.IsCVM = true
			if info.CVMType == "" {
				info.CVMType = "AMD SEV-ES"
			}
			add("MSR 0xC0010131[1]=1 (SEV-ES active)")
		} else if info.SEV.MSRSEVOn {
			info.IsCVM = true
			if info.CVMType == "" {
				info.CVMType = "AMD SEV"
			}
			add("MSR 0xC0010131[0]=1 (SEV active)")
		}
	}

	// ── Intel TDX: CPUID leaf 0x21 ───────────────────────────────────────────
	// Leaf 0x21 carries "IntelTDX" signature in EBX+ECX+EDX on TDX guests.
	eax21, ebx21, ecx21, edx21 := cpuid(cpuidLeafTDX, 0)
	if ebx21|ecx21|edx21 != 0 {
		sig := make([]byte, 12)
		binary.LittleEndian.PutUint32(sig[0:4], ebx21)
		binary.LittleEndian.PutUint32(sig[4:8], ecx21)
		binary.LittleEndian.PutUint32(sig[8:12], edx21)
		if strings.Contains(string(sig), "IntelTDX") {
			info.IsCVM = true
			info.TDXGuest = true
			if info.CVMType == "" {
				info.CVMType = "Intel TDX"
			}
			add(fmt.Sprintf("CPUID leaf 0x21 TDX signature: %q (max sub-leaf %d)", sig, eax21))
		}
	}

	// ── /dev/sev-guest ────────────────────────────────────────────────────────
	if fileExists("/dev/sev-guest") {
		info.IsCVM = true
		add("/dev/sev-guest present (AMD SEV guest device)")
		if info.CVMType == "" {
			info.CVMType = "AMD SEV"
		}
	}

	// ── /dev/tdx-guest ────────────────────────────────────────────────────────
	if fileExists("/dev/tdx-guest") {
		info.IsCVM = true
		info.TDXGuest = true
		add("/dev/tdx-guest present (Intel TDX guest device)")
		if info.CVMType == "" {
			info.CVMType = "Intel TDX"
		}
	}

	// ── ACPI CCEL table (Confidential Computing Event Log) ───────────────────
	if fileExists("/sys/firmware/acpi/tables/CCEL") {
		info.ACPIHasCCEL = true
		info.IsCVM = true
		add("ACPI CCEL table present (Confidential Computing Event Log)")
		// CCEL is used by both AMD SNP and Intel TDX
	}

	return info
}

// ──────────────────────────────────────────────────────────────────────────────
// MSR reading  (root + msr module required)
// ──────────────────────────────────────────────────────────────────────────────

func readMSR(cpu int, msr uint32) (uint64, error) {
	path := fmt.Sprintf("/dev/cpu/%d/msr", cpu)
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, 8)
	n, err := syscall.Pread(int(f.Fd()), buf, int64(msr))
	if err != nil {
		return 0, fmt.Errorf("pread MSR 0x%X: %w", msr, err)
	}
	if n != 8 {
		return 0, fmt.Errorf("short read: %d bytes", n)
	}
	return binary.LittleEndian.Uint64(buf), nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Privilege detection
// ──────────────────────────────────────────────────────────────────────────────

func isRoot() bool { return os.Getuid() == 0 }

// ──────────────────────────────────────────────────────────────────────────────
// Kernel modules
// ──────────────────────────────────────────────────────────────────────────────

func moduleLoaded(name string) bool {
	return fileExists("/sys/module/" + name)
}

// ──────────────────────────────────────────────────────────────────────────────
// ACPI tables
// ──────────────────────────────────────────────────────────────────────────────

func listACPITables() []string {
	entries, err := filepath.Glob("/sys/firmware/acpi/tables/*")
	if err != nil {
		return nil
	}
	var tables []string
	for _, e := range entries {
		fi, err := os.Stat(e)
		if err == nil && !fi.IsDir() {
			tables = append(tables, filepath.Base(e))
		}
	}
	sort.Strings(tables)
	return tables
}

// ──────────────────────────────────────────────────────────────────────────────
// Formatting helpers
// ──────────────────────────────────────────────────────────────────────────────

// formatKiB converts a kibibyte value (as returned by /proc/meminfo) to a
// human-readable string using binary prefixes.
func formatKiB(kib uint64) string {
	b := kib * 1024
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.2f TiB", float64(b)/float64(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

const sectionWidth = 62

func printBanner() {
	line := strings.Repeat("═", sectionWidth-2)
	fmt.Printf("\n%s╔%s╗%s\n", ansiBold+ansiCyan, line, ansiReset)
	title := "  Linux System & Confidential Computing Inspector  "
	pad := sectionWidth - 2 - len(title)
	fmt.Printf("%s║%s%s%s%s%s║%s\n",
		ansiBold+ansiCyan, ansiReset+ansiBold+ansiWhite,
		title, strings.Repeat(" ", pad),
		ansiReset+ansiBold+ansiCyan, "", ansiReset)
	sub := "  AMD64 · Linux · stdlib only                       "
	pad2 := sectionWidth - 2 - len(sub)
	fmt.Printf("%s║%s%s%s%s║%s\n",
		ansiBold+ansiCyan, ansiGray,
		sub, strings.Repeat(" ", pad2),
		ansiBold+ansiCyan, ansiReset)
	fmt.Printf("%s╚%s╝%s\n\n", ansiBold+ansiCyan, line, ansiReset)
}

func section(title string) {
	fmt.Printf("\n%s%s %-*s%s\n",
		ansiBold+ansiCyan, "▶", sectionWidth-3, title, ansiReset)
	fmt.Printf("%s%s%s\n", ansiGray, strings.Repeat("─", sectionWidth), ansiReset)
}

const labelWidth = 26

func field(label, value string) {
	fmt.Printf("  %s%-*s%s  %s\n", ansiGray, labelWidth, label, ansiReset, value)
}

func fieldFmt(label, format string, args ...any) {
	field(label, fmt.Sprintf(format, args...))
}

func subfield(label, value string) {
	fmt.Printf("    %s%-*s%s  %s\n", ansiGray, labelWidth-2, label, ansiReset, value)
}

func subfieldFmt(label, format string, args ...any) {
	subfield(label, fmt.Sprintf(format, args...))
}

func subsection(title string) {
	fmt.Printf("\n  %s%s%s\n", ansiBold, title, ansiReset)
}

// ──────────────────────────────────────────────────────────────────────────────
// Misc file helpers
// ──────────────────────────────────────────────────────────────────────────────

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dirHasEntries(path string) bool {
	e, err := os.ReadDir(path)
	return err == nil && len(e) > 0
}

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readEFIVar(name string) string {
	data, err := os.ReadFile("/sys/firmware/efi/efivars/" + name)
	if err != nil || len(data) < 5 {
		return ""
	}
	// First 4 bytes are EFI attributes; remaining bytes are the value.
	val := data[4:]
	if len(val) == 1 {
		if val[0] == 1 {
			return green("Enabled")
		}
		return red("Disabled")
	}
	return fmt.Sprintf("%x", val)
}

func countNUMANodes() int {
	entries, _ := filepath.Glob("/sys/devices/system/node/node*")
	if len(entries) == 0 {
		return 1
	}
	return len(entries)
}

// ──────────────────────────────────────────────────────────────────────────────
// main
// ──────────────────────────────────────────────────────────────────────────────

func main() {
	printBanner()

	// ── Gather everything upfront ────────────────────────────────────────────
	cpu := parseCPUInfo()
	mem := parseMemInfo()
	dmi := parseDMI()
	osInfo := parseOSInfo()
	ctr := detectContainer()
	hv := detectHypervisor(cpu.Flags, dmi)
	cvm := detectCVM(cpu.Flags)

	// ── Determine top-level environment type ─────────────────────────────────
	var envType, envConf string
	switch {
	case ctr.Detected && hv.Detected:
		envType = bold(cyan("Container")) + " (inside a VM)"
		envConf = "High"
	case ctr.Detected:
		envType = bold(cyan("Container"))
		envConf = "High"
	case hv.Detected && hv.CPUIDBitSet:
		envType = bold(yellow("Virtual Machine"))
		envConf = "High — CPUID hypervisor bit + " + func() string {
			if hv.Name != "" {
				return hv.Name
			}
			return "DMI"
		}()
	case hv.Detected:
		envType = bold(yellow("Virtual Machine (probable)"))
		envConf = "Medium — DMI/sysfs only (no CPUID HV bit)"
	default:
		envType = bold(green("Bare Metal (likely)"))
		envConf = "Medium — no hypervisor indicators found"
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: System
	// ════════════════════════════════════════════════════════════════════════
	section("SYSTEM")
	field("Hostname", osInfo.Hostname)
	field("Distro", func() string {
		if osInfo.Distro != "" {
			return osInfo.Distro
		}
		return gray("(unknown)")
	}())
	field("Kernel", osInfo.KernelRelease)
	field("Architecture", runtime.GOARCH)
	field("Running as", func() string {
		if isRoot() {
			return green("root") + gray(" (full access to MSRs, /dev/cpu/*)")
		}
		return yellow("unprivileged") + gray(" (MSR reads will fail)")
	}())

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: Environment
	// ════════════════════════════════════════════════════════════════════════
	section("ENVIRONMENT TYPE")
	field("Type", envType)
	field("Confidence", envConf)

	if ctr.Detected {
		field("Container Runtime", cyan(ctr.Runtime))
		for _, ev := range ctr.Evidence {
			subfield("Evidence", ev)
		}
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: Hypervisor
	// ════════════════════════════════════════════════════════════════════════
	section("HYPERVISOR")
	field("Hypervisor detected", yesNo(hv.Detected))

	if hv.Detected {
		field("CPUID HV bit (leaf 1 ECX[31])", yesNo(hv.CPUIDBitSet))

		if hv.Signature != "" {
			field("Brand string (leaf 0x40000000)",
				fmt.Sprintf("%q", hv.Signature))
		}
		if hv.Name != "" {
			field("Identified as", bold(cyan(hv.Name)))
		}
		if hv.MaxHVLeaf > 0 {
			fieldFmt("Max hypervisor CPUID leaf", "0x%08X", hv.MaxHVLeaf)
		}

		// KVM-specific leaf 0x40000001 (KVM features)
		if strings.Contains(hv.Name, "KVM") && hv.MaxHVLeaf >= 0x40000001 {
			eaxKVM, _, _, _ := cpuid(0x40000001, 0)
			subsection("KVM feature bits (leaf 0x40000001):")
			kvmFeatures := []struct {
				bit  uint32
				name string
			}{
				{0, "KVM_FEATURE_CLOCKSOURCE"},
				{1, "KVM_FEATURE_NOP_IO_DELAY"},
				{2, "KVM_FEATURE_MMU_OP (deprecated)"},
				{3, "KVM_FEATURE_CLOCKSOURCE2"},
				{4, "KVM_FEATURE_ASYNC_PF"},
				{5, "KVM_FEATURE_STEAL_TIME"},
				{6, "KVM_FEATURE_PV_EOI"},
				{7, "KVM_FEATURE_PV_UNHALT"},
				{9, "KVM_FEATURE_PV_TLB_FLUSH"},
				{10, "KVM_FEATURE_ASYNC_PF_VMEXIT"},
				{11, "KVM_FEATURE_PV_SEND_IPI"},
				{12, "KVM_FEATURE_POLL_CONTROL"},
				{13, "KVM_FEATURE_PV_SCHED_YIELD"},
				{24, "KVM_FEATURE_CLOCSOURCE_STABLE_BIT"},
			}
			for _, f := range kvmFeatures {
				if eaxKVM>>f.bit&1 == 1 {
					subfield(f.name, green("set"))
				}
			}
		}

		subsection("Evidence:")
		for _, ev := range hv.Evidence {
			subfield("•", ev)
		}
	}

	// KVM host check (if we appear to be on bare metal with KVM loaded)
	if !hv.Detected {
		subsection("Host virtualisation capability:")
		subfield("Intel VT-x (vmx flag)", supportedStr(cpu.Flags["vmx"]))
		subfield("AMD-V (svm flag)", supportedStr(cpu.Flags["svm"]))
		subfield("kvm module loaded", present(moduleLoaded("kvm")))
		subfield("kvm_amd module loaded", present(moduleLoaded("kvm_amd")))
		subfield("kvm_intel module loaded", present(moduleLoaded("kvm_intel")))
		subfield("/dev/kvm", present(fileExists("/dev/kvm")))
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: Confidential Computing
	// ════════════════════════════════════════════════════════════════════════
	section("CONFIDENTIAL COMPUTING")

	if cvm.IsCVM {
		field("CVM status", bold(green(fmt.Sprintf("✓ Confidential VM — %s", cvm.CVMType))))
	} else {
		field("CVM status", gray("Not a Confidential VM"))
	}

	// AMD SEV
	subsection("AMD SEV (CPUID leaf 0x8000001F):")
	if cpu.Vendor != "AuthenticAMD" {
		subfield("Status", gray(fmt.Sprintf("N/A — CPU vendor is %q (AMD only)", cpu.Vendor)))
	} else {
		subfield("SME capable", supportedStr(cvm.SEV.SMECapable))
		subfield("SEV capable", supportedStr(cvm.SEV.SEVCapable))
		subfield("SEV-ES capable", supportedStr(cvm.SEV.SEVESCapable))
		subfield("SEV-SNP capable", supportedStr(cvm.SEV.SNPCapable))
		subfield("VMPL capable", supportedStr(cvm.SEV.VMPLCapable))
		if cvm.SEV.EncAddrBit > 0 {
			subfieldFmt("C-bit position", "bit %d (guest physical address encryption bit)", cvm.SEV.EncAddrBit)
			subfieldFmt("Phys addr reduction", "%d bits", cvm.SEV.PhysAddrReduction)
		}
	}

	subsection("AMD SEV status MSR 0xC0010131:")
	if cpu.Vendor != "AuthenticAMD" {
		subfield("Status", gray(fmt.Sprintf("N/A — CPU vendor is %q (AMD only)", cpu.Vendor)))
	} else if !cvm.SEV.MSRRead {
		if isRoot() {
			subfield("Status", yellow("Not readable — is 'msr' module loaded? (modprobe msr)"))
		} else {
			subfield("Status", yellow("Not readable — re-run as root"))
		}
	} else {
		subfield("SEV active", activeStr(cvm.SEV.MSRSEVOn))
		subfield("SEV-ES active", activeStr(cvm.SEV.MSRSEVESOn))
		subfield("SEV-SNP active", activeStr(cvm.SEV.MSRSNPOn))
	}

	subsection("/proc/cpuinfo flags (kernel-confirmed):")
	subfield("sev", activeStr(cvm.SEV.FlagSEV))
	subfield("sev_es", activeStr(cvm.SEV.FlagSEVES))
	subfield("sev_snp", activeStr(cvm.SEV.FlagSEVSNP))
	subfield("tdx_guest", activeStr(cpu.Flags["tdx_guest"]))

	subsection("Intel TDX:")
	subfield("CPUID leaf 0x21 signature", supportedStr(cvm.TDXGuest && !fileExists("/dev/sev-guest")))
	subfield("/dev/tdx-guest", present(fileExists("/dev/tdx-guest")))

	subsection("Attestation devices:")
	subfield("/dev/sev-guest", present(fileExists("/dev/sev-guest")))
	subfield("/dev/sev", present(fileExists("/dev/sev")))
	subfield("/dev/tdx-guest", present(fileExists("/dev/tdx-guest")))
	subfield("/dev/tpm0", present(fileExists("/dev/tpm0")))
	subfield("/dev/tpmrm0", present(fileExists("/dev/tpmrm0")))
	subfield("ACPI CCEL table", present(cvm.ACPIHasCCEL))

	if len(cvm.Evidence) > 0 {
		subsection("Evidence:")
		for _, ev := range cvm.Evidence {
			subfield("•", ev)
		}
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: CPU
	// ════════════════════════════════════════════════════════════════════════
	section("CPU")
	field("Model", cpu.ModelName)
	field("Vendor", cpu.Vendor)
	if cpu.Stepping != "" {
		field("Stepping", cpu.Stepping)
	}
	if cpu.MicroCode != "" {
		field("Microcode", cpu.MicroCode)
	}
	if cpu.CPUMHz != "" {
		field("CPU MHz (first core)", cpu.CPUMHz)
	}
	if cpu.CacheSize != "" {
		field("L2/L3 cache", cpu.CacheSize)
	}

	sockets := len(cpu.PhysicalIDs)
	if sockets == 0 {
		sockets = 1
	}
	physCores := len(cpu.CoreIDs)
	if physCores == 0 {
		physCores = cpu.CoresPerSocket * sockets
	}
	if physCores == 0 {
		physCores = cpu.LogicalCPUs // fallback
	}
	field("Sockets", fmt.Sprintf("%d", sockets))
	fieldFmt("Physical cores", "%d", physCores)
	fieldFmt("Logical CPUs", "%d", cpu.LogicalCPUs)
	if physCores > 0 && cpu.LogicalCPUs > physCores {
		fieldFmt("Hyperthreading (SMT)", green("Enabled")+" (%d threads per core)",
			cpu.LogicalCPUs/physCores)
	} else {
		field("Hyperthreading (SMT)", yellow("Disabled or 1:1"))
	}
	field("NUMA nodes", fmt.Sprintf("%d", countNUMANodes()))

	// CPUID topology: cores per package, max APIC IDs
	eaxTopo, _, _, _ := cpuid(0x0B, 0) // Intel thread-level topology
	_ = eaxTopo                        // informational; we've already computed above

	// AMD CPU topology via CPUID 0x8000001E
	eaxAMDTopo, ebxAMDTopo, ecxAMDTopo, _ := cpuid(0x8000001E, 0)
	if cpu.Vendor == "AuthenticAMD" && ebxAMDTopo != 0 {
		subsection("AMD extended topology (leaf 0x8000001E):")
		subfieldFmt("Extended APIC ID", "%d", eaxAMDTopo)
		subfieldFmt("Compute unit ID", "%d", ebxAMDTopo&0xFF)
		subfieldFmt("Cores per CU", "%d", (ebxAMDTopo>>8)&0xFF+1)
		subfieldFmt("Node ID", "%d", ecxAMDTopo&0xFF)
		subfieldFmt("Nodes per socket", "%d", (ecxAMDTopo>>8)&0x7+1)
	}

	// Virtualisation support
	subsection("Virtualisation support:")
	subfield("Intel VT-x (vmx)", supportedStr(cpu.Flags["vmx"]))
	subfield("AMD-V (svm)", supportedStr(cpu.Flags["svm"]))
	iommu := dirHasEntries("/sys/class/iommu") ||
		moduleLoaded("vfio_iommu_type1")
	subfield("IOMMU present", present(iommu))

	// Notable flags
	subsection("Notable CPU flags:")
	var notable []string
	for f := range interestingFlags {
		if cpu.Flags[f] {
			notable = append(notable, f)
		}
	}
	sort.Strings(notable)
	if len(notable) == 0 {
		subfield("(none from the curated list)", "")
	}
	for _, f := range notable {
		subfield(f, gray(interestingFlags[f]))
	}

	// All flags (collapsed)
	allFlags := make([]string, 0, len(cpu.Flags))
	for f := range cpu.Flags {
		allFlags = append(allFlags, f)
	}
	sort.Strings(allFlags)
	subsection(fmt.Sprintf("All flags (%d total):", len(allFlags)))
	// Print in rows of ~6
	for i := 0; i < len(allFlags); i += 6 {
		end := i + 6
		if end > len(allFlags) {
			end = len(allFlags)
		}
		fmt.Printf("    %s\n", strings.Join(allFlags[i:end], "  "))
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: Memory
	// ════════════════════════════════════════════════════════════════════════
	section("MEMORY")
	field("Total", formatKiB(mem.MemTotal))
	field("Available", formatKiB(mem.MemAvailable))
	field("Free", formatKiB(mem.MemFree))
	field("Buffers", formatKiB(mem.Buffers))
	field("Cached", formatKiB(mem.Cached))
	used := mem.MemTotal - mem.MemAvailable
	pct := 0.0
	if mem.MemTotal > 0 {
		pct = float64(used) / float64(mem.MemTotal) * 100
	}
	field("Used", fmt.Sprintf("%s  (%.1f%%)", formatKiB(used), pct))
	if mem.SwapTotal > 0 {
		swapUsed := mem.SwapTotal - mem.SwapFree
		swapPct := float64(swapUsed) / float64(mem.SwapTotal) * 100
		field("Swap total", formatKiB(mem.SwapTotal))
		field("Swap used", fmt.Sprintf("%s  (%.1f%%)", formatKiB(swapUsed), swapPct))
	} else {
		field("Swap", gray("None configured"))
	}

	if mem.HugePagesTotal > 0 {
		subsection("Huge pages (pre-allocated):")
		subfieldFmt("Page size", "%s", formatKiB(mem.HugePageSize))
		subfieldFmt("Total pages", "%d  (%s reserved)", mem.HugePagesTotal,
			formatKiB(mem.HugePagesTotal*mem.HugePageSize))
		subfieldFmt("Free pages", "%d", mem.HugePagesFree)
	} else {
		subsection("Huge pages:")
		subfield("Pre-allocated", gray("None (HugePages_Total=0)"))
	}
	// Check transparent huge pages
	thp := readFile("/sys/kernel/mm/transparent_hugepage/enabled")
	if thp != "" {
		subsection("Transparent Huge Pages (THP):")
		subfield("Setting", thp)
	}

	if mem.DirectMap1G > 0 || mem.DirectMap2M > 0 {
		subsection("Kernel direct map (linear mapping):")
		subfield("4K pages", formatKiB(mem.DirectMap4k))
		subfield("2M pages", formatKiB(mem.DirectMap2M))
		if mem.DirectMap1G > 0 {
			subfield("1G pages", formatKiB(mem.DirectMap1G))
		}
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: Firmware / SMBIOS
	// ════════════════════════════════════════════════════════════════════════
	section("FIRMWARE & SMBIOS (DMI)")
	field("System vendor", dmi.SysVendor)
	field("Product name", dmi.ProductName)
	field("Product UUID", func() string {
		uuid := dmi.ProductUUID
		if uuid == "" {
			if isRoot() {
				return gray("(not available — possibly not exposed by hypervisor)")
			}
			return gray("(not readable — need root)")
		}
		return uuid
	}())
	field("Board vendor", dmi.BoardVendor)
	field("Board name", dmi.BoardName)
	field("BIOS vendor", dmi.BIOSVendor)
	field("BIOS version", dmi.BIOSVersion)
	if ct, ok := chassisTypes[dmi.ChassisType]; ok {
		fieldFmt("Chassis type", "%s (%s)", dmi.ChassisType, ct)
	} else if dmi.ChassisType != "" {
		field("Chassis type", dmi.ChassisType)
	}

	subsection("EFI / Secure Boot:")
	subfield("EFI runtime vars", present(fileExists("/sys/firmware/efi/efivars")))
	subfield("EFI available", present(fileExists("/sys/firmware/efi")))
	if sbVal := readEFIVar("SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"); sbVal != "" {
		subfield("Secure Boot", sbVal)
	} else {
		subfield("Secure Boot", gray("Unknown (no EFI vars access)"))
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: ACPI Tables
	// ════════════════════════════════════════════════════════════════════════
	section("ACPI TABLES")
	tables := listACPITables()
	if len(tables) == 0 {
		field("Status", gray("No tables found under /sys/firmware/acpi/tables"))
	} else {
		// Tables of special interest
		notable := map[string]string{
			"CCEL": green("CCEL") + gray(" ← Confidential Computing Event Log (CVM)"),
			"WSMT": yellow("WSMT") + gray(" ← Windows SMM Security Mitigations Table"),
			"SLIT": "SLIT" + gray(" ← System Locality Info (NUMA distances)"),
			"SRAT": "SRAT" + gray(" ← System Resource Affinity (NUMA topology)"),
			"MADT": "MADT" + gray(" ← Multiple APIC Description Table"),
			"DMAR": "DMAR" + gray(" ← DMA Remapping (Intel VT-d / IOMMU)"),
			"IVRS": "IVRS" + gray(" ← I/O Virtualisation (AMD-Vi / IOMMU)"),
			"BERT": "BERT" + gray(" ← Boot Error Record Table"),
			"HEST": "HEST" + gray(" ← Hardware Error Source Table"),
			"TPM2": green("TPM2") + gray(" ← TPM 2.0 present"),
			"PCCT": "PCCT" + gray(" ← Platform Communications Channel"),
		}
		fieldFmt("Count", "%d tables", len(tables))
		// Print in rows of 7
		for i := 0; i < len(tables); i += 7 {
			end := i + 7
			if end > len(tables) {
				end = len(tables)
			}
			fmt.Printf("    %s\n", strings.Join(tables[i:end], "  "))
		}
		subsection("Notable tables:")
		found := false
		for _, t := range tables {
			if desc, ok := notable[t]; ok {
				subfield(t, desc)
				found = true
			}
		}
		if !found {
			subfield("(none of the highlighted tables present)", "")
		}
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: Kernel Modules (VM-relevant)
	// ════════════════════════════════════════════════════════════════════════
	section("KERNEL MODULES (VM / SECURITY RELEVANT)")
	modules := []struct{ name, desc string }{
		{"kvm", "KVM hypervisor core"},
		{"kvm_amd", "KVM AMD (VMEXIT handler)"},
		{"kvm_intel", "KVM Intel (VMEXIT handler)"},
		{"vfio", "VFIO (device passthrough framework)"},
		{"vfio_pci", "VFIO PCI passthrough"},
		{"virtio", "Virtio (paravirtual I/O)"},
		{"virtio_pci", "Virtio PCI transport"},
		{"virtio_net", "Virtio network driver"},
		{"virtio_blk", "Virtio block driver"},
		{"vboxguest", "VirtualBox guest additions"},
		{"vmw_vmci", "VMware VMCI"},
		{"xen_netfront", "Xen network frontend"},
		{"xen_blkfront", "Xen block frontend"},
		{"msr", "CPU MSR access (/dev/cpu/N/msr)"},
		{"tpm", "TPM device driver"},
		{"tpm_tis", "TPM TIS (hardware TPM)"},
		{"tpm_crb", "TPM CRB (firmware TPM)"},
		{"tpm_vtpm_proxy", "vTPM proxy"},
		{"sev_guest", "AMD SEV guest driver"},
	}
	for _, m := range modules {
		if moduleLoaded(m.name) {
			field(m.name, green("Loaded")+"  "+gray(m.desc))
		}
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: Kernel Cmdline Highlights
	// ════════════════════════════════════════════════════════════════════════
	section("KERNEL CMDLINE HIGHLIGHTS")
	cmdline := osInfo.Cmdline
	if len(cmdline) > 120 {
		field("Cmdline (truncated)", cmdline[:120]+"…")
	} else {
		field("Cmdline", cmdline)
	}

	highlights := []struct{ sub, desc, severity string }{
		{"mem_encrypt=on", "AMD SME/SEV memory encryption enabled at boot", "good"},
		{"kvm_amd.sev=1", "KVM AMD SEV enabled", "good"},
		{"kvm_amd.sev_es=1", "KVM AMD SEV-ES enabled", "good"},
		{"kvm_amd.sev_snp=1", "KVM AMD SEV-SNP enabled", "good"},
		{"tdx=on", "Intel TDX enabled", "good"},
		{"iommu=pt", "IOMMU passthrough (better performance, less isolation)", "warn"},
		{"intel_iommu=on", "Intel IOMMU enabled", "good"},
		{"amd_iommu=on", "AMD IOMMU enabled", "good"},
		{"mitigations=off", "CPU side-channel mitigations DISABLED", "bad"},
		{"pti=off", "Page Table Isolation disabled (Meltdown mitigation off)", "bad"},
		{"spectre_v2=off", "Spectre v2 mitigation disabled", "bad"},
		{"nosmep", "SMEP disabled", "bad"},
		{"nosmap", "SMAP disabled", "bad"},
		{"nopti", "PTI disabled", "bad"},
		{"hugepages=", "Static huge pages configured", "info"},
		{"transparent_hugepage=never", "THP disabled", "info"},
		{"console=", "Kernel console configured", "info"},
		{"quiet", "Quiet boot (reduced kernel output)", "info"},
		{"selinux=0", "SELinux disabled", "warn"},
		{"apparmor=0", "AppArmor disabled", "warn"},
		{"nokaslr", "KASLR disabled (kernel layout randomisation off)", "bad"},
		{"nopku", "PKU (Memory Protection Keys) disabled", "warn"},
	}

	subsection("Interesting parameters found:")
	found := false
	for _, h := range highlights {
		if strings.Contains(cmdline, h.sub) {
			found = true
			val := ""
			switch h.severity {
			case "good":
				val = green("✓") + " " + h.desc
			case "bad":
				val = red("✗") + " " + h.desc
			case "warn":
				val = yellow("⚠") + " " + h.desc
			default:
				val = gray("·") + " " + h.desc
			}
			subfield(h.sub, val)
		}
	}
	if !found {
		subfield("(nothing notable detected)", "")
	}

	// ════════════════════════════════════════════════════════════════════════
	// SECTION: Misc
	// ════════════════════════════════════════════════════════════════════════
	section("MISCELLANEOUS")
	field("NUMA nodes", fmt.Sprintf("%d", countNUMANodes()))
	field("CPUs online", readFile("/sys/devices/system/cpu/online"))
	field("CPUs possible", readFile("/sys/devices/system/cpu/possible"))

	// Kernel lockdown
	if ld := readFile("/sys/kernel/security/lockdown"); ld != "" {
		field("Kernel lockdown", ld)
	}

	// LSMs
	if lsms := readFile("/sys/kernel/security/lsm"); lsms != "" {
		field("Active LSMs", lsms)
	}

	// /sys/hypervisor/
	if hvProps, err := os.ReadDir("/sys/hypervisor"); err == nil {
		names := make([]string, 0, len(hvProps))
		for _, e := range hvProps {
			names = append(names, e.Name())
		}
		field("/sys/hypervisor contents", strings.Join(names, ", "))
		if data, err := os.ReadFile("/sys/hypervisor/uuid"); err == nil {
			field("Hypervisor UUID", strings.TrimSpace(string(data)))
		}
	}

	// vsock
	field("AF_VSOCK available",
		present(fileExists("/dev/vsock") || moduleLoaded("vsock")))

	// virtio-vsock
	subfield("vhost_vsock module", present(moduleLoaded("vhost_vsock")))
	subfield("vmw_vsock_virtio_transport", present(moduleLoaded("vmw_vsock_virtio_transport")))

	// CPUID max leaves summary
	subsection("CPUID leaf summary:")
	eaxMax, _, _, _ := cpuid(0, 0)
	eaxExtMax, _, _, _ := cpuid(0x80000000, 0)
	subfieldFmt("Max basic leaf", "0x%08X", eaxMax)
	subfieldFmt("Max extended leaf", "0x%08X", eaxExtMax)
	_, _, _, edxProcInfo := cpuid(0x80000001, 0)
	subfield("Long mode (64-bit)", supportedStr(edxProcInfo>>29&1 == 1))
	subfield("1 GB pages (pdpe1gb)", supportedStr(edxProcInfo>>26&1 == 1))

	// Platform security (SGX etc.)
	eaxSGX, _, _, _ := cpuid(0x12, 0)
	_ = eaxSGX
	_, ebx7, _, edx7 := cpuid(0x07, 0)
	subsection("Extended security features (CPUID leaf 7):")
	subfield("SGX", supportedStr(ebx7>>2&1 == 1))
	subfield("SHA-NI", supportedStr(ebx7>>29&1 == 1))
	subfield("CET IBT (Indirect Branch Tracking)", supportedStr(edx7>>20&1 == 1))
	subfield("CET SS (Shadow Stack)", supportedStr(edx7>>7&1 == 1))

	fmt.Println()
}
