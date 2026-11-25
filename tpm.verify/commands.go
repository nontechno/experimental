package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	_ "crypto/sha1"
	_ "crypto/sha256"
	_ "crypto/sha512"
	"errors"
	"fmt"
	"math/big"
)

var hashes = []struct {
	Alg  uint16
	Name string
	H    crypto.Hash
}{
	{algSHA1, "sha1", crypto.SHA1},
	{algSHA256, "sha256", crypto.SHA256},
	{algSHA384, "sha384", crypto.SHA384},
	{algSHA512, "sha512", crypto.SHA512},
}

func hashFor(alg uint16) (crypto.Hash, string, bool) {
	for _, h := range hashes {
		if h.Alg == alg {
			return h.H, h.Name, true
		}
	}
	return 0, fmt.Sprintf("alg_0x%04x", alg), false
}

func digest(h crypto.Hash, parts ...[]byte) []byte {
	d := h.New()
	for _, p := range parts {
		d.Write(p)
	}
	return d.Sum(nil)
}

// ---- capabilities ----

const (
	capAlgs     = 0
	capHandles  = 1
	capCommands = 2
	capPCRs     = 5
	capProps    = 6
	capCurves   = 8
)

// getCap pages through one capability. each consumes one entry and returns its key.
func (t *TPM) getCap(cap, first, limit uint32, each func(*rd) uint32) error {
	next := first
	for page := 0; page < 64; page++ {
		var p buf
		p.u32(cap)
		p.u32(next)
		p.u32(limit)
		_, r, err := t.run(ccGetCapability, nil, nil, p, 0)
		if err != nil {
			return err
		}
		more := r.u8()
		r.u32() // capability echoed back
		n := r.count()
		var last uint32
		for i := 0; i < n && r.err == nil; i++ {
			last = each(r)
		}
		if r.err != nil {
			return r.err
		}
		if more == 0 || n == 0 {
			return nil
		}
		next = last + 1
	}
	return errors.New("capability list did not terminate")
}

type PCRSel struct {
	Alg  uint16
	PCRs []int
}

func (b *buf) pcrSelection(sels ...PCRSel) {
	b.u32(uint32(len(sels)))
	for _, s := range sels {
		b.u16(s.Alg)
		b.u8(3)
		var m [3]byte
		for _, i := range s.PCRs {
			m[i/8] |= 1 << (i % 8)
		}
		b.raw(m[:])
	}
}

func (r *rd) pcrSelection() []PCRSel {
	var out []PCRSel
	for n := r.count(); n > 0 && r.err == nil; n-- {
		s := PCRSel{Alg: r.u16()}
		for i, v := range r.take(int(r.u8())) {
			for bit := 0; bit < 8; bit++ {
				if v&(1<<bit) != 0 {
					s.PCRs = append(s.PCRs, i*8+bit)
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// ReadPCRs reads the requested PCRs of one bank (the TPM returns at most 8 per call).
func (t *TPM) ReadPCRs(alg uint16, pcrs []int) (map[int][]byte, error) {
	out := map[int][]byte{}
	want := append([]int(nil), pcrs...)
	for len(want) > 0 {
		var p buf
		p.pcrSelection(PCRSel{alg, want})
		_, r, err := t.run(ccPCRRead, nil, nil, p, 0)
		if err != nil {
			return nil, err
		}
		r.u32() // update counter
		sel := r.pcrSelection()
		n := r.count()
		if len(sel) != 1 || n == 0 || n != len(sel[0].PCRs) {
			return nil, fmt.Errorf("PCR_Read returned no data for PCRs %v", want)
		}
		for _, i := range sel[0].PCRs {
			out[i] = append([]byte(nil), r.tpm2b()...)
		}
		if r.err != nil {
			return nil, r.err
		}
		var rest []int
		for _, i := range want {
			if _, ok := out[i]; !ok {
				rest = append(rest, i)
			}
		}
		want = rest
	}
	return out, nil
}

func (t *TPM) PCRExtend(pcr int, alg uint16, d []byte) error {
	var p buf
	p.u32(1)
	p.u16(alg)
	p.raw(d)
	_, _, err := t.run(ccPCRExtend, []uint32{uint32(pcr)}, []auth{pw}, p, 0)
	return err
}

func (t *TPM) PCRReset(pcr int) error {
	_, _, err := t.run(ccPCRReset, []uint32{uint32(pcr)}, []auth{pw}, nil, 0)
	return err
}

func (t *TPM) GetRandom(n int) ([]byte, error) {
	var out []byte
	for calls := 0; len(out) < n; calls++ {
		var p buf
		p.u16(uint16(min(n-len(out), 32)))
		_, r, err := t.run(ccGetRandom, nil, nil, p, 0)
		if err != nil {
			return nil, err
		}
		v := r.tpm2b()
		if len(v) == 0 || calls > n {
			return nil, errors.New("GetRandom returned no data")
		}
		out = append(out, v...)
	}
	return out[:n], nil
}

// ---- objects ----

const (
	attrFixedTPM      = 1 << 1
	attrFixedParent   = 1 << 4
	attrSensitiveOrig = 1 << 5
	attrUserWithAuth  = 1 << 6
	attrAdminPolicy   = 1 << 7
	attrNoDA          = 1 << 10
	attrRestricted    = 1 << 16
	attrDecrypt       = 1 << 17
	attrSign          = 1 << 18

	baseAttrs = attrFixedTPM | attrFixedParent | attrSensitiveOrig | attrUserWithAuth | attrNoDA
)

type Template struct {
	Type      uint16
	Attrs     uint32
	Policy    []byte
	Symmetric bool // AES-128-CFB (required for storage keys)
	Scheme    uint16
	RSABits   uint16
	Curve     uint16
	Unique    int // length of zero-filled unique field (EK templates)
}

func (tm Template) marshal() []byte {
	var b buf
	b.u16(tm.Type)
	b.u16(algSHA256)
	b.u32(tm.Attrs)
	b.tpm2b(tm.Policy)
	if tm.Type == algKeyedHash {
		b.u16(algNull)
		b.tpm2b(nil)
		return b
	}
	if tm.Symmetric {
		b.u16(algAES)
		b.u16(128)
		b.u16(algCFB)
	} else {
		b.u16(algNull)
	}
	if tm.Scheme == 0 {
		b.u16(algNull)
	} else {
		b.u16(tm.Scheme)
		b.u16(algSHA256)
	}
	if tm.Type == algRSA {
		b.u16(tm.RSABits)
		b.u32(0) // default exponent 65537
		b.tpm2b(make([]byte, tm.Unique))
	} else {
		b.u16(tm.Curve)
		b.u16(algNull) // KDF
		b.tpm2b(make([]byte, tm.Unique))
		b.tpm2b(make([]byte, tm.Unique))
	}
	return b
}

type Object struct {
	Handle uint32
	Public []byte // TPMT_PUBLIC
	Name   []byte
	Key    crypto.PublicKey
}

func parsePublic(pub []byte) (crypto.PublicKey, error) {
	r := &rd{b: pub}
	typ := r.u16()
	r.u16()
	r.u32()
	r.tpm2b()
	if typ != algRSA && typ != algECC {
		return nil, r.err
	}
	if r.u16() != algNull {
		r.u16()
		r.u16()
	}
	if r.u16() != algNull {
		r.u16()
	}
	if typ == algRSA {
		r.u16()
		e := r.u32()
		if e == 0 {
			e = 65537
		}
		n := r.tpm2b()
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(e)}, r.err
	}
	curve := r.u16()
	if r.u16() != algNull {
		r.u16()
	}
	x, y := r.tpm2b(), r.tpm2b()
	c := map[uint16]elliptic.Curve{curveP256: elliptic.P256(), curveP384: elliptic.P384()}[curve]
	if c == nil {
		return nil, fmt.Errorf("unsupported curve 0x%x", curve)
	}
	return &ecdsa.PublicKey{Curve: c, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}, r.err
}

func createParams(data, template []byte) []byte {
	var s, p buf
	s.tpm2b(nil) // userAuth
	s.tpm2b(data)
	p.tpm2b(s)
	p.tpm2b(template)
	p.tpm2b(nil) // outsideInfo
	p.u32(0)     // creationPCR
	return p
}

func (t *TPM) CreatePrimary(hierarchy uint32, tm Template) (*Object, error) {
	h, r, err := t.run(ccCreatePrimary, []uint32{hierarchy}, []auth{pw}, createParams(nil, tm.marshal()), 1)
	if err != nil {
		return nil, err
	}
	o := &Object{Handle: h[0], Public: append([]byte(nil), r.tpm2b()...)}
	r.tpm2b() // creationData
	r.tpm2b() // creationHash
	r.u16()   // ticket tag
	r.u32()
	r.tpm2b()
	o.Name = append([]byte(nil), r.tpm2b()...)
	if r.err == nil {
		o.Key, err = parsePublic(o.Public)
	} else {
		err = r.err
	}
	if err != nil {
		t.Flush(o.Handle)
		return nil, err
	}
	return o, nil
}

// CreateAndLoad makes a child of parent and loads it.
func (t *TPM) CreateAndLoad(parent uint32, data []byte, tm Template) (*Object, error) {
	_, r, err := t.run(ccCreate, []uint32{parent}, []auth{pw}, createParams(data, tm.marshal()), 0)
	if err != nil {
		return nil, err
	}
	priv, pub := r.tpm2b(), r.tpm2b()
	if r.err != nil {
		return nil, r.err
	}
	var p buf
	p.tpm2b(priv)
	p.tpm2b(pub)
	h, lr, err := t.run(ccLoad, []uint32{parent}, []auth{pw}, p, 1)
	if err != nil {
		return nil, err
	}
	o := &Object{Handle: h[0], Public: append([]byte(nil), pub...), Name: append([]byte(nil), lr.tpm2b()...)}
	if o.Key, err = parsePublic(o.Public); err != nil {
		t.Flush(o.Handle)
		return nil, err
	}
	return o, nil
}

// ---- signatures ----

type Signature struct {
	Raw  []byte // TPMT_SIGNATURE as returned by the TPM
	Alg  uint16
	Hash uint16
	RSA  []byte
	R, S []byte
}

func (r *rd) signature() Signature {
	start := r.b
	s := Signature{Alg: r.u16(), Hash: r.u16()}
	if s.Alg == algECDSA {
		s.R, s.S = r.tpm2b(), r.tpm2b()
	} else {
		s.RSA = r.tpm2b()
	}
	if r.err == nil {
		s.Raw = append([]byte(nil), start[:len(start)-len(r.b)]...)
	}
	return s
}

// Verify checks a SHA-256 signature in software, independently of the TPM.
func (s Signature) Verify(key crypto.PublicKey, d []byte) bool {
	switch k := key.(type) {
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, d, s.RSA) == nil
	case *ecdsa.PublicKey:
		return ecdsa.Verify(k, d, new(big.Int).SetBytes(s.R), new(big.Int).SetBytes(s.S))
	}
	return false
}

func (t *TPM) Sign(key uint32, scheme uint16, d []byte) (Signature, error) {
	var p buf
	p.tpm2b(d)
	p.u16(scheme)
	p.u16(algSHA256)
	p.u16(0x8024) // TPM_ST_HASHCHECK null ticket
	p.u32(rhNull)
	p.tpm2b(nil)
	_, r, err := t.run(ccSign, []uint32{key}, []auth{pw}, p, 0)
	if err != nil {
		return Signature{}, err
	}
	s := r.signature()
	return s, r.err
}

func (t *TPM) VerifySignature(key uint32, d []byte, s Signature) error {
	var p buf
	p.tpm2b(d)
	p.raw(s.Raw)
	_, _, err := t.run(ccVerifySignature, []uint32{key}, nil, p, 0)
	return err
}

func (t *TPM) Quote(key uint32, nonce []byte, sel PCRSel) ([]byte, Signature, error) {
	var p buf
	p.tpm2b(nonce)
	p.u16(algNull) // use the key's scheme
	p.pcrSelection(sel)
	_, r, err := t.run(ccQuote, []uint32{key}, []auth{pw}, p, 0)
	if err != nil {
		return nil, Signature{}, err
	}
	att := append([]byte(nil), r.tpm2b()...)
	s := r.signature()
	return att, s, r.err
}

// ---- sessions ----

func (t *TPM) StartSession(typ uint8) (uint32, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return 0, err
	}
	var p buf
	p.tpm2b(nonce)
	p.tpm2b(nil) // salt
	p.u8(typ)    // 1 = policy, 3 = trial
	p.u16(algNull)
	p.u16(algSHA256)
	h, _, err := t.run(ccStartAuthSession, []uint32{rhNull, rhNull}, nil, p, 1)
	if err != nil {
		return 0, err
	}
	return h[0], nil
}

func (t *TPM) PolicyPCR(session uint32, sel PCRSel) error {
	var p buf
	p.tpm2b(nil)
	p.pcrSelection(sel)
	_, _, err := t.run(ccPolicyPCR, []uint32{session}, nil, p, 0)
	return err
}

func (t *TPM) PolicyDigest(session uint32) ([]byte, error) {
	_, r, err := t.run(ccPolicyGetDigest, []uint32{session}, nil, nil, 0)
	if err != nil {
		return nil, err
	}
	v := r.tpm2b()
	return append([]byte(nil), v...), r.err
}

// Unseal uses a policy session; continueSession is clear so the TPM flushes it on success.
func (t *TPM) Unseal(item, session uint32) ([]byte, error) {
	_, r, err := t.run(ccUnseal, []uint32{item}, []auth{{handle: session}}, nil, 0)
	if err != nil {
		return nil, err
	}
	v := r.tpm2b()
	return append([]byte(nil), v...), r.err
}
