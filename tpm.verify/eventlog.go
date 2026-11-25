// Parser and replay for the TCG PC Client boot event log.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
)

type Event struct {
	PCR     int
	Type    uint32
	Digests map[uint16][]byte
	Data    []byte
}

var evNames = map[uint32]string{
	0x1: "EV_POST_CODE", 0x3: "EV_NO_ACTION", 0x4: "EV_SEPARATOR", 0x5: "EV_ACTION", 0x6: "EV_EVENT_TAG",
	0x7: "EV_S_CRTM_CONTENTS", 0x8: "EV_S_CRTM_VERSION", 0x9: "EV_CPU_MICROCODE", 0xA: "EV_PLATFORM_CONFIG_FLAGS",
	0xB: "EV_TABLE_OF_DEVICES", 0xC: "EV_COMPACT_HASH", 0xD: "EV_IPL", 0xE: "EV_IPL_PARTITION_DATA",
	0xF: "EV_NONHOST_CODE", 0x10: "EV_NONHOST_CONFIG", 0x11: "EV_NONHOST_INFO", 0x12: "EV_OMIT_BOOT_DEVICE_EVENTS",
	0x80000001: "EV_EFI_VARIABLE_DRIVER_CONFIG", 0x80000002: "EV_EFI_VARIABLE_BOOT",
	0x80000003: "EV_EFI_BOOT_SERVICES_APPLICATION", 0x80000004: "EV_EFI_BOOT_SERVICES_DRIVER",
	0x80000005: "EV_EFI_RUNTIME_SERVICES_DRIVER", 0x80000006: "EV_EFI_GPT_EVENT", 0x80000007: "EV_EFI_ACTION",
	0x80000008: "EV_EFI_PLATFORM_FIRMWARE_BLOB", 0x80000009: "EV_EFI_HANDOFF_TABLES",
	0x8000000A: "EV_EFI_PLATFORM_FIRMWARE_BLOB2", 0x8000000B: "EV_EFI_HANDOFF_TABLES2",
	0x80000010: "EV_EFI_HCRTM_EVENT", 0x800000E0: "EV_EFI_VARIABLE_AUTHORITY",
}

var le = binary.LittleEndian

type ler struct {
	b   []byte
	bad bool
}

func (r *ler) take(n int) []byte {
	if r.bad || n < 0 || n > len(r.b) {
		r.bad = true
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}
func (r *ler) u16() uint16 {
	if v := r.take(2); v != nil {
		return le.Uint16(v)
	}
	return 0
}
func (r *ler) u32() uint32 {
	if v := r.take(4); v != nil {
		return le.Uint32(v)
	}
	return 0
}

// ParseEventLog handles the crypto-agile format (TCG_PCR_EVENT2).
func ParseEventLog(b []byte) ([]Event, error) {
	r := &ler{b: b}
	r.u32() // PCR of header event
	typ := r.u32()
	r.take(20)
	hdr := r.take(int(r.u32()))
	if r.bad || typ != 3 || !bytes.HasPrefix(hdr, []byte("Spec ID Event03\x00")) {
		return nil, errors.New("not a crypto-agile (TPM 2.0) event log")
	}
	h := &ler{b: hdr[16:]}
	h.take(8) // platform class, version, errata, uintnSize
	sizes := map[uint16]int{}
	for n := h.u32(); n > 0 && !h.bad; n-- {
		a := h.u16()
		sizes[a] = int(h.u16())
	}
	if h.bad || len(sizes) == 0 {
		return nil, errors.New("bad event log header")
	}
	var out []Event
	for len(r.b) >= 8 {
		pcr, typ := r.u32(), r.u32()
		if pcr == 0xFFFFFFFF || (pcr == 0 && typ == 0) {
			break // unused tail of the log area
		}
		e := Event{PCR: int(pcr), Type: typ, Digests: map[uint16][]byte{}}
		for n := r.u32(); n > 0 && !r.bad; n-- {
			a := r.u16()
			sz, ok := sizes[a]
			if !ok {
				return out, fmt.Errorf("event %d uses undeclared algorithm 0x%x", len(out), a)
			}
			e.Digests[a] = r.take(sz)
		}
		e.Data = r.take(int(r.u32()))
		if r.bad || pcr > 31 {
			return out, fmt.Errorf("truncated or corrupt log after %d events", len(out))
		}
		out = append(out, e)
	}
	return out, nil
}

// Replay computes the PCR values the log implies for one bank.
func Replay(events []Event, alg uint16) map[int][]byte {
	h, _, ok := hashFor(alg)
	if !ok {
		return nil
	}
	locality := byte(0)
	pcrs := map[int][]byte{}
	for _, e := range events {
		if e.Type == 3 { // EV_NO_ACTION is never extended
			if e.PCR == 0 && len(e.Data) >= 17 && bytes.HasPrefix(e.Data, []byte("StartupLocality\x00")) {
				locality = e.Data[16]
			}
			continue
		}
		d, ok := e.Digests[alg]
		if !ok {
			continue
		}
		cur := pcrs[e.PCR]
		if cur == nil {
			cur = make([]byte, h.Size())
			if e.PCR == 0 {
				cur[len(cur)-1] = locality
			}
		}
		pcrs[e.PCR] = digest(h, cur, d)
	}
	return pcrs
}

func eventLogTests(r *Report, t *TPM, c *Caps, path string) {
	r.Section("Measured boot event log")
	r.Doing("reading %s", path)
	data, err := os.ReadFile(path)
	if err != nil {
		r.Add("Event log", Skip, "cannot read: %v", err)
		return
	}
	events, err := ParseEventLog(data)
	if !r.Check("Parse event log", err, fmt.Sprintf("%d events, %d bytes", len(events), len(data))) {
		return
	}
	var rows [][]string
	separators := map[int]bool{}
	for i, e := range events {
		name := evNames[e.Type]
		if name == "" {
			name = fmt.Sprintf("0x%08X", e.Type)
		}
		if e.Type == 4 {
			separators[e.PCR] = true
		}
		d := e.Digests[algSHA256]
		rows = append(rows, []string{fmt.Sprint(i), fmt.Sprint(e.PCR), name, fmt.Sprintf("%x", d), fmt.Sprint(len(e.Data))})
	}
	r.Table("Boot event log", []string{"#", "PCR", "Type", "SHA-256 digest", "Data bytes"}, rows)

	for _, b := range c.Banks {
		_, name, ok := hashFor(b.Alg)
		if !ok {
			continue
		}
		r.Doing("replaying the log into the %s bank", name)
		want := Replay(events, b.Alg)
		if len(want) == 0 {
			r.Add("Replay "+name, Skip, "log has no %s digests", name)
			continue
		}
		var idx []int
		for i := range want {
			if slices.Contains(b.PCRs, i) {
				idx = append(idx, i)
			}
		}
		sort.Ints(idx)
		got, err := t.ReadPCRs(b.Alg, idx)
		if err != nil {
			r.Add("Replay "+name, Fail, "%v", err)
			continue
		}
		var bad, ok2 []int
		for _, i := range idx {
			if bytes.Equal(got[i], want[i]) {
				ok2 = append(ok2, i)
			} else {
				bad = append(bad, i)
			}
		}
		switch {
		case len(bad) == 0:
			r.Add("Replay "+name, Pass, "PCRs %v match the log", ok2)
		case slices.ContainsFunc(bad, func(i int) bool { return i < 8 }):
			r.Add("Replay "+name, Fail, "firmware PCRs differ from the log: %v (matching: %v)", bad, ok2)
		default:
			r.Add("Replay "+name, Warn, "PCRs %v differ (software may extend these without logging here); matching: %v", bad, ok2)
		}
	}
	var missing []int
	for i := 0; i < 8; i++ {
		if !separators[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		r.Add("Separator events", Warn, "no EV_SEPARATOR for PCRs %v", missing)
	} else {
		r.Add("Separator events", Pass, "present for PCRs 0-7")
	}
}
