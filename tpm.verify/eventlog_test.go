package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"testing"
)

func put(b *bytes.Buffer, v ...any) {
	for _, x := range v {
		_ = binary.Write(b, binary.LittleEndian, x)
	}
}

// testLog builds a small crypto-agile log: StartupLocality 3, then PCRs 0-7.
func testLog() []byte {
	var spec, b bytes.Buffer
	spec.WriteString("Spec ID Event03\x00")
	put(&spec, uint32(0), uint8(0), uint8(2), uint8(0), uint8(2), uint32(1), uint16(algSHA256), uint16(32), uint8(0))
	put(&b, uint32(0), uint32(3), [20]byte{}, uint32(spec.Len()))
	b.Write(spec.Bytes())
	event := func(pcr int, typ uint32, data []byte) {
		d := sha256.Sum256(data)
		if typ == 3 {
			d = [32]byte{}
		}
		put(&b, uint32(pcr), typ, uint32(1), uint16(algSHA256), d, uint32(len(data)))
		b.Write(data)
	}
	event(0, 3, []byte("StartupLocality\x00\x03"))
	for i := 0; i < 8; i++ {
		event(i, 4, []byte{0, 0, 0, 0})
	}
	return b.Bytes()
}

func TestParseAndReplay(t *testing.T) {
	ev, err := ParseEventLog(testLog())
	if err != nil || len(ev) != 9 {
		t.Fatalf("events=%d err=%v", len(ev), err)
	}
	got := Replay(ev, algSHA256)
	d := sha256.Sum256([]byte{0, 0, 0, 0})
	for pcr, first := range map[int]byte{0: 3, 1: 0} {
		start := make([]byte, 32)
		start[31] = first
		want := sha256.Sum256(append(start, d[:]...))
		if !bytes.Equal(got[pcr], want[:]) {
			t.Errorf("PCR %d mismatch", pcr)
		}
	}
	if p := os.Getenv("WRITE_TEST_LOG"); p != "" {
		if err := os.WriteFile(p, testLog(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Corrupt input must produce errors or short results, never a panic.
func TestCorruptInput(t *testing.T) {
	log := testLog()
	for i := range log {
		_, _ = ParseEventLog(log[:i])
		bad := bytes.Clone(log)
		bad[i] ^= 0xff
		ev, _ := ParseEventLog(bad)
		Replay(ev, algSHA256)
	}
}

func FuzzParsers(f *testing.F) {
	f.Add(testLog())
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseEventLog(b)
		_, _ = parseAttest(b)
		_, _ = parsePublic(b)
		_, _ = parseEKCert(b)
	})
}
