//go:build amd64

package main

import (
	"fmt"
	"strings"
)

// cpuid executes the CPUID instruction. Implemented in cpuid_amd64.s.
// Uses ABI0 (stack-based), which Go automatically wraps for calls from Go code.
func cpuid(leaf, subleaf uint32) (eax, ebx, ecx, edx uint32)

// ── CPU vendor ───────────────────────────────────────────────────────────────

// getCPUVendor returns the 12-byte CPUID vendor string (e.g. "AuthenticAMD").
func getCPUVendor() string {
	_, ebx, ecx, edx := cpuid(0, 0)
	// Vendor string is packed as EBX || EDX || ECX (Intel/AMD convention).
	b := make([]byte, 12)
	putU32LE(b[0:], ebx)
	putU32LE(b[4:], edx)
	putU32LE(b[8:], ecx)
	return strings.TrimRight(string(b), "\x00")
}

// ── SEV feature bits (CPUID leaf 0x8000001F) ────────────────────────────────

// SEVFeatures holds the decoded fields from CPUID 0x8000001F EAX and EBX.
//
// AMD Architecture Programmer's Manual Vol.3, section 15.34.1.
type SEVFeatures struct {
	// EAX feature flags
	SME             bool // bit  0 – Secure Memory Encryption
	SEV             bool // bit  1 – Secure Encrypted Virtualization
	PageFlushMSR    bool // bit  2 – PAGE_FLUSH MSR present
	SEVES           bool // bit  3 – SEV Encrypted State (SEV-ES)
	SNP             bool // bit  4 – SEV Secure Nested Paging (SEV-SNP)
	VMPL            bool // bit  5 – VM Permission Levels
	RMPQUERY        bool // bit  6 – RMPQUERY instruction available
	VMPLSSS         bool // bit  7 – VMPL Supervisor Shadow Stack
	SecureTSC       bool // bit  8 – Secure TSC
	VMGEXIT         bool // bit  9 – VMGEXIT parameter
	HVirtAddrChange bool // bit 10
	VMGEXITPARAM    bool // bit 11
	IbsVirt         bool // bit 12 – IBS virtualization
	VmsaRegProt     bool // bit 15 – VMSA Register Protection
	SmtProtect      bool // bit 16 – SMT Protection
	SevTSCScaleMsr  bool // bit 17

	// EBX fields
	CBitPos           uint32 // bits  5:0  – C-bit position in page table entry
	PhysAddrReduction uint32 // bits 11:6  – number of physical address bits reduced for encrypted mem
	NumVMPLs          uint32 // bits 17:12 – number of VMPLs supported
}

// getSEVFeatures reads CPUID leaf 0x8000001F and returns the decoded fields.
func getSEVFeatures() SEVFeatures {
	eax, ebx, _, _ := cpuid(0x8000001F, 0)

	bit := func(v uint32, n uint) bool { return v>>n&1 == 1 }

	return SEVFeatures{
		SME:             bit(eax, 0),
		SEV:             bit(eax, 1),
		PageFlushMSR:    bit(eax, 2),
		SEVES:           bit(eax, 3),
		SNP:             bit(eax, 4),
		VMPL:            bit(eax, 5),
		RMPQUERY:        bit(eax, 6),
		VMPLSSS:         bit(eax, 7),
		SecureTSC:       bit(eax, 8),
		VMGEXIT:         bit(eax, 9),
		HVirtAddrChange: bit(eax, 10),
		VMGEXITPARAM:    bit(eax, 11),
		IbsVirt:         bit(eax, 12),
		VmsaRegProt:     bit(eax, 15),
		SmtProtect:      bit(eax, 16),
		SevTSCScaleMsr:  bit(eax, 17),

		CBitPos:           ebx & 0x3F,
		PhysAddrReduction: (ebx >> 6) & 0x3F,
		NumVMPLs:          (ebx >> 12) & 0x3F,
	}
}

// printSEVFeatures pretty-prints the SEV feature set.
func printSEVFeatures(f SEVFeatures) {
	type featEntry struct {
		name    string
		enabled bool
		desc    string
	}

	entries := []featEntry{
		{"SME", f.SME, "Secure Memory Encryption"},
		{"SEV", f.SEV, "Secure Encrypted Virtualization"},
		{"SEV-ES", f.SEVES, "SEV Encrypted State"},
		{"SEV-SNP", f.SNP, "SEV Secure Nested Paging  ← key feature"},
		{"VMPL", f.VMPL, fmt.Sprintf("VM Permission Levels (%d levels)", f.NumVMPLs)},
		{"SecureTSC", f.SecureTSC, "Secure TSC"},
		{"VMSA-RegProt", f.VmsaRegProt, "VMSA Register Protection"},
	}

	for _, e := range entries {
		if e.enabled {
			indent(pass(fmt.Sprintf("%-14s  %s", e.name, e.desc)))
		} else {
			indent(warn(fmt.Sprintf("%-14s  %s  (not set)", e.name, e.desc)))
		}
	}

	fmt.Println()
	field("C-bit position in PTE", fmt.Sprintf("%d (physical addr bit that marks page as encrypted)",
		f.CBitPos))
	field("Physical addr bits reduced", fmt.Sprintf("%d", f.PhysAddrReduction))
}

// ── helpers ──────────────────────────────────────────────────────────────────

func putU32LE(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
