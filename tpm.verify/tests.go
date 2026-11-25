// Functional tests. Nothing here clears the TPM, changes hierarchy auth or
// persists objects. Only -nv-write touches non-volatile storage.
package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"time"
)

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// expectRejected passes only when the TPM itself refuses with one of the wanted codes.
func expectRejected(r *Report, name string, err error, want ...uint32) {
	var te *TPMError
	switch {
	case err == nil:
		r.Add(name, Fail, "operation succeeded but must be refused")
	case errors.As(err, &te) && slices.Contains(want, te.Base()):
		r.Add(name, Pass, "refused as expected: %v", te)
	default:
		r.Add(name, Fail, "refused for an unexpected reason: %v", err)
	}
}

func basicTests(r *Report, t *TPM, c *Caps) {
	r.Section("Self test")
	r.Doing("TPM2_SelfTest(fullTest=yes), this can take a while")
	start := time.Now()
	_, _, err := t.run(ccSelfTest, nil, nil, []byte{1}, 0)
	r.Check("Full self test", err, fmt.Sprintf("completed in %v", time.Since(start).Round(time.Millisecond)))
	_, resp, err := t.run(ccGetTestResult, nil, nil, nil, 0)
	if err == nil {
		data := resp.tpm2b()
		if rc := resp.u32(); resp.err != nil {
			err = resp.err
		} else if rc != 0 {
			err = fmt.Errorf("test result 0x%X, manufacturer data %x", rc, data)
		}
	}
	r.Check("Self test result", err, "TPM reports success")

	r.Section("Random number generator")
	r.Doing("drawing 4096 bytes with TPM2_GetRandom")
	data, err := t.GetRandom(4096)
	if r.Check("GetRandom", err, "received 4096 bytes") {
		seen := map[string]bool{}
		dup := 0
		ones := 0
		var freq [256]int
		for _, b := range data {
			ones += bits.OnesCount8(b)
			freq[b]++
		}
		for i := 0; i+16 <= len(data); i += 16 {
			if seen[string(data[i:i+16])] {
				dup++
			}
			seen[string(data[i:i+16])] = true
		}
		var chi float64
		for _, f := range freq {
			chi += math.Pow(float64(f)-16, 2) / 16
		}
		// Deliberately wide bounds (about 6 sigma): this detects a broken source,
		// it is not a statistical certification.
		z := math.Abs(float64(ones)-16384) / math.Sqrt(8192)
		if dup != 0 || z > 6 || chi > 400 || chi < 140 {
			r.Add("RNG sanity", Fail, "duplicate blocks=%d monobit z=%.2f chi-square=%.1f", dup, z, chi)
		} else {
			r.Add("RNG sanity", Pass, "no repeated 16-byte blocks, monobit z=%.2f, chi-square=%.1f (255 df). Smoke test only", z, chi)
		}
	}
	var p buf
	p.tpm2b(random(32))
	_, _, err = t.run(ccStirRandom, nil, nil, p, 0)
	r.Check("StirRandom", err, "accepted additional entropy")

	r.Section("Clock")
	read := func() (uint64, uint64, uint32, uint32, uint8, error) {
		_, x, err := t.run(ccReadClock, nil, nil, nil, 0)
		if err != nil {
			return 0, 0, 0, 0, 0, err
		}
		a, b, cnt, rs, safe := x.u64(), x.u64(), x.u32(), x.u32(), x.u8()
		return a, b, cnt, rs, safe, x.err
	}
	r.Doing("reading the clock twice, 1s apart")
	t1, c1, resets, restarts, safe, err := read()
	if err == nil {
		time.Sleep(time.Second)
		var t2, c2 uint64
		t2, c2, _, _, _, err = read()
		if err == nil && (t2 <= t1 || c2 <= c1) {
			err = fmt.Errorf("clock did not advance (time %d->%d, clock %d->%d)", t1, t2, c1, c2)
		}
		if err == nil {
			r.Add("Clock advances", Pass, "time +%d ms, clock +%d ms; resetCount=%d restartCount=%d safe=%d", t2-t1, c2-c1, resets, restarts, safe)
		}
	}
	if err != nil {
		r.Add("Clock advances", Fail, "%v", err)
	}

	r.Section("Hashing")
	msg := random(777)
	for _, h := range hashes {
		if !c.Algs[h.Alg] {
			r.Add("Hash "+h.Name, Skip, "not implemented by this TPM")
			continue
		}
		r.Doing("TPM2_Hash %s compared with software", h.Name)
		var p buf
		p.tpm2b(msg)
		p.u16(h.Alg)
		p.u32(rhNull)
		_, x, err := t.run(ccHash, nil, nil, p, 0)
		if err == nil {
			if got := x.tpm2b(); !bytes.Equal(got, digest(h.H, msg)) {
				err = errors.New("TPM digest differs from software digest")
			}
		}
		r.Check("Hash "+h.Name, err, "matches software")
	}

	r.Section("Supported key parameters")
	r.Doing("TPM2_TestParms for RSA and AES sizes")
	var rows [][]string
	probe := func(name string, params []byte) {
		_, _, err := t.run(ccTestParms, nil, nil, params, 0)
		v := "supported"
		if err != nil {
			v = "not supported: " + err.Error()
		}
		rows = append(rows, []string{name, v})
	}
	for _, n := range []uint16{1024, 2048, 3072, 4096} {
		var p buf
		p.u16(algRSA)
		p.u16(algNull)
		p.u16(algNull)
		p.u16(n)
		p.u32(0)
		probe(fmt.Sprintf("RSA-%d", n), p)
	}
	for _, n := range []uint16{128, 192, 256} {
		var p buf
		p.u16(0x25) // SYMCIPHER
		p.u16(algAES)
		p.u16(n)
		p.u16(algCFB)
		probe(fmt.Sprintf("AES-%d-CFB", n), p)
	}
	r.Table("Key parameter support", []string{"Parameters", "Result"}, rows)
	r.Add("TestParms", Info, "see the Key parameter support table")
}

// pcrDump reads and records every active bank.
func pcrDump(r *Report, t *TPM, c *Caps) {
	r.Section("PCR values")
	for _, b := range c.Banks {
		_, name, _ := hashFor(b.Alg)
		r.Doing("reading %s bank", name)
		vals, err := t.ReadPCRs(b.Alg, b.PCRs)
		if !r.Check("Read "+name+" bank", err, fmt.Sprintf("%d PCRs", len(vals))) {
			continue
		}
		var rows [][]string
		var empty []int
		for _, i := range b.PCRs {
			v := vals[i]
			rows = append(rows, []string{fmt.Sprint(i), pcrUse(i), hex.EncodeToString(v)})
			if i <= 7 && (bytes.Count(v, []byte{0}) == len(v) || bytes.Count(v, []byte{0xff}) == len(v)) {
				empty = append(empty, i)
			}
		}
		r.Table("PCR bank "+name, []string{"PCR", "Typical use", "Value"}, rows)
		if len(empty) > 0 {
			r.Add("Firmware measurements "+name, Warn, "PCRs %v are all-zero/all-one: firmware did not measure into this bank", empty)
		} else {
			r.Add("Firmware measurements "+name, Pass, "PCRs 0-7 have been extended")
		}
	}
}

func pcrUse(i int) string {
	u := map[int]string{0: "firmware code", 1: "firmware config", 2: "option ROM code", 3: "option ROM config",
		4: "boot loader", 5: "boot config / GPT", 6: "platform specific", 7: "Secure Boot policy",
		8: "GRUB commands", 9: "files read by boot loader", 10: "Linux IMA", 14: "shim MOK", 16: "debug (resettable)", 23: "application (resettable)"}
	return u[i]
}

func pcrOps(r *Report, t *TPM, c *Caps) {
	r.Section("PCR extend and reset")
	if !slices.Contains(c.Bank(algSHA256), 16) {
		r.Add("PCR 16 tests", Skip, "no SHA-256 PCR 16")
		return
	}
	r.Doing("resetting, extending and checking debug PCR 16")
	if !r.Check("Reset PCR 16", t.PCRReset(16), "reset") {
		return
	}
	defer func() { _ = t.PCRReset(16) }()
	before, err := t.ReadPCRs(algSHA256, []int{16})
	if err != nil {
		r.Add("Read PCR 16", Fail, "%v", err)
		return
	}
	if !bytes.Equal(before[16], make([]byte, 32)) {
		r.Add("PCR 16 after reset", Fail, "not zero: %x", before[16])
	} else {
		r.Add("PCR 16 after reset", Pass, "all zero")
	}
	d := random(32)
	err = t.PCRExtend(16, algSHA256, d)
	if err == nil {
		var after map[int][]byte
		after, err = t.ReadPCRs(algSHA256, []int{16})
		if err == nil && !bytes.Equal(after[16], digest(crypto.SHA256, before[16], d)) {
			err = errors.New("PCR value is not SHA256(old || data)")
		}
	}
	r.Check("Extend arithmetic", err, "new value equals SHA256(old || data)")
	expectRejected(r, "PCR 0 cannot be reset", t.PCRReset(0), 0x907)
}

// lockoutUnchanged confirms the negative tests did not count as authorization failures.
func lockoutUnchanged(r *Report, t *TPM, before uint32) {
	r.Section("Side effects")
	after := before
	var q buf
	q.u32(capProps)
	q.u32(0x20E)
	q.u32(1)
	_, x, err := t.run(ccGetCapability, nil, nil, q, 0)
	if err == nil {
		x.u8()
		x.u32()
		n, k, v := x.u32(), x.u32(), x.u32()
		if x.err != nil || n != 1 || k != 0x20E {
			err = errors.New("could not re-read the lockout counter")
		} else {
			after = v
		}
	}
	if err == nil && after > before {
		err = fmt.Errorf("lockout counter rose from %d to %d", before, after)
	}
	r.Check("Dictionary attack counter", err, fmt.Sprintf("unchanged by the tests (%d)", after))
}

func srkTemplate() Template {
	return Template{Type: algECC, Curve: curveP256, Symmetric: true, Attrs: baseAttrs | attrRestricted | attrDecrypt}
}

func signingKey(r *Report, t *TPM, name string, tm Template, scheme uint16) {
	r.Doing("creating transient %s key, signing, verifying", name)
	o, err := t.CreatePrimary(rhOwner, tm)
	if err != nil {
		r.Add(name+" key generation", Fail, "%v", err)
		return
	}
	defer t.Flush(o.Handle)
	r.Add(name+" key generation", Pass, "transient key created")
	d := digest(crypto.SHA256, random(100))
	sig, err := t.Sign(o.Handle, scheme, d)
	if !r.Check(name+" sign", err, "signature produced") {
		return
	}
	if sig.Verify(o.Key, d) {
		r.Add(name+" verify (software)", Pass, "signature valid outside the TPM")
	} else {
		r.Add(name+" verify (software)", Fail, "signature invalid")
	}
	r.Check(name+" verify (TPM)", t.VerifySignature(o.Handle, d, sig), "TPM accepts its signature")
	bad := slices.Clone(d)
	bad[0] ^= 1
	expectRejected(r, name+" rejects altered digest", t.VerifySignature(o.Handle, bad, sig), 0x9B)
}

func cryptoTests(r *Report, t *TPM, c *Caps) {
	r.Section("Asymmetric keys")
	if c.needAuth(r, "Key tests", true, false) {
		return
	}
	if c.Algs[algRSA] {
		signingKey(r, t, "RSA-2048", Template{Type: algRSA, RSABits: 2048, Scheme: algRSASSA, Attrs: baseAttrs | attrSign}, algRSASSA)

		r.Doing("RSA-OAEP: encrypt in software, decrypt in TPM")
		o, err := t.CreatePrimary(rhOwner, Template{Type: algRSA, RSABits: 2048, Attrs: baseAttrs | attrDecrypt})
		if err == nil {
			defer t.Flush(o.Handle)
			secret := random(32)
			var ct []byte
			ct, err = rsa.EncryptOAEP(crypto.SHA256.New(), rand.Reader, o.Key.(*rsa.PublicKey), secret, nil)
			if err == nil {
				var p buf
				p.tpm2b(ct)
				p.u16(algOAEP)
				p.u16(algSHA256)
				p.tpm2b(nil)
				var x *rd
				_, x, err = t.run(ccRSADecrypt, []uint32{o.Handle}, []auth{pw}, p, 0)
				if err == nil && !bytes.Equal(x.tpm2b(), secret) {
					err = errors.New("decrypted data differs")
				}
			}
		}
		r.Check("RSA-OAEP decrypt", err, "round trip matches")
	} else {
		r.Add("RSA tests", Skip, "RSA not implemented")
	}
	for _, cv := range []struct {
		id   uint16
		name string
	}{{curveP256, "ECC P-256"}, {curveP384, "ECC P-384"}} {
		if !slices.Contains(c.Curves, cv.id) {
			r.Add(cv.name, Skip, "curve not implemented")
			continue
		}
		signingKey(r, t, cv.name, Template{Type: algECC, Curve: cv.id, Scheme: algECDSA, Attrs: baseAttrs | attrSign}, algECDSA)
	}
}

type attest struct {
	magic     uint32
	typ       uint16
	extra     []byte
	pcrSel    []PCRSel
	pcrDigest []byte
}

func parseAttest(b []byte) (attest, error) {
	r := &rd{b: b}
	a := attest{magic: r.u32(), typ: r.u16()}
	r.tpm2b() // qualifiedSigner
	a.extra = r.tpm2b()
	r.take(8 + 4 + 4 + 1) // clockInfo
	r.u64()               // firmwareVersion
	a.pcrSel = r.pcrSelection()
	a.pcrDigest = r.tpm2b()
	return a, r.err
}

// checkQuote is the verifier side: signature, structure, nonce and PCR digest.
func checkQuote(key crypto.PublicKey, att []byte, sig Signature, nonce []byte, sel PCRSel, pcrs map[int][]byte) error {
	if !sig.Verify(key, digest(crypto.SHA256, att)) {
		return errors.New("signature invalid")
	}
	a, err := parseAttest(att)
	if err != nil {
		return err
	}
	if a.magic != 0xff544347 || a.typ != 0x8018 {
		return errors.New("not a TPM-generated quote")
	}
	if !bytes.Equal(a.extra, nonce) {
		return errors.New("nonce mismatch")
	}
	if len(a.pcrSel) != 1 || a.pcrSel[0].Alg != sel.Alg || !slices.Equal(a.pcrSel[0].PCRs, sel.PCRs) {
		return errors.New("quote covers a different PCR selection")
	}
	var all []byte
	for _, i := range sel.PCRs {
		all = append(all, pcrs[i]...)
	}
	if !bytes.Equal(a.pcrDigest, digest(crypto.SHA256, all)) {
		return errors.New("PCR digest does not match PCR values read")
	}
	return nil
}

func quoteTests(r *Report, t *TPM, c *Caps) {
	r.Section("Attestation quote")
	if c.needAuth(r, "Quote", false, true) {
		return
	}
	bank := c.Bank(algSHA256)
	if len(bank) == 0 {
		r.Add("Quote", Skip, "no SHA-256 bank")
		return
	}
	sel := PCRSel{algSHA256, nil}
	for _, i := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		if slices.Contains(bank, i) {
			sel.PCRs = append(sel.PCRs, i)
		}
	}
	keys := []struct {
		name string
		tm   Template
	}{
		{"ECC", Template{Type: algECC, Curve: curveP256, Scheme: algECDSA, Attrs: baseAttrs | attrRestricted | attrSign}},
		{"RSA", Template{Type: algRSA, RSABits: 2048, Scheme: algRSASSA, Attrs: baseAttrs | attrRestricted | attrSign}},
	}
	for _, k := range keys {
		if !c.Algs[k.tm.Type] {
			r.Add(k.name+" quote", Skip, "algorithm not implemented")
			continue
		}
		r.Doing("creating restricted %s attestation key and quoting PCRs %v", k.name, sel.PCRs)
		ak, err := t.CreatePrimary(rhEndorsement, k.tm)
		if err != nil {
			r.Add(k.name+" attestation key", Fail, "%v", err)
			continue
		}
		func() {
			defer t.Flush(ak.Handle)
			nonce := random(32)
			att, sig, err := t.Quote(ak.Handle, nonce, sel)
			if err != nil {
				r.Add(k.name+" quote", Fail, "%v", err)
				return
			}
			pcrs, err := t.ReadPCRs(algSHA256, sel.PCRs)
			if err == nil {
				err = checkQuote(ak.Key, att, sig, nonce, sel, pcrs)
			}
			if !r.Check(k.name+" quote verification", err, "signature, magic, nonce, selection and PCR digest all verified in software") {
				return
			}
			if checkQuote(ak.Key, att, sig, random(32), sel, pcrs) != nil {
				r.Add(k.name+" stale nonce detected", Pass, "verifier rejects a different nonce")
			} else {
				r.Add(k.name+" stale nonce detected", Fail, "verifier accepted wrong nonce")
			}
			bad := slices.Clone(att)
			bad[len(bad)-1] ^= 1
			if checkQuote(ak.Key, bad, sig, nonce, sel, pcrs) != nil {
				r.Add(k.name+" tampered quote detected", Pass, "verifier rejects a modified quote")
			} else {
				r.Add(k.name+" tampered quote detected", Fail, "verifier accepted modified quote")
			}
		}()
	}
}

func sealTests(r *Report, t *TPM, c *Caps) {
	r.Section("Sealing to PCR policy")
	if c.needAuth(r, "Seal/unseal", true, false) {
		return
	}
	if !slices.Contains(c.Bank(algSHA256), 16) {
		r.Add("Seal/unseal", Skip, "no SHA-256 PCR 16")
		return
	}
	r.Doing("sealing a random secret to PCR 16, unsealing, changing the PCR, retrying")
	if !r.Check("Prepare PCR 16", t.PCRReset(16), "reset") {
		return
	}
	defer func() { _ = t.PCRReset(16) }()
	sel := PCRSel{algSHA256, []int{16}}

	srk, err := t.CreatePrimary(rhOwner, srkTemplate())
	if !r.Check("Storage root key", err, "transient ECC storage key created") {
		return
	}
	defer t.Flush(srk.Handle)

	trial, err := t.StartSession(3)
	if err != nil {
		r.Add("Policy digest", Fail, "%v", err)
		return
	}
	var policy []byte
	if err = t.PolicyPCR(trial, sel); err == nil {
		policy, err = t.PolicyDigest(trial)
	}
	t.Flush(trial)
	if !r.Check("Policy digest", err, fmt.Sprintf("PolicyPCR digest %x", policy)) {
		return
	}
	secret := random(32)
	obj, err := t.CreateAndLoad(srk.Handle, secret, Template{Type: algKeyedHash, Policy: policy, Attrs: attrFixedTPM | attrFixedParent | attrNoDA})
	if !r.Check("Seal", err, "secret sealed and loaded") {
		return
	}
	defer t.Flush(obj.Handle)

	unseal := func() ([]byte, error) {
		s, err := t.StartSession(1)
		if err != nil {
			return nil, fmt.Errorf("start session: %w", err)
		}
		defer t.Flush(s) // harmless if the TPM already flushed it
		if err := t.PolicyPCR(s, sel); err != nil {
			return nil, fmt.Errorf("PolicyPCR: %w", err)
		}
		return t.Unseal(obj.Handle, s)
	}
	got, err := unseal()
	if err == nil && !bytes.Equal(got, secret) {
		err = errors.New("unsealed data differs")
	}
	r.Check("Unseal with matching PCR", err, "secret recovered")

	_, _, err = t.run(ccUnseal, []uint32{obj.Handle}, []auth{pw}, nil, 0)
	// userWithAuth is clear, so password authorization must not work.
	expectRejected(r, "Unseal with password refused", err, 0x12F)

	if !r.Check("Change PCR 16", t.PCRExtend(16, algSHA256, random(32)), "extended") {
		return
	}
	_, err = unseal()
	expectRejected(r, "Unseal after PCR change refused", err, 0x9D)
}

func nvTest(r *Report, t *TPM, c *Caps, enabled bool) {
	r.Section("NV storage")
	if !enabled {
		r.Add("NV write test", Skip, "not run; pass -nv-write to define, write, read and delete a temporary NV index")
		return
	}
	if c.needAuth(r, "NV write test", true, false) {
		return
	}
	var index uint32
	for i := uint32(0x01500000); i < 0x01500100; i++ {
		if !slices.Contains(c.Handles[0x01], i) {
			index = i
			break
		}
	}
	if index == 0 {
		r.Add("NV write test", Skip, "no free index in test range")
		return
	}
	r.Doing("defining temporary NV index 0x%08X", index)
	var pub, p buf
	pub.u32(index)
	pub.u16(algSHA256)
	pub.u32(1<<1 | 1<<17 | 1<<25) // OWNERWRITE | OWNERREAD | NO_DA
	pub.tpm2b(nil)
	pub.u16(32)
	p.tpm2b(nil)
	p.tpm2b(pub)
	_, _, err := t.run(ccNVDefineSpace, []uint32{rhOwner}, []auth{pw}, p, 0)
	if !r.Check("NV define", err, fmt.Sprintf("index 0x%08X", index)) {
		return
	}
	defer func() {
		_, _, err := t.run(ccNVUndefineSpace, []uint32{rhOwner, index}, []auth{pw}, nil, 0)
		r.Check("NV delete", err, "temporary index removed")
	}()
	data := random(32)
	var w buf
	w.tpm2b(data)
	w.u16(0)
	_, _, err = t.run(ccNVWrite, []uint32{rhOwner, index}, []auth{pw}, w, 0)
	if !r.Check("NV write", err, "32 bytes") {
		return
	}
	var q buf
	q.u16(32)
	q.u16(0)
	_, x, err := t.run(ccNVRead, []uint32{rhOwner, index}, []auth{pw}, q, 0)
	if err == nil && !bytes.Equal(x.tpm2b(), data) {
		err = errors.New("read data differs")
	}
	r.Check("NV read back", err, "matches")
}
