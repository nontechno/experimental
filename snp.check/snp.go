//go:build amd64

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ── ioctl constants ──────────────────────────────────────────────────────────
//
// Linux exposes two generations of the SNP_GET_REPORT ioctl depending on
// kernel version.  We try the newer interface first and fall back to the old.
//
// Derivation: _IOWR(type, nr, size) = (3<<30) | (size<<16) | (type<<8) | nr
//
//   Kernel ≥ 6.8 — struct snp_user_guest_request (24 bytes)
//     _IOWR('S', 0, 24) = 0xC000_0000 | 0x0018_0000 | 0x0000_5300 = 0xC018_5300
//
//   Kernel 5.19–6.7 — struct snp_guest_request_ioctl (32 bytes)
//     _IOWR('S', 0, 32) = 0xC000_0000 | 0x0020_0000 | 0x0000_5300 = 0xC020_5300

const (
	ioctlSNPGetReportNew uintptr = 0xC0185300 // kernel ≥ 6.8
	ioctlSNPGetReportOld uintptr = 0xC0205300 // kernel 5.19–6.7
)

// ── request/response structs (same across both kernel versions) ──────────────

// snpReportReq mirrors struct snp_report_req (include/uapi/linux/psp-sev.h).
//
//	struct snp_report_req {
//	    __u8  user_data[64];
//	    __u32 vmpl;
//	    __u8  rsvd[28];   // must be zero
//	};
type snpReportReq struct {
	UserData [64]byte
	VMPL     uint32
	Reserved [28]byte
}

// snpReportResp mirrors struct snp_report_resp.
//
//	struct snp_report_resp {
//	    __u8 data[4000];
//	};
//
// Layout of data[]:
//
//	bytes   0– 3  : response status (0 = success)
//	bytes   4– 7  : report size in bytes
//	bytes   8–31  : reserved
//	bytes  32–1215: attestation report (1184 = 0x4A0 bytes)
type snpReportResp struct {
	Data [4000]byte
}

// ── new ioctl wrapper (kernel ≥ 6.8) ────────────────────────────────────────

// snpUserGuestRequest mirrors struct snp_user_guest_request.
//
//	struct snp_user_guest_request {
//	    __u64 req_data;
//	    __u64 resp_data;
//	    union { __u64 exitinfo2; struct { __u32 fw_error; __u32 vmm_error; }; };
//	};
type snpUserGuestRequest struct {
	ReqData   uint64
	RespData  uint64
	ExitInfo2 uint64 // low 32 bits = fw_error; high 32 bits = vmm_error
}

// ── old ioctl wrapper (kernel 5.19–6.7) ─────────────────────────────────────

// snpGuestRequestIOCTL mirrors struct snp_guest_request_ioctl.
//
//	struct snp_guest_request_ioctl {
//	    __u8  msg_version;    // must be 1
//	    /* 7 bytes implicit padding for alignment */
//	    __u64 req_data;
//	    __u64 resp_data;
//	    __u64 fw_err;
//	};
type snpGuestRequestIOCTL struct {
	MsgVersion uint8
	pad        [7]byte // explicit padding to align the uint64 fields
	ReqData    uint64
	RespData   uint64
	FWErr      uint64
}

// ── attestation report response offsets ─────────────────────────────────────

const (
	respStatusOffset = 0
	respSizeOffset   = 4
	respReportOffset = 32   // attestation report starts here inside resp.Data
	reportSize       = 1184 // 0x4A0 bytes (AMD SEV-SNP ABI spec, Table 21)
)

// ── public API ───────────────────────────────────────────────────────────────

// getSNPReport issues the SNP_GET_REPORT ioctl on /dev/sev-guest.
//
// userData is a caller-supplied 64-byte nonce that is embedded verbatim into
// the REPORT_DATA field of the attestation report (anti-replay).
//
// Returns:
//   - a parsed AttestationReport
//   - the raw 1184-byte report blob
//   - an error if the ioctl fails or the response is malformed
func getSNPReport(userData [64]byte) (*AttestationReport, []byte, error) {
	f, err := os.OpenFile("/dev/sev-guest", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/sev-guest: %w", err)
	}
	defer f.Close()

	req := snpReportReq{
		UserData: userData,
		VMPL:     0, // VMPL 0 = highest privilege level inside the VM
	}
	var resp snpReportResp

	// Try newer interface first; fall back to old on ENOTTY / EINVAL.
	raw, fwErr, err := ioctlNew(f.Fd(), &req, &resp)
	if err != nil {
		if errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.EINVAL) {
			indent(warn("new ioctl (0xC0185300) rejected — retrying with old (0xC0205300)"))
			raw, fwErr, err = ioctlOld(f.Fd(), &req, &resp)
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("SNP_GET_REPORT: %w (fw_err=0x%08x)", err, fwErr)
	}

	if len(raw) < reportSize {
		return nil, raw, fmt.Errorf("report too short: got %d bytes, want %d", len(raw), reportSize)
	}

	report, err := parseReport(raw[:reportSize])
	if err != nil {
		return nil, raw, fmt.Errorf("parse report: %w", err)
	}
	return report, raw[:reportSize], nil
}

// ── internal ioctl helpers ───────────────────────────────────────────────────

func ioctlNew(fd uintptr, req *snpReportReq, resp *snpReportResp) ([]byte, uint32, error) {
	wrapper := snpUserGuestRequest{
		ReqData:  uint64(uintptr(unsafe.Pointer(req))),
		RespData: uint64(uintptr(unsafe.Pointer(resp))),
	}
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, fd,
		ioctlSNPGetReportNew,
		uintptr(unsafe.Pointer(&wrapper)),
	); errno != 0 {
		return nil, uint32(wrapper.ExitInfo2), errno
	}
	return extractReport(resp), uint32(wrapper.ExitInfo2), nil
}

func ioctlOld(fd uintptr, req *snpReportReq, resp *snpReportResp) ([]byte, uint32, error) {
	wrapper := snpGuestRequestIOCTL{
		MsgVersion: 1,
		ReqData:    uint64(uintptr(unsafe.Pointer(req))),
		RespData:   uint64(uintptr(unsafe.Pointer(resp))),
	}
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, fd,
		ioctlSNPGetReportOld,
		uintptr(unsafe.Pointer(&wrapper)),
	); errno != 0 {
		return nil, uint32(wrapper.FWErr), errno
	}
	return extractReport(resp), uint32(wrapper.FWErr), nil
}

// extractReport validates the firmware response header and returns the raw
// attestation report bytes starting at offset 32 inside resp.Data.
func extractReport(resp *snpReportResp) []byte {
	// Bytes 0–3: status; bytes 4–7: report size; bytes 8–31: reserved.
	status := le32(resp.Data[:], 0)
	size := le32(resp.Data[:], 4)
	if status != 0 {
		// Status is documented in AMD SEV Firmware ABI spec §4.
		// Common values: 0x16 = invalid parameter, 0x1A = resource in use.
		panic(fmt.Sprintf("firmware returned non-zero status: 0x%08x", status))
	}
	end := respReportOffset + int(size)
	if end > len(resp.Data) {
		end = len(resp.Data)
	}
	return resp.Data[respReportOffset:end]
}
