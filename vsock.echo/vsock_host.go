// vsock-host: listens on a vsock port, receives a blob, sends a response blob, closes.
// Run on the host (CID is assigned by the hypervisor; VMADDR_CID_HOST = 2 from the VM's perspective).
//
// Usage: vsock-host -port 1234
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"syscall"
	"unsafe"
)

const (
	AF_VSOCK        = 40
	VMADDR_CID_ANY  = ^uint32(0) // 0xFFFFFFFF — bind to any CID
	VMADDR_PORT_ANY = ^uint32(0)
)

// sockaddrVM mirrors struct sockaddr_vm from <linux/vm_sockets.h>.
type sockaddrVM struct {
	Family    uint16
	Reserved1 uint16
	Port      uint32
	CID       uint32
	Flags     uint8
	Pad       [3]uint8
}

func (sa *sockaddrVM) sockaddr() (unsafe.Pointer, _Socklen, error) {
	return unsafe.Pointer(sa), _Socklen(unsafe.Sizeof(*sa)), nil
}

type _Socklen uint32

// responseBlob is what the host sends back to every client.
var responseBlob = []byte("HELLO_FROM_HOST_v1\x00\xDE\xAD\xBE\xEF")

func main() {
	port := flag.Uint("port", 1234, "vsock port to listen on")
	flag.Parse()

	fd, err := syscall.Socket(AF_VSOCK, syscall.SOCK_STREAM, 0)
	if err != nil {
		log.Fatalf("socket: %v", err)
	}
	defer syscall.Close(fd)

	sa := &sockaddrVM{
		Family: AF_VSOCK,
		Port:   uint32(*port),
		CID:    VMADDR_CID_ANY,
	}

	if err := bind(fd, sa); err != nil {
		log.Fatalf("bind: %v", err)
	}
	if err := syscall.Listen(fd, 8); err != nil {
		log.Fatalf("listen: %v", err)
	}

	log.Printf("vsock-host listening on port %d (AF_VSOCK)", *port)

	for {
		connFD, _, err := accept(fd)
		if err != nil {
			log.Printf("accept error: %v", err)
			continue
		}
		go handle(connFD)
	}
}

func handle(fd int) {
	defer syscall.Close(fd)

	log.Printf("[fd=%d] connection accepted", fd)

	// Read length-prefixed blob: 4-byte big-endian uint32 length, then payload.
	blob, err := readBlob(fd)
	if err != nil {
		log.Printf("[fd=%d] read blob: %v", fd, err)
		return
	}
	log.Printf("[fd=%d] received %d bytes: %q", fd, len(blob), truncate(blob, 64))

	// Write length-prefixed response.
	if err := writeBlob(fd, responseBlob); err != nil {
		log.Printf("[fd=%d] write blob: %v", fd, err)
		return
	}
	log.Printf("[fd=%d] sent %d-byte response, closing", fd, len(responseBlob))
}

// readBlob reads a 4-byte big-endian length header then that many bytes.
func readBlob(fd int) ([]byte, error) {
	var lenBuf [4]byte
	if err := readFull(fd, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	if err := readFull(fd, buf); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	return buf, nil
}

// writeBlob sends a 4-byte big-endian length header then the payload.
func writeBlob(fd int, data []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if err := writeFull(fd, lenBuf[:]); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	return writeFull(fd, data)
}

func readFull(fd int, buf []byte) error {
	for len(buf) > 0 {
		n, err := syscall.Read(fd, buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("unexpected EOF")
		}
		buf = buf[n:]
	}
	return nil
}

func writeFull(fd int, buf []byte) error {
	for len(buf) > 0 {
		n, err := syscall.Write(fd, buf)
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

// bind wraps the raw sockaddr_vm bind syscall.
func bind(fd int, sa *sockaddrVM) error {
	_, _, errno := syscall.Syscall(
		syscall.SYS_BIND,
		uintptr(fd),
		uintptr(unsafe.Pointer(sa)),
		uintptr(unsafe.Sizeof(*sa)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// accept wraps the raw accept syscall for AF_VSOCK.
func accept(fd int) (int, *sockaddrVM, error) {
	sa := &sockaddrVM{}
	saLen := uint32(unsafe.Sizeof(*sa))
	nfd, _, errno := syscall.Syscall(
		syscall.SYS_ACCEPT,
		uintptr(fd),
		uintptr(unsafe.Pointer(sa)),
		uintptr(unsafe.Pointer(&saLen)),
	)
	if errno != 0 {
		return 0, nil, errno
	}
	return int(nfd), sa, nil
}

func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
