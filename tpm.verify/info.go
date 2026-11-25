package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}

var algNames = map[uint16]string{
	0x01: "RSA", 0x03: "TDES", 0x04: "SHA1", 0x05: "HMAC", 0x06: "AES", 0x07: "MGF1",
	0x08: "KEYEDHASH", 0x0A: "XOR", 0x0B: "SHA256", 0x0C: "SHA384", 0x0D: "SHA512",
	0x10: "NULL", 0x12: "SM3_256", 0x13: "SM4", 0x14: "RSASSA", 0x15: "RSAES",
	0x16: "RSAPSS", 0x17: "OAEP", 0x18: "ECDSA", 0x19: "ECDH", 0x1A: "ECDAA", 0x1B: "SM2",
	0x1C: "ECSCHNORR", 0x1D: "ECMQV", 0x20: "KDF1_SP800_56A", 0x21: "KDF2",
	0x22: "KDF1_SP800_108", 0x23: "ECC", 0x25: "SYMCIPHER", 0x26: "CAMELLIA",
	0x27: "SHA3_256", 0x28: "SHA3_384", 0x29: "SHA3_512", 0x3F: "CMAC", 0x40: "CTR",
	0x41: "OFB", 0x42: "CBC", 0x43: "CFB", 0x44: "ECB",
}

var ccNames = map[uint32]string{
	0x11F: "NV_UndefineSpaceSpecial", 0x120: "EvictControl", 0x121: "HierarchyControl",
	0x122: "NV_UndefineSpace", 0x124: "ChangeEPS", 0x125: "ChangePPS", 0x126: "Clear",
	0x127: "ClearControl", 0x128: "ClockSet", 0x129: "HierarchyChangeAuth",
	0x12A: "NV_DefineSpace", 0x12B: "PCR_Allocate", 0x12C: "PCR_SetAuthPolicy",
	0x12D: "PP_Commands", 0x12E: "SetPrimaryPolicy", 0x12F: "FieldUpgradeStart",
	0x130: "ClockRateAdjust", 0x131: "CreatePrimary", 0x132: "NV_GlobalWriteLock",
	0x133: "GetCommandAuditDigest", 0x134: "NV_Increment", 0x135: "NV_SetBits",
	0x136: "NV_Extend", 0x137: "NV_Write", 0x138: "NV_WriteLock",
	0x139: "DictionaryAttackLockReset", 0x13A: "DictionaryAttackParameters",
	0x13B: "NV_ChangeAuth", 0x13C: "PCR_Event", 0x13D: "PCR_Reset", 0x13E: "SequenceComplete",
	0x13F: "SetAlgorithmSet", 0x140: "SetCommandCodeAuditStatus", 0x141: "FieldUpgradeData",
	0x142: "IncrementalSelfTest", 0x143: "SelfTest", 0x144: "Startup", 0x145: "Shutdown",
	0x146: "StirRandom", 0x147: "ActivateCredential", 0x148: "Certify", 0x149: "PolicyNV",
	0x14A: "CertifyCreation", 0x14B: "Duplicate", 0x14C: "GetTime",
	0x14D: "GetSessionAuditDigest", 0x14E: "NV_Read", 0x14F: "NV_ReadLock",
	0x150: "ObjectChangeAuth", 0x151: "PolicySecret", 0x152: "Rewrap", 0x153: "Create",
	0x154: "ECDH_ZGen", 0x155: "HMAC/MAC", 0x156: "Import", 0x157: "Load", 0x158: "Quote",
	0x159: "RSA_Decrypt", 0x15B: "HMAC_Start/MAC_Start", 0x15C: "SequenceUpdate",
	0x15D: "Sign", 0x15E: "Unseal", 0x160: "PolicySigned", 0x161: "ContextLoad",
	0x162: "ContextSave", 0x163: "ECDH_KeyGen", 0x164: "EncryptDecrypt", 0x165: "FlushContext",
	0x167: "LoadExternal", 0x168: "MakeCredential", 0x169: "NV_ReadPublic",
	0x16A: "PolicyAuthorize", 0x16B: "PolicyAuthValue", 0x16C: "PolicyCommandCode",
	0x16D: "PolicyCounterTimer", 0x16E: "PolicyCpHash", 0x16F: "PolicyLocality",
	0x170: "PolicyNameHash", 0x171: "PolicyOR", 0x172: "PolicyTicket", 0x173: "ReadPublic",
	0x174: "RSA_Encrypt", 0x176: "StartAuthSession", 0x177: "VerifySignature",
	0x178: "ECC_Parameters", 0x179: "FirmwareRead", 0x17A: "GetCapability", 0x17B: "GetRandom",
	0x17C: "GetTestResult", 0x17D: "Hash", 0x17E: "PCR_Read", 0x17F: "PolicyPCR",
	0x180: "PolicyRestart", 0x181: "ReadClock", 0x182: "PCR_Extend",
	0x183: "PCR_SetAuthValue", 0x184: "NV_Certify", 0x185: "EventSequenceComplete",
	0x186: "HashSequenceStart", 0x187: "PolicyPhysicalPresence", 0x188: "PolicyDuplicationSelect",
	0x189: "PolicyGetDigest", 0x18A: "TestParms", 0x18B: "Commit", 0x18C: "PolicyPassword",
	0x18D: "ZGen_2Phase", 0x18E: "EC_Ephemeral", 0x18F: "PolicyNvWritten",
	0x190: "PolicyTemplate", 0x191: "CreateLoaded", 0x192: "PolicyAuthorizeNV",
	0x193: "EncryptDecrypt2",
}

var propNames = map[uint32]string{
	0x100: "FAMILY_INDICATOR", 0x101: "LEVEL", 0x102: "REVISION", 0x103: "DAY_OF_YEAR",
	0x104: "YEAR", 0x105: "MANUFACTURER", 0x106: "VENDOR_STRING_1", 0x107: "VENDOR_STRING_2",
	0x108: "VENDOR_STRING_3", 0x109: "VENDOR_STRING_4", 0x10A: "VENDOR_TPM_TYPE",
	0x10B: "FIRMWARE_VERSION_1", 0x10C: "FIRMWARE_VERSION_2", 0x10D: "INPUT_BUFFER",
	0x10E: "HR_TRANSIENT_MIN", 0x10F: "HR_PERSISTENT_MIN", 0x110: "HR_LOADED_MIN",
	0x111: "ACTIVE_SESSIONS_MAX", 0x112: "PCR_COUNT", 0x113: "PCR_SELECT_MIN",
	0x114: "CONTEXT_GAP_MAX", 0x116: "NV_COUNTERS_MAX", 0x117: "NV_INDEX_MAX", 0x118: "MEMORY",
	0x119: "CLOCK_UPDATE", 0x11A: "CONTEXT_HASH", 0x11B: "CONTEXT_SYM", 0x11C: "CONTEXT_SYM_SIZE",
	0x11D: "ORDERLY_COUNT", 0x11E: "MAX_COMMAND_SIZE", 0x11F: "MAX_RESPONSE_SIZE",
	0x120: "MAX_DIGEST", 0x121: "MAX_OBJECT_CONTEXT", 0x122: "MAX_SESSION_CONTEXT",
	0x123: "PS_FAMILY_INDICATOR", 0x124: "PS_LEVEL", 0x125: "PS_REVISION",
	0x126: "PS_DAY_OF_YEAR", 0x127: "PS_YEAR", 0x128: "SPLIT_MAX", 0x129: "TOTAL_COMMANDS",
	0x12A: "LIBRARY_COMMANDS", 0x12B: "VENDOR_COMMANDS", 0x12C: "NV_BUFFER_MAX",
	0x12D: "MODES", 0x200: "PERMANENT", 0x201: "STARTUP_CLEAR", 0x202: "HR_NV_INDEX",
	0x203: "HR_LOADED", 0x204: "HR_LOADED_AVAIL", 0x205: "HR_ACTIVE", 0x206: "HR_ACTIVE_AVAIL",
	0x207: "HR_TRANSIENT_AVAIL", 0x208: "HR_PERSISTENT", 0x209: "HR_PERSISTENT_AVAIL",
	0x20A: "NV_COUNTERS", 0x20B: "NV_COUNTERS_AVAIL", 0x20C: "ALGORITHM_SET",
	0x20D: "LOADED_CURVES", 0x20E: "LOCKOUT_COUNTER", 0x20F: "MAX_AUTH_FAIL",
	0x210: "LOCKOUT_INTERVAL", 0x211: "LOCKOUT_RECOVERY", 0x212: "NV_WRITE_RECOVERY",
	0x213: "AUDIT_COUNTER_0", 0x214: "AUDIT_COUNTER_1",
}

func u32str(v uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return strings.TrimSpace(string(bytes.ReplaceAll(b[:], []byte{0}, nil)))
}

func bitList(v uint32, names map[int]string) string {
	var out []string
	for i := 0; i < 32; i++ {
		if n, ok := names[i]; ok {
			out = append(out, fmt.Sprintf("%s=%d", n, (v>>i)&1))
		}
	}
	return strings.Join(out, " ")
}

// Caps is what later tests need to know about the TPM.
type Caps struct {
	Props    map[uint32]uint32
	Algs     map[uint16]bool
	Commands map[uint32]bool
	Banks    []PCRSel
	Handles  map[uint8][]uint32
	Curves   []uint16
}

func (c *Caps) Bank(alg uint16) []int {
	for _, b := range c.Banks {
		if b.Alg == alg {
			return b.PCRs
		}
	}
	return nil
}

// authSet reports whether a hierarchy has a password this tool does not know.
func (c *Caps) authSet(bit int) bool { return c.Props[0x200]&(1<<bit) != 0 }

// needAuth records a SKIP and returns true when tests cannot use a hierarchy.
func (c *Caps) needAuth(r *Report, what string, owner, endorsement bool) bool {
	var why []string
	if owner && c.authSet(0) {
		why = append(why, "owner")
	}
	if endorsement && c.authSet(1) {
		why = append(why, "endorsement")
	}
	if len(why) == 0 {
		return false
	}
	r.Add(what, Skip, "%s hierarchy has a password set; this tool only uses empty hierarchy auth", strings.Join(why, " and "))
	return true
}

func hostInfo(r *Report) {
	r.Section("Host and platform")
	r.Doing("reading OS, CPU, DMI, sysfs and boot security state")
	osName := ""
	for _, l := range strings.Split(readTrim("/etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			osName = strings.Trim(v, `"`)
		}
	}
	cpu, flags := "", ""
	for _, l := range strings.Split(readTrim("/proc/cpuinfo"), "\n") {
		k, v, _ := strings.Cut(l, ":")
		switch strings.TrimSpace(k) {
		case "model name":
			cpu = strings.TrimSpace(v)
		case "flags":
			flags = " " + v + " "
		}
	}
	rows := [][]string{
		{"OS", osName}, {"Kernel", readTrim("/proc/sys/kernel/osrelease")}, {"Arch", runtime.GOARCH},
		{"CPU", cpu}, {"System vendor", readTrim("/sys/class/dmi/id/sys_vendor")},
		{"Product", readTrim("/sys/class/dmi/id/product_name")},
		{"BIOS version", readTrim("/sys/class/dmi/id/bios_version")},
		{"BIOS date", readTrim("/sys/class/dmi/id/bios_date")},
		{"Kernel command line", readTrim("/proc/cmdline")},
		{"Kernel lockdown", readTrim("/sys/kernel/security/lockdown")},
	}
	r.Table("Host", []string{"Property", "Value"}, rows)
	if strings.Contains(flags, " hypervisor ") {
		r.Add("Execution environment", Info, "CPU reports a hypervisor: this is a virtual machine, so any TPM is likely virtual")
	} else {
		r.Add("Execution environment", Info, "no hypervisor CPU flag (consistent with bare metal)")
	}

	if _, err := os.Stat("/sys/firmware/efi"); err != nil {
		r.Add("UEFI boot", Warn, "/sys/firmware/efi not present: legacy boot or hidden")
	} else {
		r.Add("UEFI boot", Pass, "booted via UEFI")
	}
	for _, v := range []string{"SecureBoot", "SetupMode"} {
		b, err := os.ReadFile("/sys/firmware/efi/efivars/" + v + "-8be4df61-93ca-11d2-aa0d-00e098032b8c")
		switch {
		case err != nil || len(b) < 5:
			r.Add(v+" variable", Skip, "cannot read EFI variable")
		case v == "SecureBoot" && b[4] == 1:
			r.Add("SecureBoot variable", Pass, "Secure Boot enabled")
		case v == "SecureBoot":
			r.Add("SecureBoot variable", Warn, "Secure Boot disabled")
		case b[4] == 0:
			r.Add("SetupMode variable", Pass, "not in setup mode")
		default:
			r.Add("SetupMode variable", Warn, "platform is in Setup Mode: keys can be replaced")
		}
	}
}

func deviceInfo(r *Report) {
	r.Section("TPM presence")
	r.Doing("looking for kernel TPM devices")
	nodes, _ := filepath.Glob("/dev/tpm*")
	if len(nodes) == 0 {
		r.Add("Device nodes", Warn, "no /dev/tpm* node (check that the TPM is enabled and the tpm_crb/tpm_tis driver is loaded)")
	}
	var rows [][]string
	for _, n := range nodes {
		if st, err := os.Stat(n); err == nil {
			rows = append(rows, []string{n, st.Mode().String()})
			if st.Mode().Perm()&0o006 != 0 {
				r.Add("Permissions "+n, Warn, "world-accessible: %s", st.Mode())
			}
		}
	}
	if len(rows) > 0 {
		r.Add("Device nodes", Pass, "found %s", strings.Join(nodes, ", "))
		r.Table("Device nodes", []string{"Node", "Mode"}, rows)
	}
	rows = nil
	classes, _ := filepath.Glob("/sys/class/tpm/tpm*")
	for _, c := range classes {
		name := filepath.Base(c)
		driver, _ := os.Readlink(c + "/device/driver")
		for _, kv := range [][2]string{
			{"tpm_version_major", readTrim(c + "/tpm_version_major")},
			{"driver", filepath.Base(driver)},
			{"modalias", readTrim(c + "/device/modalias")},
			{"ACPI HID", readTrim(c + "/device/firmware_node/hid")},
			{"description", readTrim(c + "/device/description")},
		} {
			rows = append(rows, []string{name, kv[0], kv[1]})
		}
	}
	if len(rows) > 0 {
		r.Table("Sysfs", []string{"Device", "Attribute", "Value"}, rows)
	}
	for _, p := range []string{"/sys/firmware/acpi/tables/TPM2", "/sys/firmware/acpi/tables/TCPA"} {
		if _, err := os.Stat(p); err == nil {
			r.Add("ACPI table", Info, "firmware publishes %s", filepath.Base(p))
		} else if !errors.Is(err, fs.ErrNotExist) {
			r.Add("ACPI table", Skip, "%v", err)
		}
	}
}

func capabilities(r *Report, t *TPM) (*Caps, error) {
	r.Section("TPM identity, state and capabilities")
	c := &Caps{Props: map[uint32]uint32{}, Algs: map[uint16]bool{}, Commands: map[uint32]bool{}, Handles: map[uint8][]uint32{}}

	r.Doing("TPM2_GetCapability: fixed and variable properties")
	for _, start := range []uint32{0x100, 0x200} {
		err := t.getCap(capProps, start, 128, func(r *rd) uint32 {
			k, v := r.u32(), r.u32()
			c.Props[k] = v
			return k
		})
		if err != nil {
			r.Add("TPM 2.0 response", Fail, "%v (a TPM 1.2 or a non-started TPM is not supported)", err)
			return nil, err
		}
	}
	p := c.Props
	vendor := u32str(p[0x106]) + u32str(p[0x107]) + u32str(p[0x108]) + u32str(p[0x109])
	mfr := u32str(p[0x105])
	fw := fmt.Sprintf("%d.%d.%d.%d", p[0x10B]>>16, p[0x10B]&0xffff, p[0x10C]>>16, p[0x10C]&0xffff)
	known := map[string]string{"AMD": "AMD (typically firmware TPM)", "IFX": "Infineon", "INTC": "Intel", "NTC": "Nuvoton",
		"STM": "STMicroelectronics", "MSFT": "Microsoft (virtual/firmware)", "IBM": "IBM (software TPM)",
		"GOOG": "Google (virtual)", "ATML": "Atmel", "NSM": "National Semiconductor"}
	r.Add("TPM 2.0 response", Pass, "family %s, revision %.2f", u32str(p[0x100]), float64(p[0x102])/100)
	r.Add("Manufacturer", Info, "%q %s, vendor string %q, firmware %s", mfr, known[mfr], vendor, fw)
	if mfr == "IBM" || mfr == "MSFT" || mfr == "GOOG" {
		r.Add("TPM type", Warn, "manufacturer %q indicates a software or virtual TPM, not a hardware root of trust", mfr)
	}
	r.Table("Identity", []string{"Property", "Value"}, [][]string{
		{"Manufacturer", mfr}, {"Vendor string", vendor}, {"Firmware version", fw},
		{"Spec family", u32str(p[0x100])}, {"Spec revision", fmt.Sprintf("%.2f", float64(p[0x102])/100)},
		{"Spec date", fmt.Sprintf("day %d of %d", p[0x103], p[0x104])},
	})

	var keys []int
	for k := range p {
		keys = append(keys, int(k))
	}
	sort.Ints(keys)
	var rows [][]string
	for _, k := range keys {
		rows = append(rows, []string{fmt.Sprintf("0x%03X", k), propNames[uint32(k)], fmt.Sprintf("%d", p[uint32(k)]), fmt.Sprintf("0x%08X", p[uint32(k)])})
	}
	r.Table("All TPM properties", []string{"ID", "Name", "Decimal", "Hex"}, rows)

	if perm, ok := p[0x200]; ok {
		r.Add("Permanent flags", Info, "%s", bitList(perm, map[int]string{0: "ownerAuthSet", 1: "endorsementAuthSet", 2: "lockoutAuthSet", 8: "disableClear", 9: "inLockout", 10: "tpmGeneratedEPS"}))
		if perm&(1<<9) != 0 {
			r.Add("Dictionary attack lockout", Fail, "TPM is in lockout")
		} else {
			r.Add("Dictionary attack lockout", Pass, "not in lockout (failures %d of %d, interval %ds, recovery %ds)", p[0x20E], p[0x20F], p[0x210], p[0x211])
		}
		if perm&(1<<2) == 0 {
			r.Add("Lockout hierarchy auth", Warn, "no lockout auth set: any root user could clear the TPM or reset lockout")
		}
	}
	if sc, ok := p[0x201]; ok {
		r.Add("Startup flags", Info, "%s", bitList(sc, map[int]string{0: "phEnable", 1: "shEnable", 2: "ehEnable", 3: "phEnableNV", 31: "orderly"}))
		if sc&1 != 0 {
			r.Add("Platform hierarchy", Warn, "still enabled after boot (firmware normally disables or locks it)")
		}
		if sc&2 == 0 || sc&4 == 0 {
			r.Add("Hierarchies", Warn, "owner or endorsement hierarchy disabled; some tests will fail")
		}
	}

	r.Doing("enumerating algorithms, commands, curves, PCR banks and handles")
	rows = nil
	err := t.getCap(capAlgs, 0, 128, func(r *rd) uint32 {
		a, at := r.u16(), r.u32()
		c.Algs[a] = true
		var f []string
		for _, b := range []struct {
			bit  int
			name string
		}{{0, "asymmetric"}, {1, "symmetric"}, {2, "hash"}, {3, "object"}, {8, "signing"}, {9, "encrypting"}, {10, "method"}} {
			if at&(1<<b.bit) != 0 {
				f = append(f, b.name)
			}
		}
		rows = append(rows, []string{fmt.Sprintf("0x%04X", a), algNames[a], strings.Join(f, ", ")})
		return uint32(a)
	})
	r.Check("Algorithm list", err, fmt.Sprintf("%d algorithms", len(rows)))
	r.Table("Algorithms", []string{"ID", "Name", "Attributes"}, rows)

	rows = nil
	err = t.getCap(capCommands, 0x11F, 256, func(r *rd) uint32 {
		v := r.u32()
		cc := v & 0xFFFF
		if v&(1<<29) != 0 {
			cc |= 1 << 29
		}
		c.Commands[cc] = true
		rows = append(rows, []string{fmt.Sprintf("0x%X", cc), ccNames[cc]})
		return cc
	})
	r.Check("Command list", err, fmt.Sprintf("%d commands", len(rows)))
	r.Table("Commands", []string{"Code", "Name"}, rows)

	rows = nil
	if c.Algs[algECC] {
		err = t.getCap(capCurves, 0, 64, func(r *rd) uint32 {
			v := r.u16()
			c.Curves = append(c.Curves, v)
			n := map[uint16]string{1: "NIST P192", 2: "NIST P224", 3: "NIST P256", 4: "NIST P384", 5: "NIST P521", 0x10: "BN P256", 0x11: "BN P638", 0x20: "SM2 P256"}[v]
			rows = append(rows, []string{fmt.Sprintf("0x%04X", v), n})
			return uint32(v)
		})
		r.Check("ECC curve list", err, fmt.Sprintf("%d curves", len(rows)))
		r.Table("ECC curves", []string{"ID", "Name"}, rows)
	}

	// The PCR capability is not paged: property and count are fixed.
	var q buf
	q.u32(capPCRs)
	q.u32(0)
	q.u32(1)
	_, resp, err := t.run(ccGetCapability, nil, nil, q, 0)
	if err == nil {
		resp.u8()
		resp.u32()
		for _, s := range resp.pcrSelection() {
			if len(s.PCRs) > 0 {
				c.Banks = append(c.Banks, s)
			}
		}
		err = resp.err
	}
	var banks []string
	for _, b := range c.Banks {
		_, n, _ := hashFor(b.Alg)
		banks = append(banks, fmt.Sprintf("%s (%d PCRs)", n, len(b.PCRs)))
	}
	if err == nil && len(c.Banks) == 0 {
		err = errors.New("no active PCR bank")
	}
	r.Check("Active PCR banks", err, strings.Join(banks, ", "))
	if len(c.Bank(algSHA256)) == 0 {
		r.Add("SHA-256 PCR bank", Warn, "not active; SHA-256 PCR tests will be skipped")
	}

	rows = nil
	for _, h := range []struct {
		typ  uint8
		name string
	}{{0x01, "NV index"}, {0x02, "loaded session"}, {0x03, "saved session"}, {0x80, "transient object"}, {0x81, "persistent object"}} {
		err = t.getCap(capHandles, uint32(h.typ)<<24, 128, func(r *rd) uint32 {
			v := r.u32()
			// 0x02 lists loaded sessions and 0x03 saved sessions.
			c.Handles[h.typ] = append(c.Handles[h.typ], v)
			rows = append(rows, []string{h.name, fmt.Sprintf("0x%08X", v)})
			return v
		})
		if err != nil {
			r.Add("Handles: "+h.name, Warn, "%v", err)
		}
	}
	r.Table("Handles in use", []string{"Type", "Handle"}, rows)
	return c, nil
}
