package sink

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

func socketPath(t *testing.T) string {
	t.Helper()
	// Socket paths are limited to about 100 bytes, so keep them short.
	dir, err := os.MkdirTemp("", "uds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func TestUDSStreamsEnvelopedRecords(t *testing.T) {
	path := socketPath(t)
	u, err := NewUDS(path, UDSListen, true, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond) // let accept register the reader

	u.Spans([]model.Span{{Name: "GET /checkout"}, {Name: "db.query"}})
	u.Logs([]model.Log{{Body: "payment declined"}})

	scanner := bufio.NewScanner(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	want := []struct{ signal, field, value string }{
		{"traces", "name", "GET /checkout"},
		{"traces", "name", "db.query"},
		{"logs", "body", "payment declined"},
	}
	for _, w := range want {
		if !scanner.Scan() {
			t.Fatalf("expected a line for %s/%s: %v", w.signal, w.value, scanner.Err())
		}
		var got struct {
			Signal string         `json:"signal"`
			Record map[string]any `json:"record"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
			t.Fatalf("line %q: %v", scanner.Text(), err)
		}
		if got.Signal != w.signal || got.Record[w.field] != w.value {
			t.Fatalf("got signal=%s %s=%v, want signal=%s %s=%s",
				got.Signal, w.field, got.Record[w.field], w.signal, w.field, w.value)
		}
	}
}

func TestUDSWithoutEnvelopeWritesBareRecords(t *testing.T) {
	path := socketPath(t)
	u, err := NewUDS(path, UDSListen, false, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	u.Spans([]model.Span{{Name: "bare"}})

	scanner := bufio.NewScanner(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if !scanner.Scan() {
		t.Fatal(scanner.Err())
	}
	var span model.Span
	if err := json.Unmarshal(scanner.Bytes(), &span); err != nil {
		t.Fatalf("line %q: %v", scanner.Text(), err)
	}
	if span.Name != "bare" {
		t.Fatalf("got %q, want bare", span.Name)
	}
}

// With nobody reading, the sink must drop rather than block the export path.
func TestUDSDropsWhenNoReader(t *testing.T) {
	path := socketPath(t)
	u, err := NewUDS(path, UDSListen, true, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			u.Spans([]model.Span{{Name: "orphan"}})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writing with no reader blocked the caller")
	}
	time.Sleep(100 * time.Millisecond)
	if _, dropped := u.Stats(); dropped == 0 {
		t.Fatal("expected dropped batches with no reader attached")
	}
}

func TestUDSSurvivesReaderDisconnect(t *testing.T) {
	path := socketPath(t)
	u, err := NewUDS(path, UDSListen, true, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	u.Spans([]model.Span{{Name: "before"}})
	conn.Close()
	time.Sleep(50 * time.Millisecond)

	// Must not panic or block once the reader is gone.
	u.Spans([]model.Span{{Name: "after"}})

	// A new reader can attach afterwards.
	conn2, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer conn2.Close()
	time.Sleep(50 * time.Millisecond)
	u.Spans([]model.Span{{Name: "reattached"}})

	scanner := bufio.NewScanner(conn2)
	_ = conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("new reader got nothing: %v", scanner.Err())
	}
}

func TestUDSDialMode(t *testing.T) {
	path := socketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	u, err := NewUDS(path, UDSDial, true, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	var peer net.Conn
	select {
	case peer = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("dial mode never connected")
	}
	defer peer.Close()

	u.Logs([]model.Log{{Body: "dialed"}})
	scanner := bufio.NewScanner(peer)
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	if !scanner.Scan() {
		t.Fatal(scanner.Err())
	}
}

func TestUDSRejectsNonSocketPath(t *testing.T) {
	path := socketPath(t)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := NewUDS(path, UDSListen, true, 8); err == nil {
		t.Fatal("a regular file at the socket path should be an error, not silently removed")
	}
}

// A stale socket from a killed run is normal and must not block startup.
func TestUDSReplacesStaleSocket(t *testing.T) {
	path := socketPath(t)
	first, err := NewUDS(path, UDSListen, true, 8)
	if err != nil {
		t.Fatal(err)
	}
	// Leak the listener the way a SIGKILL would, leaving the file behind.
	_ = first.ln.Close()

	second, err := NewUDS(path, UDSListen, true, 8)
	if err != nil {
		t.Fatalf("stale socket should be replaced: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUDSCloseIsIdempotentAndRemovesSocket(t *testing.T) {
	path := socketPath(t)
	u, err := NewUDS(path, UDSListen, true, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if err := u.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("socket file should be removed on close")
	}
}

func TestUDSRejectsUnknownMode(t *testing.T) {
	if _, err := NewUDS(socketPath(t), "broadcast", true, 8); err == nil {
		t.Fatal("want an error for an unknown mode")
	}
}
