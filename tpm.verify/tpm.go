// Minimal TPM 2.0 wire protocol (TCG TPM 2.0 Library, Parts 2 and 3), stdlib only.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// Command codes.
const (
	ccNVUndefineSpace    = 0x122
	ccNVDefineSpace      = 0x12A
	ccCreatePrimary      = 0x131
	ccNVWrite            = 0x137
	ccPCRReset           = 0x13D
	ccSelfTest           = 0x143
	ccStirRandom         = 0x146
	ccActivateCredential = 0x147
	ccNVRead             = 0x14E
	ccPolicySecret       = 0x151
	ccCreate             = 0x153
	ccLoad               = 0x157
	ccQuote              = 0x158
	ccRSADecrypt         = 0x159
	ccSign               = 0x15D
	ccUnseal             = 0x15E
	ccFlushContext       = 0x165
	ccNVReadPublic       = 0x169
	ccStartAuthSession   = 0x176
	ccVerifySignature    = 0x177
	ccGetCapability      = 0x17A
	ccGetRandom          = 0x17B
	ccGetTestResult      = 0x17C
	ccHash               = 0x17D
	ccPCRRead            = 0x17E
	ccPolicyPCR          = 0x17F
	ccReadClock          = 0x181
	ccPCRExtend          = 0x182
	ccPolicyGetDigest    = 0x189
	ccTestParms          = 0x18A
)

// Algorithms and well-known handles.
const (
	algRSA       = 0x0001
	algSHA1      = 0x0004
	algAES       = 0x0006
	algKeyedHash = 0x0008
	algSHA256    = 0x000B
	algSHA384    = 0x000C
	algSHA512    = 0x000D
	algNull      = 0x0010
	algRSASSA    = 0x0014
	algOAEP      = 0x0017
	algECDSA     = 0x0018
	algECC       = 0x0023
	algCFB       = 0x0043

	curveP256 = 0x0003
	curveP384 = 0x0004

	rhOwner       = 0x40000001
	rhNull        = 0x40000007
	rsPW          = 0x40000009
	rhEndorsement = 0x4000000B

	tagNoSessions = 0x8001
	tagSessions   = 0x8002
)

// ---- marshaling ----

type buf []byte

func (b *buf) u8(v uint8)     { *b = append(*b, v) }
func (b *buf) u16(v uint16)   { *b = binary.BigEndian.AppendUint16(*b, v) }
func (b *buf) u32(v uint32)   { *b = binary.BigEndian.AppendUint32(*b, v) }
func (b *buf) raw(v []byte)   { *b = append(*b, v...) }
func (b *buf) tpm2b(v []byte) { b.u16(uint16(len(v))); b.raw(v) }

// rd is a bounds-checked reader; the first short read poisons it.
type rd struct {
	b   []byte
	err error
}

func (r *rd) take(n int) []byte {
	if r.err != nil || n < 0 || n > len(r.b) {
		if r.err == nil {
			r.err = errors.New("short TPM response")
		}
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}
func (r *rd) u8() uint8 {
	if v := r.take(1); v != nil {
		return v[0]
	}
	return 0
}
func (r *rd) u16() uint16 {
	if v := r.take(2); v != nil {
		return binary.BigEndian.Uint16(v)
	}
	return 0
}
func (r *rd) u32() uint32 {
	if v := r.take(4); v != nil {
		return binary.BigEndian.Uint32(v)
	}
	return 0
}
func (r *rd) u64() uint64 {
	if v := r.take(8); v != nil {
		return binary.BigEndian.Uint64(v)
	}
	return 0
}
func (r *rd) tpm2b() []byte { return r.take(int(r.u16())) }

// count reads a list length and rejects lengths larger than the data left.
func (r *rd) count() int {
	n := int(r.u32())
	if n < 0 || n > len(r.b) {
		r.err = errors.New("implausible list length in TPM response")
		return 0
	}
	return n
}

// ---- transport ----

type TPM struct {
	rw     io.ReadWriteCloser
	stream bool // sockets may split a response; the char device must be read once
	Path   string
}

func OpenTPM(device, tcp string) (*TPM, error) {
	if tcp != "" {
		c, err := net.DialTimeout("tcp", tcp, 5*time.Second)
		if err != nil {
			return nil, err
		}
		return &TPM{rw: c, stream: true, Path: "tcp:" + tcp}, nil
	}
	paths := []string{device}
	if device == "" {
		paths = []string{"/dev/tpmrm0", "/dev/tpm0"}
	}
	var errs []error
	for _, p := range paths {
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err == nil {
			return &TPM{rw: f, Path: p}, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func (t *TPM) Close() error { return t.rw.Close() }

func (t *TPM) transceive(cmd []byte) ([]byte, error) {
	if c, ok := t.rw.(net.Conn); ok {
		_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	if _, err := t.rw.Write(cmd); err != nil {
		return nil, err
	}
	out := make([]byte, 8192)
	if !t.stream {
		n, err := t.rw.Read(out)
		if err != nil {
			return nil, err
		}
		return out[:n], nil
	}
	if _, err := io.ReadFull(t.rw, out[:10]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(out[2:6]))
	if size < 10 || size > len(out) {
		return nil, fmt.Errorf("bad response size %d", size)
	}
	_, err := io.ReadFull(t.rw, out[10:size])
	return out[:size], err
}

// TPMError is a non-zero response code.
type TPMError struct {
	CC, RC uint32
}

var rcFmt0 = map[uint32]string{
	0x100: "INITIALIZE", 0x12F: "AUTH_UNAVAILABLE", 0x101: "FAILURE", 0x120: "DISABLED", 0x143: "COMMAND_CODE",
	0x148: "NV_LOCKED", 0x149: "NV_AUTHORIZATION", 0x14A: "NV_UNINITIALIZED", 0x14B: "NV_SPACE",
	0x14C: "NV_DEFINED", 0x902: "OBJECT_MEMORY", 0x903: "SESSION_MEMORY", 0x907: "LOCALITY",
	0x921: "LOCKOUT", 0x922: "RETRY", 0x923: "NV_UNAVAILABLE", 0x90A: "TESTING",
}
var rcFmt1 = map[uint32]string{
	0x02: "ATTRIBUTES", 0x03: "HASH", 0x04: "VALUE", 0x05: "HIERARCHY", 0x07: "KEY_SIZE",
	0x0A: "MODE", 0x0B: "HANDLE", 0x0E: "AUTH_FAIL", 0x12: "SCHEME", 0x15: "SIZE",
	0x16: "SYMMETRIC", 0x1B: "SIGNATURE", 0x1D: "POLICY_FAIL", 0x1F: "INTEGRITY",
	0x22: "BAD_AUTH", 0x23: "EXPIRED", 0x26: "CURVE",
}

// Base strips the parameter/handle/session number from a format-one code.
func (e *TPMError) Base() uint32 {
	if e.RC&0x80 != 0 {
		return e.RC & 0xBF
	}
	return e.RC
}

func (e *TPMError) Error() string {
	name := "unknown"
	if e.RC&0x80 != 0 {
		if n, ok := rcFmt1[e.RC&0x3F]; ok {
			name = n
		}
	} else if n, ok := rcFmt0[e.RC]; ok {
		name = n
	} else if e.RC&0xFFFF0000 == 0x80280000 || e.RC == 0x000B0143 {
		name = "blocked by software layer"
	}
	return fmt.Sprintf("TPM_RC 0x%03X (%s) from command 0x%03X", e.RC, name, e.CC)
}

// auth is one entry of a command authorization area.
type auth struct {
	handle uint32
	attrs  uint8
}

var pw = auth{handle: rsPW}

// run sends a command. nOut is the number of handles in the response.
func (t *TPM) run(cc uint32, handles []uint32, auths []auth, params []byte, nOut int) ([]uint32, *rd, error) {
	var body buf
	for _, h := range handles {
		body.u32(h)
	}
	tag := uint16(tagNoSessions)
	if len(auths) > 0 {
		tag = tagSessions
		var a buf
		for _, s := range auths {
			a.u32(s.handle)
			a.tpm2b(nil) // nonce
			a.u8(s.attrs)
			a.tpm2b(nil) // empty password / HMAC
		}
		body.u32(uint32(len(a)))
		body.raw(a)
	}
	body.raw(params)
	var cmd buf
	cmd.u16(tag)
	cmd.u32(uint32(10 + len(body)))
	cmd.u32(cc)
	cmd.raw(body)

	for attempt := 0; ; attempt++ {
		resp, err := t.transceive(cmd)
		if err != nil {
			return nil, nil, fmt.Errorf("TPM I/O (command 0x%03X): %w", cc, err)
		}
		r := &rd{b: resp}
		rtag, _, rc := r.u16(), r.u32(), r.u32()
		if r.err != nil {
			return nil, nil, r.err
		}
		if (rc == 0x922 || rc == 0x908 || rc == 0x90A) && attempt < 20 {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if rc != 0 {
			return nil, nil, &TPMError{CC: cc, RC: rc}
		}
		var out []uint32
		for i := 0; i < nOut; i++ {
			out = append(out, r.u32())
		}
		if rtag == tagSessions {
			p := r.take(int(r.u32()))
			return out, &rd{b: p, err: r.err}, r.err
		}
		return out, r, r.err
	}
}

func (t *TPM) Flush(h uint32) {
	var p buf
	p.u32(h)
	_, _, _ = t.run(ccFlushContext, nil, nil, p, 0)
}
