//go:build amd64

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ── /proc/cpuinfo ─────────────────────────────────────────────────────────────

// readCPUInfoFlags parses the first CPU entry's flags from /proc/cpuinfo.
// Returns a set of lowercase flag names.
func readCPUInfoFlags() map[string]bool {
	flags := make(map[string]bool)

	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		indent(warn("Cannot open /proc/cpuinfo: " + err.Error()))
		return flags
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// "flags" on x86; "Features" on ARM — only x86 matters here.
		if !strings.HasPrefix(line, "flags") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		for _, tok := range strings.Fields(parts[1]) {
			flags[strings.ToLower(tok)] = true
		}
		break // first CPU entry is sufficient
	}
	return flags
}

// ── sysfs / device markers ───────────────────────────────────────────────────

type pathCheck struct {
	path  string
	desc  string
	vital bool // if true, absence is a definitive failure (not just a warning)
}

var kernelPaths = []pathCheck{
	{"/dev/sev-guest", "SEV-SNP guest char device (kernel driver)", true},
	{"/sys/bus/platform/drivers/sev-guest", "sev-guest platform driver binding", false},
	{"/sys/kernel/security/tpm0", "TPM (used by KDS cert retrieval tools)", false},
	{"/sys/firmware/efi", "UEFI firmware (expected in SNP guests)", false},
	{"/dev/tpm0", "TPM character device", false},
	{"/sys/devices/system/cpu/cpu0/microcode", "Microcode update path", false},
}

// checkKernelMarkers scans the sysfs/dev paths and prints results.
func checkKernelMarkers() {
	for _, p := range kernelPaths {
		_, err := os.Stat(p.path)
		if err == nil {
			indent(pass(fmt.Sprintf("%-48s  %s", p.path, p.desc)))
		} else if p.vital {
			indent(fail(fmt.Sprintf("%-48s  %s", p.path, p.desc)))
		} else {
			indent(warn(fmt.Sprintf("%-48s  %s  (not present)", p.path, p.desc)))
		}
	}

	fmt.Println()
	checkCPUModelInfo()
}

// checkCPUModelInfo prints a few CPU fields from /proc/cpuinfo that are
// useful for cross-checking AMD EPYC generation (SNP requires Zen 3+).
func checkCPUModelInfo() {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return
	}
	defer f.Close()

	want := map[string]bool{
		"model name": true,
		"cpu family": true,
		"model":      true,
		"stepping":   true,
	}
	found := map[string]string{}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if len(found) == len(want) {
			break
		}
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if want[key] {
			found[key] = strings.TrimSpace(parts[1])
		}
	}

	if name, ok := found["model name"]; ok {
		field("CPU model", name)
	}
	if family, ok := found["cpu family"]; ok {
		note := familyNote(family)
		field("CPU family", family+note)
	}
	if model, ok := found["model"]; ok {
		field("CPU model number", model)
	}
	if stepping, ok := found["stepping"]; ok {
		field("Stepping", stepping)
	}
}

func familyNote(family string) string {
	switch family {
	case "23":
		return "  " + dim("(Zen 1/2 — SEV/SEV-ES only, no SNP)")
	case "25":
		return "  " + label("(Zen 3/4 — EPYC Milan/Genoa; SEV-SNP supported ✓)")
	case "26":
		return "  " + label("(Zen 5 — EPYC Turin; SEV-SNP supported ✓)")
	default:
		return "  " + dim("(check AMD spec for SNP support)")
	}
}
