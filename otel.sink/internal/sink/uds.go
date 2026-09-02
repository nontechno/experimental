package sink

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// UDS socket modes.
const (
	// UDSListen creates the socket and streams to whoever connects. Several
	// readers can attach at once; with none attached, records are discarded.
	UDSListen = "listen"
	// UDSDial connects to a socket someone else created, reconnecting if
	// that peer restarts.
	UDSDial = "dial"
)

// UDS streams records over a Unix domain socket as newline-delimited JSON.
// A stream needs framing that survives a reader attaching mid-flight, so the
// wire format is always one JSON object per line regardless of the file
// sink's format.
//
// Writes never block the export path. Records are queued to a bounded buffer
// and written by a background goroutine; when the buffer is full — no reader
// attached, or a reader that cannot keep up — batches are dropped and
// counted. A debugging tap must not be able to stall the service being
// debugged.
type UDS struct {
	path     string
	mode     string
	envelope bool

	frames  chan []byte
	done    chan struct{}
	wg      sync.WaitGroup
	stop    sync.Once
	dropped atomic.Uint64
	sent    atomic.Uint64

	mu    sync.Mutex
	ln    net.Listener
	conns map[net.Conn]struct{}
}

// frame is the envelope written for each record, so that one socket can
// carry all three signals unambiguously.
type frame struct {
	Signal string `json:"signal"`
	Record any    `json:"record"`
}

// NewUDS opens the socket and starts the writer.
//
// In listen mode a stale socket file left by a previous run is removed
// first, which is the usual cause of "address already in use" on a path that
// nothing is actually using.
func NewUDS(path, mode string, envelope bool, buffer int) (*UDS, error) {
	if path == "" {
		return nil, errors.New("uds.path is empty")
	}
	if buffer <= 0 {
		buffer = 1024
	}

	u := &UDS{
		path:     path,
		mode:     mode,
		envelope: envelope,
		frames:   make(chan []byte, buffer),
		done:     make(chan struct{}),
		conns:    map[net.Conn]struct{}{},
	}

	switch mode {
	case UDSListen:
		if err := removeStaleSocket(path); err != nil {
			return nil, err
		}
		ln, err := net.Listen("unix", path)
		if err != nil {
			return nil, fmt.Errorf("listen %s: %w", path, err)
		}
		// Readable by the owner's group, e.g. a sidecar running as the same
		// group. Not world-writable.
		if err := os.Chmod(path, 0o660); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("chmod %s: %w", path, err)
		}
		u.ln = ln
		u.wg.Add(1)
		go u.accept()

	case UDSDial:
		conn, err := net.Dial("unix", path)
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", path, err)
		}
		u.conns[conn] = struct{}{}

	default:
		return nil, fmt.Errorf("uds mode %q: want %s or %s", mode, UDSListen, UDSDial)
	}

	u.wg.Add(1)
	go u.write()
	return u, nil
}

// Name implements Sink.
func (u *UDS) Name() string { return "uds(" + u.path + ")" }

// Spans implements Sink.
func (u *UDS) Spans(records []model.Span) { u.enqueue("traces", records) }

// Metrics implements Sink.
func (u *UDS) Metrics(records []model.Metric) { u.enqueue("metrics", records) }

// Logs implements Sink.
func (u *UDS) Logs(records []model.Log) { u.enqueue("logs", records) }

// Stats reports batches written and batches dropped for lack of a reader.
func (u *UDS) Stats() (sent, dropped uint64) {
	return u.sent.Load(), u.dropped.Load()
}

// enqueue encodes a whole batch into one buffer so it reaches the socket in
// a single write, then hands it off without blocking.
func (u *UDS) enqueue(signal string, records any) {
	payload, err := u.encode(signal, records)
	if err != nil {
		fmt.Fprintf(os.Stderr, "otel.sink: uds encode %s: %v\n", signal, err)
		return
	}
	if len(payload) == 0 {
		return
	}
	select {
	case u.frames <- payload:
	default:
		// Buffer full: no reader, or a reader falling behind.
		if n := u.dropped.Add(1); n == 1 || n%1000 == 0 {
			log.Printf("uds %s: dropped %d batches (no reader, or reader too slow)", u.path, n)
		}
	}
}

func (u *UDS) encode(signal string, records any) ([]byte, error) {
	var buf []byte
	appendRecord := func(rec any) error {
		var (
			b   []byte
			err error
		)
		if u.envelope {
			b, err = json.Marshal(frame{Signal: signal, Record: rec})
		} else {
			b, err = json.Marshal(rec)
		}
		if err != nil {
			return err
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
		return nil
	}

	switch typed := records.(type) {
	case []model.Span:
		for i := range typed {
			if err := appendRecord(typed[i]); err != nil {
				return nil, err
			}
		}
	case []model.Metric:
		for i := range typed {
			if err := appendRecord(typed[i]); err != nil {
				return nil, err
			}
		}
	case []model.Log:
		for i := range typed {
			if err := appendRecord(typed[i]); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported record type %T", records)
	}
	return buf, nil
}

// accept adds each new reader to the broadcast set.
func (u *UDS) accept() {
	defer u.wg.Done()
	for {
		conn, err := u.ln.Accept()
		if err != nil {
			select {
			case <-u.done:
				return // shutting down
			default:
			}
			// A transient accept error should not kill the sink.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		u.mu.Lock()
		u.conns[conn] = struct{}{}
		u.mu.Unlock()
		log.Printf("uds %s: reader attached", u.path)
	}
}

// write drains the queue to every attached connection.
func (u *UDS) write() {
	defer u.wg.Done()
	for {
		select {
		case <-u.done:
			return
		case payload := <-u.frames:
			u.broadcast(payload)
		}
	}
}

func (u *UDS) broadcast(payload []byte) {
	u.mu.Lock()
	targets := make([]net.Conn, 0, len(u.conns))
	for conn := range u.conns {
		targets = append(targets, conn)
	}
	u.mu.Unlock()

	if len(targets) == 0 {
		if u.mode == UDSDial {
			u.redial()
		}
		u.dropped.Add(1)
		return
	}

	for _, conn := range targets {
		// A reader that stops consuming must not wedge the writer.
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write(payload); err != nil {
			log.Printf("uds %s: reader detached (%v)", u.path, err)
			u.mu.Lock()
			delete(u.conns, conn)
			u.mu.Unlock()
			_ = conn.Close()
			continue
		}
		u.sent.Add(1)
	}
}

// redial reconnects in dial mode after the peer went away. One attempt per
// batch, so a peer that stays down costs a syscall rather than a spin.
func (u *UDS) redial() {
	conn, err := net.Dial("unix", u.path)
	if err != nil {
		return
	}
	log.Printf("uds %s: reconnected", u.path)
	u.mu.Lock()
	u.conns[conn] = struct{}{}
	u.mu.Unlock()
}

// Close stops the writer, drops the connections and removes the socket file
// in listen mode. Safe to call twice.
func (u *UDS) Close() error {
	var err error
	u.stop.Do(func() {
		close(u.done)
		if u.ln != nil {
			err = u.ln.Close() // unblocks accept
		}
		u.wg.Wait()

		u.mu.Lock()
		for conn := range u.conns {
			_ = conn.Close()
			delete(u.conns, conn)
		}
		u.mu.Unlock()

		if u.mode == UDSListen {
			// net removes the socket file on listener close in most cases;
			// this covers the rest.
			if rmErr := os.Remove(u.path); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
				err = rmErr
			}
		}
		if dropped := u.dropped.Load(); dropped > 0 {
			log.Printf("uds %s: %d batches dropped over the run", u.path, dropped)
		}
	})
	return err
}

// removeStaleSocket deletes a leftover socket file, and only a socket file:
// a regular file at that path is a configuration mistake, not garbage to
// clean up.
func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	return nil
}
