package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// IOCTL_VM_SOCKETS_GET_LOCAL_CID = _IO(7, 0xb9) = (7<<8)|0xb9 = 0x7b9
const ioctlGetLocalCID uintptr = 0x7b9

// Well-known vsock CIDs (from <linux/vm_sockets.h>)
const (
	CIDHypervisor uint32 = 0 // VMADDR_CID_HYPERVISOR
	CIDLocal      uint32 = 1 // VMADDR_CID_LOCAL
	CIDHost       uint32 = 2 // VMADDR_CID_HOST � assigned to the bare-metal host
)

// VMInfo is the result of VM detection.
type VMInfo struct {
	// IsVM is true when we are running inside a virtual machine.
	IsVM bool
	// CID is the vsock context ID of this VM. Only meaningful when IsVM is true
	// and the vsock CID was successfully retrieved (CID > CIDHost).
	CID uint32
}

// String returns a human-readable summary.
func (v VMInfo) String() string {
	if !v.IsVM {
		return "host (bare metal)"
	}
	if v.CID > CIDHost {
		return fmt.Sprintf("VM (CID=%d)", v.CID)
	}
	return "VM (CID unknown)"
}

// GetLocalCID opens /dev/vsock and issues the IOCTL_VM_SOCKETS_GET_LOCAL_CID
// ioctl to retrieve this context's vsock CID.
//
//   - Returns (cid, nil) on success.
//   - Returns an error if /dev/vsock is absent (host without vsock support) or
//     if the ioctl fails (e.g. permission denied, or running on the host side
//     of a vsock-capable hypervisor where the host CID == CIDHost).
func GetLocalCID() (uint32, error) {
	f, err := os.Open("/dev/vsock")
	if err != nil {
		return 0, fmt.Errorf("open /dev/vsock: %w", err)
	}
	defer f.Close()

	var cid uint32
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		ioctlGetLocalCID,
		uintptr(unsafe.Pointer(&cid)),
	)
	if errno != 0 {
		return 0, fmt.Errorf("ioctl IOCTL_VM_SOCKETS_GET_LOCAL_CID: %w", errno)
	}
	return cid, nil
}

// IsHypervisorFlagSet scans /proc/cpuinfo for the "hypervisor" CPU flag.
// Most Type-1 (KVM, Xen, Hyper-V) and Type-2 (VMware, VirtualBox) hypervisors
// set this flag via CPUID leaf 1, bit 31 of ECX.
func IsHypervisorFlagSet() bool {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// The flags line looks like: "flags		: fpu vme de ... hypervisor ..."
		if strings.HasPrefix(line, "flags") && strings.Contains(line, "hypervisor") {
			return true
		}
	}
	return false
}

// Detect reports whether the current process is running inside a VM and,
// if it is, the vsock CID assigned to this VM.
//
// Detection strategy (in order):
//  1. Open /dev/vsock and call IOCTL_VM_SOCKETS_GET_LOCAL_CID.
//     A CID > CIDHost (2) unambiguously means we are a guest VM.
//     A CID == CIDHost means we are the host side of a vsock-aware hypervisor.
//  2. If vsock is unavailable, fall back to checking the "hypervisor" flag in
//     /proc/cpuinfo � present in virtually all modern hypervisors but gives no
//     CID information.
func Detect() VMInfo {
	cid, err := GetLocalCID()
	if err == nil {
		// CID > 2  -> guest VM with a real CID
		// CID == 2 -> this is the host (VMADDR_CID_HOST)
		// CID <= 1 -> unexpected, treat as host
		return VMInfo{
			IsVM: cid > CIDHost,
			CID:  cid,
		}
	}

	// vsock path failed � fall back to cpuinfo hypervisor flag.
	return VMInfo{IsVM: IsHypervisorFlagSet()}
}

func PrintVMInfo() {
	if info := Detect(); info.IsVM {
		fmt.Printf("VM with CID=%d\n", info.CID)
	} else {
		fmt.Printf("Host with CID=%d\n", info.CID)
	}
}
