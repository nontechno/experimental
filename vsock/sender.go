// go build -o sender sender.go
// go run sender.go --cid 2 --port 5000 --count 5
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
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

type Addr struct{ CID, Port uint32 }

func (a *Addr) Network() string { return "vsock" }
func (a *Addr) String() string  { return fmt.Sprintf("vsock(%d:%d)", a.CID, a.Port) }

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

func Dial(cid, port uint32) (*Conn, error) {
	fd, err := syscall.Socket(afVSOCK, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	sa := &sockaddrVM{family: afVSOCK, port: port, cid: cid}
	raw, rawLen := sa.toRaw()
	if _, _, errno := syscall.Syscall(
		syscall.SYS_CONNECT, uintptr(fd),
		uintptr(unsafe.Pointer(&raw)), uintptr(rawLen),
	); errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("vsock connect cid=%d port=%d: %w", cid, port, errno)
	}
	return &Conn{
		fd:    fd,
		local: &Addr{CID: CIDAny, Port: 0},
		peer:  &Addr{CID: cid, Port: port},
	}, nil
}

func WriteMsg(c net.Conn, data []byte) error {
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(data)))
	if _, err := c.Write(hdr); err != nil {
		return err
	}
	_, err := c.Write(data)
	return err
}

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

type Ack struct {
	Ack int `json:"ack"`
}

// ── main ────────────────────────────────────────────────────────────────────

func main() {
	cid := flag.Uint("cid", 2, "target CID (2=host, or guest CID from receiver --show-cid)")
	port := flag.Uint("port", 5000, "target vsock port")
	count := flag.Int("count", 5, "number of messages to send")
	interval := flag.Duration("interval", 200*time.Millisecond, "delay between messages")
	body := flag.String("body", "hello from vsock", "message body")
	stdin := flag.Bool("stdin", false, "read message bodies from stdin, one per line")
	flag.Parse()

	logger := log.New(os.Stderr, "[sender]   ", log.Ltime|log.Lmsgprefix)
	logger.Printf("connecting to cid=%d port=%d", *cid, *port)

	conn, err := Dial(uint32(*cid), uint32(*port))
	if err != nil {
		logger.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	logger.Printf("connected: local=%s remote=%s", conn.LocalAddr(), conn.RemoteAddr())

	if *stdin {
		sendFromStdin(conn, logger)
	} else {
		sendN(conn, *count, *body, *interval, logger)
	}
}

func sendN(conn *Conn, count int, body string, interval time.Duration, logger *log.Logger) {
	for i := 1; i <= count; i++ {
		if err := sendOne(conn, i, body, logger); err != nil {
			logger.Fatalf("send #%d: %v", i, err)
		}
		if i < count {
			time.Sleep(interval)
		}
	}
	logger.Printf("done — sent %d messages", count)
}

func sendFromStdin(conn *Conn, logger *log.Logger) {
	sc := bufio.NewScanner(os.Stdin)
	seq := 1
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if err := sendOne(conn, seq, line, logger); err != nil {
			logger.Fatalf("send #%d: %v", seq, err)
		}
		seq++
	}
	if err := sc.Err(); err != nil {
		logger.Fatalf("stdin: %v", err)
	}
	logger.Printf("done — sent %d messages", seq-1)
}

func sendOne(conn *Conn, seq int, body string, logger *log.Logger) error {
	msg := Message{Seq: seq, Timestamp: time.Now().UTC(), Body: body}
	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if err := WriteMsg(conn, raw); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	ackRaw, err := ReadMsg(conn)
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}

	var ack Ack
	if err := json.Unmarshal(ackRaw, &ack); err != nil {
		return fmt.Errorf("decode ack: %w", err)
	}
	if ack.Ack != seq {
		return fmt.Errorf("unexpected ack: got %d want %d", ack.Ack, seq)
	}

	logger.Printf("sent #%d  ack=%d  body=%q", seq, ack.Ack, body)
	return nil
}
