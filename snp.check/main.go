// snp-check: confirms whether the current process is running inside an
// AMD SEV-SNP Confidential VM by performing layered checks from weakest
// (CPUID hints) to strongest (cryptographic attestation report from the
// AMD Secure Processor via /dev/sev-guest ioctl).
//
// Build: go build -o snp-check .
// Run:   sudo ./snp-check          (root needed for /dev/sev-guest)
//
// Requires: AMD CPU, Linux kernel ≥ 5.19, CONFIG_SEV_GUEST=y.

//go:build amd64

package main

import (
	"fmt"
	"os"
)

// ── ANSI terminal helpers ────────────────────────────────────────────────────

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiGray   = "\033[90m"
	ansiBold   = "\033[1m"
)

func pass(s string) string  { return ansiGreen + "✓ " + s + ansiReset }
func fail(s string) string  { return ansiRed + "✗ " + s + ansiReset }
func warn(s string) string  { return ansiYellow + "⚠ " + s + ansiReset }
func label(s string) string { return ansiCyan + s + ansiReset }
func dim(s string) string   { return ansiGray + s + ansiReset }

func section(n, title string) {
	fmt.Printf("\n%s[%s]%s %s%s%s\n",
		ansiBold, n, ansiReset,
		ansiBold+ansiCyan, title, ansiReset)
}

func field(k, v string) {
	fmt.Printf("    %-26s %s\n", dim(k+":"), v)
}

func indent(s string) {
	fmt.Printf("    %s\n", s)
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	fmt.Printf("\n%s%s╔══════════════════════════════════════════════════╗%s\n",
		ansiBold, ansiCyan, ansiReset)
	fmt.Printf("%s%s║     AMD SEV-SNP Confidential VM Checker          ║%s\n",
		ansiBold, ansiCyan, ansiReset)
	fmt.Printf("%s%s╚══════════════════════════════════════════════════╝%s\n",
		ansiBold, ansiCyan, ansiReset)

	// evidence tracks how many definitive checks passed (max 3).
	// CPUID hints don't count — only kernel driver + firmware report do.
	evidence := 0

	// ── Layer 1: CPU Vendor ──────────────────────────────────────────────────
	section("1", "CPU Vendor  (CPUID leaf 0x00000000)")
	vendor := getCPUVendor()
	field("Vendor string", vendor)
	if vendor == "AuthenticAMD" {
		indent(pass("Genuine AMD CPU"))
	} else {
		indent(fail("Not an AMD CPU — SEV-SNP is AMD-only"))
		fmt.Println("\nAborting: this tool only runs on AMD hardware.")
		os.Exit(1)
	}

	// ── Layer 2: CPUID 0x8000001F ────────────────────────────────────────────
	section("2", "AMD Memory Encryption Features  (CPUID leaf 0x8000001F)")
	feats := getSEVFeatures()
	printSEVFeatures(feats)
	fmt.Println()
	indent(dim("Note: a hypervisor may hide these bits even when SEV-SNP is active."))
	indent(dim("The ioctl in layer 6 is the definitive check."))

	// ── Layer 3: /proc/cpuinfo flags ─────────────────────────────────────────
	section("3", "/proc/cpuinfo CPU Flags")
	cpuFlags := readCPUInfoFlags()
	for _, f := range []string{"sev", "sev_es", "sev_snp"} {
		if cpuFlags[f] {
			indent(pass(f))
		} else {
			indent(warn(f + "  (not present in /proc/cpuinfo)"))
		}
	}

	// ── Layer 4: sysfs / device markers ─────────────────────────────────────
	section("4", "Kernel & sysfs Markers")
	checkKernelMarkers()

	// ── Layer 5: /dev/sev-guest ──────────────────────────────────────────────
	section("5", "/dev/sev-guest  (SEV guest kernel driver)")
	if _, err := os.Stat("/dev/sev-guest"); err == nil {
		indent(pass("/dev/sev-guest present — kernel SEV-SNP guest driver is loaded"))
		evidence++
	} else {
		indent(fail("/dev/sev-guest absent"))
		indent(dim("Requires kernel ≥ 5.19 built with CONFIG_SEV_GUEST=y"))
	}

	// ── Layer 6: SNP Attestation Report ─────────────────────────────────────
	section("6", "SNP Attestation Report  (ioctl SNP_GET_REPORT on /dev/sev-guest)")

	var nonce [64]byte
	copy(nonce[:], "snp-check nonce: please verify me")

	report, rawBytes, err := getSNPReport(nonce)
	if err != nil {
		indent(fail("SNP_GET_REPORT failed: " + err.Error()))
		indent(dim("If /dev/sev-guest exists but ioctl fails, check that you are root"))
		indent(dim("and that the kernel was not started with nosnp or similar flags."))
	} else {
		indent(pass(fmt.Sprintf(
			"Attestation report received from AMD Secure Processor (%d bytes)", len(rawBytes))))
		evidence += 2 // strongest possible evidence
		fmt.Println()
		printReport(report)
	}

	// ── Verdict ──────────────────────────────────────────────────────────────
	fmt.Printf("\n%s%s╔══════════════════════════════════════════════════╗%s\n",
		ansiBold, ansiCyan, ansiReset)
	switch {
	case evidence >= 3:
		fmt.Printf("%s%s║  ✓  CONFIRMED: AMD SEV-SNP Confidential VM       ║%s\n",
			ansiBold, ansiGreen, ansiReset)
		fmt.Printf("%s%s║     Attestation report signed by AMD Secure CPU  ║%s\n",
			ansiBold, ansiGreen, ansiReset)
	case evidence == 1:
		fmt.Printf("%s%s║  ⚠  PARTIAL: /dev/sev-guest found, no report     ║%s\n",
			ansiBold, ansiYellow, ansiReset)
		fmt.Printf("%s%s║     Likely SNP but attestation unconfirmed        ║%s\n",
			ansiBold, ansiYellow, ansiReset)
	default:
		fmt.Printf("%s%s║  ✗  NOT confirmed as AMD SEV-SNP CVM              ║%s\n",
			ansiBold, ansiRed, ansiReset)
	}
	fmt.Printf("%s%s╚══════════════════════════════════════════════════╝%s\n\n",
		ansiBold, ansiCyan, ansiReset)
}
