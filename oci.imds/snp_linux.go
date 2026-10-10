//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// SNP_GET_REPORT = _IOWR('S', 0x0, struct snp_guest_request_ioctl), a 32-byte struct.
const snpGetReportIoctl = 0xC0205300

type snpGuestRequestIoctl struct {
	MsgVersion uint8
	_          [7]byte
	ReqData    uint64 // address of struct snp_report_req (96 bytes)
	RespData   uint64 // address of struct snp_report_resp (4000 bytes)
	Exitinfo2  uint64 // low 32 bits: firmware error, high 32 bits: VMM error
}

// Package-level so the buffers live on the heap and cannot move while their
// addresses are stored in uint64 fields of the request.
var snpReqBuf, snpRespBuf []byte

// snpGetReport asks the SNP firmware, through /dev/sev-guest, for a signed
// attestation report that embeds userData (up to 64 bytes) as REPORT_DATA.
// It returns the raw response buffer (status, report size, then the report).
func snpGetReport(device string, userData []byte, vmpl uint32) ([]byte, error) {
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	snpReqBuf = make([]byte, 96)
	snpRespBuf = make([]byte, 4000)
	copy(snpReqBuf[:64], userData)
	binary.LittleEndian.PutUint32(snpReqBuf[64:68], vmpl)

	req := snpGuestRequestIoctl{
		MsgVersion: 1,
		ReqData:    uint64(uintptr(unsafe.Pointer(&snpReqBuf[0]))),
		RespData:   uint64(uintptr(unsafe.Pointer(&snpRespBuf[0]))),
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), snpGetReportIoctl, uintptr(unsafe.Pointer(&req)))
	runtime.KeepAlive(snpReqBuf)
	runtime.KeepAlive(snpRespBuf)
	if errno != 0 {
		return nil, fmt.Errorf("SNP_GET_REPORT ioctl on %s: %v (fw_error=0x%x vmm_error=0x%x)",
			device, errno, uint32(req.Exitinfo2), uint32(req.Exitinfo2>>32))
	}
	out := make([]byte, len(snpRespBuf))
	copy(out, snpRespBuf)
	return out, nil
}

// kernelLog reads the kernel ring buffer without blocking
// (SYSLOG_ACTION_READ_ALL). It may be empty without CAP_SYSLOG.
func kernelLog() string {
	buf := make([]byte, 1<<20)
	n, err := syscall.Klogctl(3, buf)
	if err != nil || n <= 0 {
		return ""
	}
	return string(buf[:n])
}
