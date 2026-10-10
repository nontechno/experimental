package main

// confidential.go adds confidential-computing evidence on top of the basic
// flags collected in env.go: the AMD SEV-SNP guest attestation report (via
// /dev/sev-guest), TPM PCRs and event-log size, IMA, kernel lockdown, kernel
// config support flags, relevant boot-log lines, SGX / Nitro Enclaves
// detection, and confidential-computing fields found in the OCI IMDS JSON.
//
// Everything is read-only. Secrets are never read: only the *existence* of
// the EFI secret area is recorded, and the attestation report is public,
// signed evidence meant to be handed to a verifier.

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var surfacePaths = []string{
	"/dev/sgx_enclave", "/dev/sgx_provision", "/dev/nitro_enclaves", "/dev/nsm", "/dev/vsock",
	"/sys/class/misc/sev-guest", "/sys/bus/platform/devices/sev-guest",
	"/sys/bus/platform/drivers/sev-guest", "/sys/bus/pci/drivers/ccp", "/sys/bus/platform/drivers/ccp",
	"/sys/kernel/config/tsm/report",     // configfs-tsm: kernel attestation report interface
	"/sys/kernel/security/secrets/coco", // EFI confidential-computing secret area (existence only)
	"/sys/kernel/security/tpm0/binary_bios_measurements",
	"/sys/kernel/security/ima",
	"/sys/module/kvm_intel/parameters/tdx", "/sys/firmware/tdx",
}

var kconfigWanted = []string{
	"CONFIG_AMD_MEM_ENCRYPT", "CONFIG_AMD_MEM_ENCRYPT_ACTIVE_BY_DEFAULT", "CONFIG_X86_MEM_ENCRYPT",
	"CONFIG_ARCH_HAS_CC_PLATFORM", "CONFIG_KVM_AMD_SEV", "CONFIG_SEV_GUEST", "CONFIG_CRYPTO_DEV_CCP",
	"CONFIG_CRYPTO_DEV_SP_PSP", "CONFIG_TSM_REPORTS", "CONFIG_EFI_SECRET",
	"CONFIG_INTEL_TDX_GUEST", "CONFIG_TDX_GUEST_DRIVER", "CONFIG_INTEL_TDX_HOST",
	"CONFIG_X86_SGX", "CONFIG_X86_SGX_KVM", "CONFIG_ARM_CCA_GUEST",
	"CONFIG_IMA", "CONFIG_TCG_TPM", "CONFIG_TCG_TIS", "CONFIG_TCG_CRB",
	"CONFIG_SECURITY_LOCKDOWN_LSM", "CONFIG_EFI_STUB", "CONFIG_IKCONFIG_PROC",
}

var cmdlinePrefixes = []string{
	"mem_encrypt=", "kvm_amd.", "kvm_intel.", "ima_", "lockdown=", "swiotlb=",
	"tdx", "efi=", "amd_iommu=", "intel_iommu=", "iommu=", "nokaslr", "mitigations=",
}

var ccLogRe = regexp.MustCompile(`(?i)\b(sev(-es|-snp)?|snp|tdx|sme|tsme|sgx|rmp|svsm|vtom|cc_platform|memory encryption|secure ?boot|tpm\d*|ima|lockdown|confidential)\b`)

func (c *collector) confidentialExtra(ctx context.Context, o envOptions) {
	cc := c.env.Confidential
	if cc == nil {
		cc = map[string]any{}
		c.env.Confidential = cc
	}
	techs, _ := cc["technologies"].([]string)
	if techs == nil {
		techs = []string{}
	}

	for _, f := range []string{"sgx", "sgx_lc", "tme", "rmpquery", "pconfig"} {
		cc["cpuFlag."+f] = c.flags[f]
	}

	surf := map[string]bool{}
	for _, p := range surfacePaths {
		surf[p] = c.exists(p)
	}
	cc["surfaces"] = surf
	if c.env.Devices != nil {
		for _, p := range []string{"/dev/sgx_enclave", "/dev/nitro_enclaves", "/dev/nsm"} {
			c.env.Devices[p] = surf[p]
		}
	}

	if c.flags["sgx"] || surf["/dev/sgx_enclave"] || c.exists("/dev/sgx/enclave") {
		techs = append(techs, "intel-sgx")
	}
	if c.flags["tme"] {
		techs = append(techs, "intel-tme (cpu flag)")
	}
	if surf["/dev/nitro_enclaves"] {
		techs = append(techs, "aws-nitro-enclaves-capable (/dev/nitro_enclaves)")
	}
	if surf["/dev/nsm"] {
		techs = append(techs, "aws-nitro-enclave (/dev/nsm present)")
	}
	for _, p := range []string{"tdx", "sgx"} {
		if s, ok := c.read("/sys/module/kvm_intel/parameters/" + p); ok {
			cc["kvm_intel."+p] = s
			if p == "tdx" && (s == "Y" || s == "1") {
				techs = append(techs, "intel-tdx-host (kvm_intel tdx enabled)")
			}
		}
	}
	cc["technologies"] = techs

	// Boot parameters that influence memory encryption and integrity.
	var rel []string
	for _, tok := range strings.Fields(c.env.Kernel["cmdline"]) {
		if hasAnyPrefix(tok, cmdlinePrefixes) {
			rel = append(rel, tok)
		}
	}
	if len(rel) > 0 {
		cc["cmdlineRelevant"] = rel
	}

	if s, ok := c.read("/sys/kernel/security/lockdown"); ok {
		cc["kernelLockdown"] = s // e.g. "none [integrity] confidentiality"
	}

	if tpm := c.tpmInfo(); tpm != nil {
		cc["tpm"] = tpm
	}
	if fi, err := os.Stat(c.p("/sys/kernel/security/tpm0/binary_bios_measurements")); err == nil {
		cc["tpmEventLogBytes"] = fi.Size()
	}
	if s, ok := c.read("/sys/kernel/security/ima/runtime_measurements_count"); ok {
		cc["imaMeasurementCount"] = s
	}
	if s, ok := c.read("/sys/kernel/security/ima/violations"); ok {
		cc["imaViolations"] = s
	}

	if kc := c.kernelConfig(); len(kc) > 0 {
		cc["kernelConfig"] = kc
	}
	if lines := c.ccKernelLog(); len(lines) > 0 {
		cc["bootLogLines"] = lines
	}

	if o.Attest && c.root == "/" {
		dev := o.AttestDevice
		if dev == "" {
			dev = "/dev/sev-guest"
		}
		if c.exists(dev) {
			c.snpAttest(ctx, o, dev)
		}
	}
}

func (c *collector) tpmInfo() map[string]any {
	devs, _ := filepath.Glob(c.p("/sys/class/tpm/tpm[0-9]*"))
	if len(devs) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, d := range devs {
		r := c.rel(d)
		t := map[string]any{}
		if v, ok := c.read(r + "/tpm_version_major"); ok {
			t["versionMajor"] = v
		}
		for _, alg := range []string{"sha1", "sha256", "sha384", "sha512", "sm3"} {
			files, _ := filepath.Glob(c.p(r + "/pcr-" + alg + "/*"))
			if len(files) == 0 {
				continue
			}
			pcrs := map[string]string{}
			for _, f := range files {
				if v, ok := c.read(c.rel(f)); ok {
					pcrs[filepath.Base(f)] = strings.ToLower(v)
				}
			}
			t["pcr-"+alg] = pcrs
		}
		out[filepath.Base(d)] = t
	}
	return out
}

func (c *collector) kernelConfig() map[string]string {
	var text string
	if f, err := os.Open(c.p("/proc/config.gz")); err == nil {
		if zr, err := gzip.NewReader(f); err == nil {
			b, _ := io.ReadAll(io.LimitReader(zr, 8<<20))
			text = string(b)
		}
		f.Close()
	}
	if text == "" {
		if rel := c.env.Kernel["release"]; rel != "" {
			if s, ok := c.read("/boot/config-" + rel); ok {
				text = s
			}
		}
	}
	if text == "" {
		return nil
	}
	want := map[string]bool{}
	for _, k := range kconfigWanted {
		want[k] = true
	}
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "CONFIG_"):
			if k, v, ok := strings.Cut(line, "="); ok && want[k] {
				out[k] = v
			}
		case strings.HasPrefix(line, "# CONFIG_") && strings.HasSuffix(line, " is not set"):
			k := strings.TrimSuffix(strings.TrimPrefix(line, "# "), " is not set")
			if want[k] {
				out[k] = "n"
			}
		}
	}
	return out
}

// ccKernelLog returns boot-log lines about memory encryption, TPM, IMA,
// lockdown, etc. It combines /var/log/dmesg (boot-time snapshot) with the live
// kernel ring buffer, which may need CAP_SYSLOG.
func (c *collector) ccKernelLog() []string {
	var texts []string
	if s, ok := c.read("/var/log/dmesg"); ok {
		texts = append(texts, s)
	}
	if c.root == "/" {
		if s := kernelLog(); s != "" {
			texts = append(texts, s)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, line := range strings.Split(t, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !ccLogRe.MatchString(line) || seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, truncate(line, 300))
			if len(out) >= 60 {
				return out
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- SNP attestation

func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func le64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }

func tcbFields(v uint64) map[string]any {
	return map[string]any{
		"raw":        fmt.Sprintf("0x%016x", v),
		"bootLoader": uint8(v),
		"tee":        uint8(v >> 8),
		"snp":        uint8(v >> 48),
		"microcode":  uint8(v >> 56),
	}
}

func signingKeyName(v uint32) string {
	switch v {
	case 0:
		return "VCEK"
	case 1:
		return "VLEK"
	case 7:
		return "none"
	}
	return fmt.Sprintf("unknown(%d)", v)
}

func sigAlgoName(v uint32) string {
	switch v {
	case 0:
		return "invalid"
	case 1:
		return "ecdsa-p384-sha384"
	}
	return fmt.Sprintf("unknown(%d)", v)
}

const snpReportLen = 0x4A0 // ATTESTATION_REPORT size (1184 bytes)

// parseSNPReport decodes the AMD SEV-SNP ATTESTATION_REPORT structure
// (SEV-SNP firmware ABI spec, "ATTESTATION_REPORT" table). The signature is
// not verified here; the raw report is included so a verifier can do that.
func parseSNPReport(rep []byte) (map[string]any, error) {
	if len(rep) < 0x2A0 {
		return nil, fmt.Errorf("attestation report too short: %d bytes", len(rep))
	}
	h := func(a, b int) string { return hex.EncodeToString(rep[a:b]) }
	ver := le32(rep, 0x00)
	pol := le64(rep, 0x08)
	pinfo := le64(rep, 0x40)
	flags := le32(rep, 0x48)

	r := map[string]any{
		"reportVersion": ver,
		"guestSvn":      le32(rep, 0x04),
		"policy": map[string]any{
			"raw":              fmt.Sprintf("0x%016x", pol),
			"abiMinor":         uint8(pol),
			"abiMajor":         uint8(pol >> 8),
			"smtAllowed":       pol&(1<<16) != 0,
			"migrateMA":        pol&(1<<18) != 0,
			"debug":            pol&(1<<19) != 0, // true => guest is debuggable, NOT confidential
			"singleSocket":     pol&(1<<20) != 0,
			"cxlAllowed":       pol&(1<<21) != 0,
			"memAes256Xts":     pol&(1<<22) != 0,
			"raplDisabled":     pol&(1<<23) != 0,
			"ciphertextHiding": pol&(1<<24) != 0,
			"pageSwapDisabled": pol&(1<<25) != 0,
		},
		"familyId":      h(0x10, 0x20),
		"imageId":       h(0x20, 0x30),
		"vmpl":          le32(rep, 0x30),
		"signatureAlgo": sigAlgoName(le32(rep, 0x34)),
		"currentTcb":    tcbFields(le64(rep, 0x38)),
		"platformInfo": map[string]any{
			"raw":         fmt.Sprintf("0x%016x", pinfo),
			"smtEnabled":  pinfo&1 != 0,
			"tsmeEnabled": pinfo&2 != 0,
		},
		"flags": map[string]any{
			"authorKeyEn": flags&1 != 0,
			"maskChipKey": flags&2 != 0,
			"signingKey":  signingKeyName((flags >> 2) & 7),
		},
		"reportData":      h(0x50, 0x90),
		"measurement":     h(0x90, 0xC0), // launch digest
		"hostData":        h(0xC0, 0xE0),
		"idKeyDigest":     h(0xE0, 0x110),
		"authorKeyDigest": h(0x110, 0x140),
		"reportId":        h(0x140, 0x160),
		"reportIdMa":      h(0x160, 0x180),
		"reportedTcb":     tcbFields(le64(rep, 0x180)),
		"chipId":          h(0x1A0, 0x1E0), // hardware-unique; zeroed when maskChipKey is set
	}
	if ver >= 3 {
		r["cpuidFamilyId"] = rep[0x188]
		r["cpuidModelId"] = rep[0x189]
		r["cpuidStepping"] = rep[0x18A]
	}
	if ver >= 2 && len(rep) >= 0x1F8 {
		r["committedTcb"] = tcbFields(le64(rep, 0x1E0))
		r["currentFirmware"] = fmt.Sprintf("%d.%d.%d", rep[0x1EA], rep[0x1E9], rep[0x1E8])
		r["committedFirmware"] = fmt.Sprintf("%d.%d.%d", rep[0x1EE], rep[0x1ED], rep[0x1EC])
		r["launchTcb"] = tcbFields(le64(rep, 0x1F0))
	}
	end := len(rep)
	if end > snpReportLen {
		end = snpReportLen
	}
	r["rawReportBase64"] = base64.StdEncoding.EncodeToString(rep[:end])
	return r, nil
}

func (c *collector) snpAttest(ctx context.Context, o envOptions, dev string) {
	cc := c.env.Confidential
	nonce := make([]byte, 64)
	copy(nonce, o.AttestNonce)

	type result struct {
		resp []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := snpGetReport(dev, nonce, 0)
		ch <- result{resp, err}
	}()

	var res result
	select {
	case res = <-ch:
	case <-time.After(3 * time.Second): // an ioctl cannot be cancelled; stop waiting for it
		cc["snpAttestationError"] = "timed out after 3s waiting for " + dev
		return
	case <-ctx.Done():
		cc["snpAttestationError"] = ctx.Err().Error()
		return
	}
	if res.err != nil {
		cc["snpAttestationError"] = res.err.Error()
		return
	}
	resp := res.resp
	if len(resp) < 32 {
		cc["snpAttestationError"] = "short response from " + dev
		return
	}
	status, size := le32(resp, 0), le32(resp, 4)
	if status != 0 {
		cc["snpAttestationError"] = fmt.Sprintf("firmware returned status 0x%x", status)
		return
	}
	if size == 0 || int(size) > len(resp)-32 {
		cc["snpAttestationError"] = fmt.Sprintf("implausible report size %d", size)
		return
	}
	rep, err := parseSNPReport(resp[32 : 32+int(size)])
	if err != nil {
		cc["snpAttestationError"] = err.Error()
		return
	}
	rep["device"] = dev
	rep["requestedReportData"] = hex.EncodeToString(nonce)
	rep["reportDataMatchesRequest"] = rep["reportData"] == hex.EncodeToString(nonce)
	cc["snpAttestation"] = rep
}

// ---------------------------------------------------------------- IMDS scan

var ccKeyNeedles = []string{
	"confidential", "secureboot", "secure_boot", "trustedplatform", "trusted_platform",
	"measuredboot", "measured_boot", "memoryencryption", "memory_encryption", "tpm",
	"sev", "tdx", "enclave", "attestation", "platformconfig", "platform_config",
}

// scanCCKeys walks the OCI IMDS /instance/ JSON and returns every field whose
// key looks confidential-computing related (e.g. platform config flags).
// Values under a matching key are returned whole; user_data is never matched.
func scanCCKeys(raw json.RawMessage) map[string]any {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	out := map[string]any{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, vv := range t {
				p := k
				if path != "" {
					p = path + "." + k
				}
				lk := strings.ToLower(k)
				hit := false
				for _, n := range ccKeyNeedles {
					if strings.Contains(lk, n) {
						hit = true
						break
					}
				}
				if hit {
					if s, ok := vv.(string); ok {
						vv = truncate(s, 500)
					}
					out[p] = vv
				} else {
					walk(p, vv)
				}
			}
		case []any:
			for i, vv := range t {
				walk(fmt.Sprintf("%s[%d]", path, i), vv)
			}
		}
	}
	walk("", v)
	return out
}
