// go build -o receiver receiver.go
// go run receiver.go --port 5000 --show-cid
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"
)

// ── vsock primitives ────────────────────────────────────────────────────────

const afVSOCK = 40

const (
	CIDAny  uint32 = 0xFFFFFFFF
	CIDHost uint32 = 2
)

// sockaddrVM mirrors struct sockaddr_vm (16 bytes).
type sockaddrVM struct {
	family    uint16
	reserved1 uint16
	port      uint32
	cid       uint32
	zero      [4]byte
}

func (s *sockaddrVM) toRaw() (syscall.RawSockaddrAny, uint32) {
	var raw syscall.RawSockaddrAny
	copy(
		(*[syscall.SizeofSockaddrAny]byte)(unsafe.Pointer(&raw))[:],
		(*[16]byte)(unsafe.Pointer(s))[:],
	)
	return raw, 16
}

// Addr implements net.Addr.
type Addr struct{ CID, Port uint32 }

func (a *Addr) Network() string { return "vsock" }
func (a *Addr) String() string  { return fmt.Sprintf("vsock(%d:%d)", a.CID, a.Port) }

// Conn wraps a vsock file descriptor and implements net.Conn.
type Conn struct {
	fd    int
	local *Addr
	peer  *Addr
}

func (c *Conn) Read(b []byte) (int, error)  { return syscall.Read(c.fd, b) }
func (c *Conn) Write(b []byte) (int, error) { return syscall.Write(c.fd, b) }
func (c *Conn) Close() error                { return syscall.Close(c.fd) }
func (c *Conn) LocalAddr() net.Addr         { return c.local }
func (c *Conn) RemoteAddr() net.Addr        { return c.peer }
func (c *Conn) SetDeadline(t time.Time) error {
	return setDeadline(c.fd, t, true, true)
}
func (c *Conn) SetReadDeadline(t time.Time) error {
	return setDeadline(c.fd, t, true, false)
}
func (c *Conn) SetWriteDeadline(t time.Time) error {
	return setDeadline(c.fd, t, false, true)
}

func setDeadline(fd int, t time.Time, r, w bool) error {
	var tv syscall.Timeval
	if !t.IsZero() {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		tv = syscall.NsecToTimeval(d.Nanoseconds())
	}
	if r {
		if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
			return err
		}
	}
	if w {
		if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_SNDTIMEO, &tv); err != nil {
			return err
		}
	}
	return nil
}

// Listener implements net.Listener for vsock.
type Listener struct {
	fd   int
	addr *Addr
}

func Listen(cid, port uint32) (*Listener, error) {
	fd, err := syscall.Socket(afVSOCK, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	sa := &sockaddrVM{family: afVSOCK, port: port, cid: cid}
	raw, rawLen := sa.toRaw()
	if _, _, errno := syscall.Syscall(
		syscall.SYS_BIND, uintptr(fd),
		uintptr(unsafe.Pointer(&raw)), uintptr(rawLen),
	); errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("vsock bind cid=%d port=%d: %w", cid, port, errno)
	}
	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &Listener{fd: fd, addr: &Addr{CID: cid, Port: port}}, nil
}

func (l *Listener) Accept() (net.Conn, error) {
	var raw syscall.RawSockaddrAny
	rawLen := uint32(syscall.SizeofSockaddrAny)
	nfd, _, errno := syscall.Syscall(
		syscall.SYS_ACCEPT4, uintptr(l.fd),
		uintptr(unsafe.Pointer(&raw)),
		uintptr(unsafe.Pointer(&rawLen)),
	)
	if errno != 0 {
		return nil, &net.OpError{Op: "accept", Net: "vsock", Err: errno}
	}
	peer := (*sockaddrVM)(unsafe.Pointer(&raw))
	return &Conn{
		fd:    int(nfd),
		local: l.addr,
		peer:  &Addr{CID: peer.cid, Port: peer.port},
	}, nil
}

func (l *Listener) Close() error   { return syscall.Close(l.fd) }
func (l *Listener) Addr() net.Addr { return l.addr }

// LocalCID reads our vsock CID via ioctl on /dev/vsock.
func LocalCID() (uint32, error) {
	f, err := os.Open("/dev/vsock")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	const ioctlGetLocalCID = 0x7b9
	var cid uint32
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, f.Fd(),
		ioctlGetLocalCID,
		uintptr(unsafe.Pointer(&cid)),
	); errno != 0 {
		return 0, errno
	}
	return cid, nil
}

// WriteMsg sends a length-prefixed message: [4-byte BE length][payload].
func WriteMsg(c net.Conn, data []byte) error {
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(data)))
	if _, err := c.Write(hdr); err != nil {
		return err
	}
	_, err := c.Write(data)
	return err
}

// ReadMsg reads a message written by WriteMsg.
func ReadMsg(c net.Conn) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if n > 4<<20 {
		return nil, fmt.Errorf("message too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ── wire types ──────────────────────────────────────────────────────────────

type Message struct {
	Seq       int       `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Body      string    `json:"body"`
}

// ── main ────────────────────────────────────────────────────────────────────

func main() {
	port := flag.Uint("port", 5000, "vsock port to listen on")
	showCID := flag.Bool("show-cid", false, "print local CID before listening")
	flag.Parse()

	logger := log.New(os.Stderr, "[receiver] ", log.Ltime|log.Lmsgprefix)

	if *showCID {
		if cid, err := LocalCID(); err != nil {
			logger.Printf("warn: could not read local CID: %v", err)
		} else {
			fmt.Printf("local CID: %d\n", cid)
		}
	}

	ln, err := Listen(CIDAny, uint32(*port))
	if err != nil {
		logger.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	logger.Printf("listening on %s", ln.Addr())

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		logger.Println("shutting down")
		ln.Close()
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			logger.Printf("accept: %v", err)
			return
		}
		logger.Printf("accepted connection from %s", conn.RemoteAddr())
		go handleConn(conn, logger)
	}
}

func handleConn(conn net.Conn, logger *log.Logger) {
	defer conn.Close()

	for {
		raw, err := ReadMsg(conn)
		if err != nil {
			if err != io.EOF {
				logger.Printf("read: %v", err)
			}
			logger.Println("connection closed")
			return
		}

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			logger.Printf("decode: %v", err)
			continue
		}

		fmt.Printf("msg #%d  at=%s  body=%q\n",
			msg.Seq,
			msg.Timestamp.Format(time.RFC3339),
			msg.Body,
		)

		ack := fmt.Sprintf(`{"ack":%d}`, msg.Seq)
		if err := WriteMsg(conn, []byte(ack)); err != nil {
			logger.Printf("ack: %v", err)
			return
		}
	}
}
