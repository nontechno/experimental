// vsock-vm: connects to the host (CID 2) on a vsock port, sends a blob,
// reads the response, saves it to a file, and closes the connection.
//
// Usage: vsock-vm -port 1234 -out response.bin
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"syscall"
	"unsafe"
)

const (
	AF_VSOCK        = 40
	VMADDR_CID_HOST = 2 // host CID as seen from inside the VM
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

// requestBlob is what the VM sends to the host.
var requestBlob = []byte("HELLO_FROM_VM_v1\x00\xCA\xFE\xBA\xBE")

func main() {
	port := flag.Uint("port", 1234, "vsock port on the host")
	outFile := flag.String("out", "response.bin", "file to save the host response")
	flag.Parse()

	fd, err := syscall.Socket(AF_VSOCK, syscall.SOCK_STREAM, 0)
	if err != nil {
		log.Fatalf("socket: %v", err)
	}
	defer syscall.Close(fd)

	sa := &sockaddrVM{
		Family: AF_VSOCK,
		Port:   uint32(*port),
		CID:    VMADDR_CID_HOST,
	}

	log.Printf("connecting to CID=%d port=%d …", VMADDR_CID_HOST, *port)
	if err := connect(fd, sa); err != nil {
		log.Fatalf("connect: %v", err)
	}
	log.Printf("connected")

	// Send length-prefixed request blob.
	if err := writeBlob(fd, requestBlob); err != nil {
		log.Fatalf("send blob: %v", err)
	}
	log.Printf("sent %d bytes", len(requestBlob))

	// Read length-prefixed response from host.
	resp, err := readBlob(fd)
	if err != nil {
		log.Fatalf("read response: %v", err)
	}
	log.Printf("received %d bytes: %q", len(resp), truncate(resp, 64))

	// Save response to file.
	if err := os.WriteFile(*outFile, resp, 0o644); err != nil {
		log.Fatalf("save response: %v", err)
	}
	log.Printf("response saved to %s", *outFile)

	// Connection is closed by the host, but close our side too.
	syscall.Close(fd)
	log.Printf("done")
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

// connect wraps the raw connect syscall for AF_VSOCK.
func connect(fd int, sa *sockaddrVM) error {
	_, _, errno := syscall.Syscall(
		syscall.SYS_CONNECT,
		uintptr(fd),
		uintptr(unsafe.Pointer(sa)),
		uintptr(unsafe.Sizeof(*sa)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
