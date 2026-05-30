//go:build amd64

package main

import (
	"encoding/binary"
	"fmt"
)

// ── attestation report struct ────────────────────────────────────────────────
//
// AMD SEV-SNP Firmware ABI Specification, §7.3, Table 21.
// Total size: 0x4A0 = 1184 bytes.
//
// We parse by offset (not via binary.Read) to be immune to Go struct padding.

// AttestationReport is the decoded SEV-SNP attestation report.
type AttestationReport struct {
	// ── Header fields (0x000–0x04F) ─────────────────────────────────────────
	Version      uint32     // 0x000 – must be 2
	GuestSVN     uint32     // 0x004 – guest security version
	Policy       uint64     // 0x008 – guest policy bits (see PolicyBits)
	FamilyID     [16]byte   // 0x010 – family identifier set at launch
	ImageID      [16]byte   // 0x020 – image identifier set at launch
	VMPL         uint32     // 0x030 – VMPL level at which report was generated
	SigAlgo      uint32     // 0x034 – 1 = ECDSA P-384 SHA-384
	CurrentTCB   TCBVersion // 0x038
	PlatformInfo uint64     // 0x040 – platform configuration flags
	AuthorKeyEn  uint32     // 0x048 – bit 0: author key block included in IDBlock
	// 0x04C reserved

	// ── Caller-supplied nonce ────────────────────────────────────────────────
	ReportData [64]byte // 0x050 – echoed back from SNP_GET_REPORT request

	// ── Measurements ────────────────────────────────────────────────────────
	Measurement [48]byte // 0x090 – SHA-384 of guest memory at launch
	HostData    [32]byte // 0x0C0 – host-supplied opaque data

	// ── Identity ────────────────────────────────────────────────────────────
	IDKeyDigest     [48]byte // 0x0E0 – SHA-384 of ID signing key (if used)
	AuthorKeyDigest [48]byte // 0x110 – SHA-384 of author signing key (if used)
	ReportID        [32]byte // 0x140 – unique ID of this report
	ReportIDMA      [32]byte // 0x160 – ID of the migration agent (if any)

	// ── TCB versions ────────────────────────────────────────────────────────
	ReportedTCB TCBVersion // 0x180
	// 0x188–0x19F reserved
	ChipID       [64]byte   // 0x1A0 – unique chip identifier (VCEK KDS key)
	CommittedTCB TCBVersion // 0x1E0

	// ── Software versions ───────────────────────────────────────────────────
	CurrentBuild uint8 // 0x1E8
	CurrentMinor uint8 // 0x1E9
	CurrentMajor uint8 // 0x1EA
	// 0x1EB reserved
	CommittedBuild uint8 // 0x1EC
	CommittedMinor uint8 // 0x1ED
	CommittedMajor uint8 // 0x1EE
	// 0x1EF reserved

	LaunchTCB TCBVersion // 0x1F0
	// 0x1F8–0x29F reserved (168 bytes)

	// ── Signature ───────────────────────────────────────────────────────────
	Signature ECDSASignature // 0x2A0
}

// TCBVersion is the 8-byte platform TCB version number.
// Each field is a separate SVN (security version number).
type TCBVersion struct {
	BootLoader uint8 // +0
	TEE        uint8 // +1
	// +2 through +5 reserved
	SNP       uint8 // +6
	Microcode uint8 // +7
}

func (t TCBVersion) String() string {
	return fmt.Sprintf("bl:%d tee:%d snp:%d ucode:%d",
		t.BootLoader, t.TEE, t.SNP, t.Microcode)
}

// ECDSASignature holds the P-384 signature appended to every report.
// R and S are each 48 bytes but stored in 72-byte fields (rest zero-padded).
type ECDSASignature struct {
	R [48]byte
	S [48]byte
}

// ── field offsets ─────────────────────────────────────────────────────────────

const (
	offVersion      = 0x000
	offGuestSVN     = 0x004
	offPolicy       = 0x008
	offFamilyID     = 0x010
	offImageID      = 0x020
	offVMPL         = 0x030
	offSigAlgo      = 0x034
	offCurrentTCB   = 0x038
	offPlatformInfo = 0x040
	offAuthorKeyEn  = 0x048
	offReportData   = 0x050
	offMeasurement  = 0x090
	offHostData     = 0x0C0
	offIDKeyDigest  = 0x0E0
	offAuthorKey    = 0x110
	offReportID     = 0x140
	offReportIDMA   = 0x160
	offReportedTCB  = 0x180
	offChipID       = 0x1A0
	offCommittedTCB = 0x1E0
	offCurBuild     = 0x1E8
	offCurMinor     = 0x1E9
	offCurMajor     = 0x1EA
	offComBuild     = 0x1EC
	offComMinor     = 0x1ED
	offComMajor     = 0x1EE
	offLaunchTCB    = 0x1F0
	offSignatureR   = 0x2A0
	offSignatureS   = 0x2A0 + 48
)

// ── parser ────────────────────────────────────────────────────────────────────

// parseReport decodes a 1184-byte raw attestation report.
func parseReport(data []byte) (*AttestationReport, error) {
	if len(data) < reportSize {
		return nil, fmt.Errorf("short report: %d < %d bytes", len(data), reportSize)
	}

	tcb := func(off int) TCBVersion {
		return TCBVersion{
			BootLoader: data[off+0],
			TEE:        data[off+1],
			SNP:        data[off+6],
			Microcode:  data[off+7],
		}
	}
	cpyN := func(dst []byte, off int) { copy(dst, data[off:off+len(dst)]) }

	r := &AttestationReport{}
	r.Version = le32(data, offVersion)
	r.GuestSVN = le32(data, offGuestSVN)
	r.Policy = le64(data, offPolicy)
	cpyN(r.FamilyID[:], offFamilyID)
	cpyN(r.ImageID[:], offImageID)
	r.VMPL = le32(data, offVMPL)
	r.SigAlgo = le32(data, offSigAlgo)
	r.CurrentTCB = tcb(offCurrentTCB)
	r.PlatformInfo = le64(data, offPlatformInfo)
	r.AuthorKeyEn = le32(data, offAuthorKeyEn)
	cpyN(r.ReportData[:], offReportData)
	cpyN(r.Measurement[:], offMeasurement)
	cpyN(r.HostData[:], offHostData)
	cpyN(r.IDKeyDigest[:], offIDKeyDigest)
	cpyN(r.AuthorKeyDigest[:], offAuthorKey)
	cpyN(r.ReportID[:], offReportID)
	cpyN(r.ReportIDMA[:], offReportIDMA)
	r.ReportedTCB = tcb(offReportedTCB)
	cpyN(r.ChipID[:], offChipID)
	r.CommittedTCB = tcb(offCommittedTCB)
	r.CurrentBuild = data[offCurBuild]
	r.CurrentMinor = data[offCurMinor]
	r.CurrentMajor = data[offCurMajor]
	r.CommittedBuild = data[offComBuild]
	r.CommittedMinor = data[offComMinor]
	r.CommittedMajor = data[offComMajor]
	r.LaunchTCB = tcb(offLaunchTCB)
	copy(r.Signature.R[:], data[offSignatureR:offSignatureR+48])
	copy(r.Signature.S[:], data[offSignatureS:offSignatureS+48])

	return r, nil
}

// ── little-endian helpers ────────────────────────────────────────────────────

func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func le64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }

// ── pretty printer ────────────────────────────────────────────────────────────

func printReport(r *AttestationReport) {
	fmt.Printf("  %s%sAttestation Report Fields%s\n", ansiBold, ansiCyan, ansiReset)
	fmt.Println()

	// ── Header ──────────────────────────────────────────────────────────────
	subheader("Header")
	field("Report version", fmt.Sprintf("%d", r.Version))
	field("Guest SVN", fmt.Sprintf("%d", r.GuestSVN))
	field("VMPL level", fmt.Sprintf("%d  (0=highest privilege)", r.VMPL))
	field("Signature algo", sigAlgoName(r.SigAlgo))
	field("Author key", fmt.Sprintf("%v", r.AuthorKeyEn&1 == 1))

	// ── Policy ──────────────────────────────────────────────────────────────
	fmt.Println()
	subheader(fmt.Sprintf("Guest Policy  (0x%016x)", r.Policy))
	printPolicy(r.Policy)

	// ── TCB versions ────────────────────────────────────────────────────────
	fmt.Println()
	subheader("TCB Versions  (bl=boot-loader  tee=firmware  snp=SNP  ucode=microcode)")
	field("Current  TCB", r.CurrentTCB.String())
	field("Reported TCB", r.ReportedTCB.String())
	field("Committed TCB", r.CommittedTCB.String())
	field("Launch    TCB", r.LaunchTCB.String())

	// ── Software versions ───────────────────────────────────────────────────
	fmt.Println()
	subheader("Software Versions")
	field("Current  (build.minor.major)",
		fmt.Sprintf("%d.%d.%d", r.CurrentBuild, r.CurrentMinor, r.CurrentMajor))
	field("Committed (build.minor.major)",
		fmt.Sprintf("%d.%d.%d", r.CommittedBuild, r.CommittedMinor, r.CommittedMajor))

	// ── Platform info ────────────────────────────────────────────────────────
	fmt.Println()
	subheader("Platform Info")
	field("SMT enabled", fmt.Sprintf("%v", r.PlatformInfo&(1<<0) != 0))
	field("TSME enabled", fmt.Sprintf("%v", r.PlatformInfo&(1<<1) != 0))

	// ── Identity ────────────────────────────────────────────────────────────
	fmt.Println()
	subheader("Identity")
	field("Chip ID", hexFmt(r.ChipID[:]))
	field("Family ID", hexFmt(r.FamilyID[:]))
	field("Image ID", hexFmt(r.ImageID[:]))
	field("Report ID", hexFmt(r.ReportID[:]))
	field("Report ID MA", hexFmt(r.ReportIDMA[:]))

	// ── Measurements ────────────────────────────────────────────────────────
	fmt.Println()
	subheader("Measurements")
	field("Measurement", hexFmt(r.Measurement[:]))
	indent(dim("  (SHA-384 of initial guest memory; compare against known-good image hash)"))
	field("Host data", hexFmt(r.HostData[:]))
	indent(dim("  (opaque data injected by the host/VMM at launch)"))
	field("Report data", hexFmt(r.ReportData[:]))
	indent(dim("  (the 64-byte nonce you passed in — confirms freshness)"))

	// ── Key digests ──────────────────────────────────────────────────────────
	if allZero(r.IDKeyDigest[:]) {
		field("ID key digest", dim("(all zeros — ID block not used)"))
	} else {
		field("ID key digest", hexFmt(r.IDKeyDigest[:]))
	}
	if allZero(r.AuthorKeyDigest[:]) {
		field("Author key digest", dim("(all zeros — author key not used)"))
	} else {
		field("Author key digest", hexFmt(r.AuthorKeyDigest[:]))
	}

	// ── Signature ────────────────────────────────────────────────────────────
	fmt.Println()
	subheader("ECDSA P-384 Signature  (signed by VCEK — AMD Versioned Chip Endorsement Key)")
	field("R", hexFmt(r.Signature.R[:]))
	field("S", hexFmt(r.Signature.S[:]))
	fmt.Println()
	indent(dim("To verify: fetch VCEK cert from AMD KDS and check P-384 signature over"))
	indent(dim("report bytes 0x000–0x29F using SHA-384."))
	indent(dim("  https://kdsintf.amd.com/vcek/v1/<product>/<chip_id_hex>?blSPL=<bl>&teeSPL=<tee>&snpSPL=<snp>&ucodeSPL=<ucode>"))
}

// ── policy decoder ────────────────────────────────────────────────────────────

type policyBit struct {
	bit      uint
	name     string
	desc     string
	warnWhen bool // print warn() instead of pass() when this bit is set
}

var policyBits = []policyBit{
	{0, "SMT", "Simultaneous multi-threading allowed", false},
	{1, "RSVD1", "Reserved (must be 1 in valid policy)", false},
	{2, "MIG_MA", "Migration agent allowed", false},
	{3, "DEBUG", "Debug mode enabled  ⚠ VM memory readable by host!", true},
	{4, "SINGLE_SOCKET", "Single-socket topology required", false},
	{17, "CXL_ALLOWED", "CXL memory allowed", false},
	{18, "MEM_AES_256_XTS", "AES-256-XTS memory encryption", false},
	{19, "RAPL_DIS", "RAPL power reporting disabled", false},
	{20, "CIPHERTEXT_HIDING", "Ciphertext hiding enabled", false},
}

func printPolicy(p uint64) {
	for _, pb := range policyBits {
		set := (p>>pb.bit)&1 == 1
		if set {
			if pb.warnWhen {
				indent(warn(fmt.Sprintf("bit %2d  %-20s  %s", pb.bit, pb.name, pb.desc)))
			} else {
				indent(pass(fmt.Sprintf("bit %2d  %-20s  %s", pb.bit, pb.name, pb.desc)))
			}
		} else {
			indent(dim(fmt.Sprintf("  bit %2d  %-20s  (not set)", pb.bit, pb.name)))
		}
	}

	// Minimum required firmware version (bits 8–15 = ABI minor, 16–23 = ABI major)
	minABIMinor := (p >> 8) & 0xFF
	minABIMajor := (p >> 16) & 0xFF
	field("Min ABI version required", fmt.Sprintf("%d.%d", minABIMajor, minABIMinor))
}

// ── formatting helpers ────────────────────────────────────────────────────────

func subheader(s string) {
	fmt.Printf("  %s%s%s\n", ansiBold, s, ansiReset)
}

func hexFmt(b []byte) string {
	if len(b) <= 16 {
		return fmt.Sprintf("%x", b)
	}
	// Split long hex into lines of 32 bytes
	out := fmt.Sprintf("%x\n", b[:16])
	for i := 16; i < len(b); i += 16 {
		end := i + 16
		if end > len(b) {
			end = len(b)
		}
		out += fmt.Sprintf("                             %x\n", b[i:end])
	}
	return out
}

func sigAlgoName(algo uint32) string {
	switch algo {
	case 1:
		return "1 (ECDSA P-384 SHA-384)"
	case 2:
		return "2 (ECDSA P-521 SHA-512)"
	default:
		return fmt.Sprintf("%d (unknown)", algo)
	}
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
