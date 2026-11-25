// Endorsement key: certificate discovery, chain validation, key match and
// credential activation (proves the attestation key lives in the same TPM).
package main

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

var ekPolicy, _ = hex.DecodeString("837197674484b3f81a90cc8d46a5d724fd52d76e06520b64f2a1da1b331469aa")

const ekAttrs = attrFixedTPM | attrFixedParent | attrSensitiveOrig | attrAdminPolicy | attrRestricted | attrDecrypt

func (t *TPM) nvReadAll(index uint32, maxChunk int) ([]byte, error) {
	_, x, err := t.run(ccNVReadPublic, []uint32{index}, nil, nil, 0)
	if err != nil {
		return nil, err
	}
	pub := &rd{b: x.tpm2b()}
	pub.u32()
	pub.u16()
	attrs := pub.u32()
	pub.tpm2b()
	size := int(pub.u16())
	if pub.err != nil {
		return nil, pub.err
	}
	authHandle := index
	if attrs&(1<<18) == 0 { // no AUTHREAD
		authHandle = rhOwner
	}
	if maxChunk <= 0 || maxChunk > 1024 {
		maxChunk = 512
	}
	var out []byte
	for len(out) < size {
		var p buf
		p.u16(uint16(min(maxChunk, size-len(out))))
		p.u16(uint16(len(out)))
		_, x, err := t.run(ccNVRead, []uint32{authHandle, index}, []auth{pw}, p, 0)
		if err != nil {
			return nil, err
		}
		v := x.tpm2b()
		if len(v) == 0 {
			return nil, errors.New("empty NV read")
		}
		out = append(out, v...)
	}
	return out, nil
}

// parseEKCert tolerates trailing NV padding.
func parseEKCert(der []byte) (*x509.Certificate, error) {
	var raw asn1.RawValue
	if _, err := asn1.Unmarshal(der, &raw); err != nil {
		return nil, err
	}
	return x509.ParseCertificate(raw.FullBytes)
}

func keyAlg(c *x509.Certificate) uint16 {
	switch c.PublicKey.(type) {
	case *rsa.PublicKey:
		return algRSA
	case *ecdsa.PublicKey:
		return algECC
	}
	return 0
}

func verifyEKChain(cert *x509.Certificate, roots, inter *x509.CertPool) error {
	// EK certificates carry a critical directoryName SAN that Go does not process.
	leaf := *cert
	leaf.UnhandledCriticalExtensions = slices.DeleteFunc(slices.Clone(leaf.UnhandledCriticalExtensions),
		func(o asn1.ObjectIdentifier) bool { return o.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) })
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err
}

func loadCerts(dir string) (roots, inter *x509.CertPool, n int, err error) {
	roots, inter = x509.NewCertPool(), x509.NewCertPool()
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		return nil, nil, 0, err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var ders [][]byte
		for rest := data; ; {
			var b *pem.Block
			b, rest = pem.Decode(rest)
			if b == nil {
				break
			}
			ders = append(ders, b.Bytes)
		}
		if len(ders) == 0 {
			ders = [][]byte{data}
		}
		for _, d := range ders {
			c, err := x509.ParseCertificate(d)
			if err != nil {
				continue
			}
			// Self-signed certificates are trust anchors, the rest intermediates.
			if bytes.Equal(c.RawSubject, c.RawIssuer) && c.CheckSignatureFrom(c) == nil {
				roots.AddCert(c)
				n++
			} else {
				inter.AddCert(c)
			}
		}
	}
	if n == 0 {
		return nil, nil, 0, fmt.Errorf("no self-signed root certificates in %s", dir)
	}
	return roots, inter, n, nil
}

// kdfa is the SP800-108 counter-mode KDF used by TPM 2.0.
func kdfa(key []byte, label string, contextU []byte, bits int) []byte {
	var out []byte
	for i := uint32(1); len(out)*8 < bits; i++ {
		h := hmac.New(sha256.New, key)
		h.Write(binary.BigEndian.AppendUint32(nil, i))
		h.Write(append([]byte(label), 0))
		h.Write(contextU)
		h.Write(binary.BigEndian.AppendUint32(nil, uint32(bits)))
		out = h.Sum(out)
	}
	return out[:bits/8]
}

// makeCredential is TPM2_MakeCredential done in software for an RSA EK.
func makeCredential(ek *rsa.PublicKey, akName, credential []byte) (blob, secret []byte, err error) {
	seed := random(32)
	secret, err = rsa.EncryptOAEP(sha256.New(), rand.Reader, ek, seed, []byte("IDENTITY\x00"))
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(kdfa(seed, "STORAGE", akName, 128))
	if err != nil {
		return nil, nil, err
	}
	var plain buf
	plain.tpm2b(credential)
	enc := make([]byte, len(plain))
	cipher.NewCFBEncrypter(block, make([]byte, 16)).XORKeyStream(enc, plain)
	mac := hmac.New(sha256.New, kdfa(seed, "INTEGRITY", nil, 256))
	mac.Write(enc)
	mac.Write(akName)
	var b buf
	b.tpm2b(mac.Sum(nil))
	b.raw(enc)
	return b, secret, nil
}

func activate(t *TPM, ak, ek *Object, blob, secret []byte) ([]byte, error) {
	s, err := t.StartSession(1)
	if err != nil {
		return nil, err
	}
	defer t.Flush(s)
	var ps buf
	ps.tpm2b(nil)
	ps.tpm2b(nil)
	ps.tpm2b(nil)
	ps.u32(0)
	if _, _, err := t.run(ccPolicySecret, []uint32{rhEndorsement, s}, []auth{pw}, ps, 0); err != nil {
		return nil, fmt.Errorf("PolicySecret: %w", err)
	}
	var p buf
	p.tpm2b(blob)
	p.tpm2b(secret)
	_, x, err := t.run(ccActivateCredential, []uint32{ak.Handle, ek.Handle}, []auth{pw, {handle: s}}, p, 0)
	if err != nil {
		return nil, err
	}
	v := x.tpm2b()
	return slices.Clone(v), x.err
}

func ekTests(r *Report, t *TPM, c *Caps, caDir, certFile string) {
	r.Section("Endorsement key and device identity")
	var roots, inter *x509.CertPool
	if caDir != "" {
		var n int
		var err error
		roots, inter, n, err = loadCerts(caDir)
		r.Check("Load trusted EK CAs", err, fmt.Sprintf("%d root certificate(s)", n))
	}
	certs := map[uint16]*x509.Certificate{}
	var supplied *x509.Certificate
	if certFile != "" {
		data, err := os.ReadFile(certFile)
		if err == nil {
			if b, _ := pem.Decode(data); b != nil {
				data = b.Bytes
			}
			supplied, err = parseEKCert(data)
		}
		r.Check("Load supplied EK certificate", err, certFile)
	}
	for _, e := range []struct {
		idx  uint32
		alg  uint16
		name string
	}{{0x01C00002, algRSA, "RSA"}, {0x01C0000A, algECC, "ECC"}} {
		var der []byte
		var err error
		if supplied != nil && keyAlg(supplied) == e.alg {
			r.Doing("using the supplied %s EK certificate", e.name)
			der = supplied.Raw
		} else {
			r.Doing("looking for %s EK certificate at NV 0x%08X", e.name, e.idx)
			if !slices.Contains(c.Handles[0x01], e.idx) {
				r.Add(e.name+" EK certificate", Warn, "none at NV 0x%08X. Some firmware TPMs (including AMD fTPM) publish it online instead: try tpm2_getekcertificate, then pass -ek-cert", e.idx)
				continue
			}
			der, err = t.nvReadAll(e.idx, int(c.Props[0x12C]))
		}
		var cert *x509.Certificate
		if err == nil {
			cert, err = parseEKCert(der)
		}
		if !r.Check(e.name+" EK certificate", err, "read and parsed") {
			continue
		}
		certs[e.alg] = cert
		fp := sha256.Sum256(cert.Raw)
		r.Table(e.name+" EK certificate", []string{"Field", "Value"}, [][]string{
			{"Subject", cert.Subject.String()}, {"Issuer", cert.Issuer.String()},
			{"Serial", cert.SerialNumber.Text(16)}, {"Not before", cert.NotBefore.Format(time.RFC3339)},
			{"Not after", cert.NotAfter.Format(time.RFC3339)}, {"Signature algorithm", cert.SignatureAlgorithm.String()},
			{"SHA-256 fingerprint", hex.EncodeToString(fp[:])}, {"Issuer URLs", fmt.Sprint(cert.IssuingCertificateURL)},
		})
		if time.Now().After(cert.NotAfter) {
			r.Add(e.name+" EK certificate validity", Warn, "expired %s", cert.NotAfter.Format("2006-01-02"))
		}
		if roots == nil {
			r.Add(e.name+" EK chain", Skip, "pass -ek-ca DIR with the manufacturer root and intermediate certificates")
			continue
		}
		r.Check(e.name+" EK chain", verifyEKChain(cert, roots, inter), "chains to a supplied root (revocation not checked)")
	}

	// High-range templates (for example ECC P-384 at 0x01C00016) use other indices.
	for _, idx := range c.Handles[0x01] {
		if idx < 0x01C00000 || idx > 0x01C0FFFF || idx == 0x01C00002 || idx == 0x01C0000A {
			continue
		}
		der, err := t.nvReadAll(idx, int(c.Props[0x12C]))
		if err != nil {
			continue
		}
		cert, err := parseEKCert(der)
		if err != nil {
			continue // nonces and templates share this range
		}
		name := fmt.Sprintf("EK-range certificate at 0x%08X", idx)
		r.Add(name, Info, "%s key, issuer %s, expires %s (key match not tested)", cert.PublicKeyAlgorithm, cert.Issuer, cert.NotAfter.Format("2006-01-02"))
		if roots != nil {
			r.Check(name+" chain", verifyEKChain(cert, roots, inter), "chains to a supplied root (revocation not checked)")
		}
	}

	if c.needAuth(r, "EK creation and credential activation", false, true) {
		return
	}
	if !c.Algs[algRSA] {
		r.Add("Credential activation", Skip, "needs RSA")
		return
	}
	r.Doing("creating the RSA EK from the standard TCG template")
	ek, err := t.CreatePrimary(rhEndorsement, Template{Type: algRSA, RSABits: 2048, Symmetric: true, Policy: ekPolicy, Attrs: ekAttrs, Unique: 256})
	if !r.Check("RSA EK creation", err, "created") {
		return
	}
	defer t.Flush(ek.Handle)
	matches := func(a, b crypto.PublicKey) bool {
		switch k := a.(type) {
		case *rsa.PublicKey:
			return k.Equal(b)
		case *ecdsa.PublicKey:
			return k.Equal(b)
		}
		return false
	}
	if cert := certs[algRSA]; cert != nil {
		if matches(ek.Key, cert.PublicKey) {
			r.Add("RSA EK matches certificate", Pass, "TPM holds the certified key")
		} else {
			r.Add("RSA EK matches certificate", Warn, "differs: the vendor may use a non-default template (NV 0x01C00003/04), or the certificate belongs to another key")
		}
	}
	if cert := certs[algECC]; cert != nil && slices.Contains(c.Curves, curveP256) {
		e, err := t.CreatePrimary(rhEndorsement, Template{Type: algECC, Curve: curveP256, Symmetric: true, Policy: ekPolicy, Attrs: ekAttrs, Unique: 32})
		if r.Check("ECC EK creation", err, "created") {
			if matches(e.Key, cert.PublicKey) {
				r.Add("ECC EK matches certificate", Pass, "TPM holds the certified key")
			} else {
				r.Add("ECC EK matches certificate", Warn, "differs from the default P-256 template key")
			}
			t.Flush(e.Handle)
		}
	}

	r.Doing("MakeCredential in software, ActivateCredential in TPM")
	ak, err := t.CreatePrimary(rhEndorsement, Template{Type: algRSA, RSABits: 2048, Scheme: algRSASSA, Attrs: baseAttrs | attrRestricted | attrSign})
	if !r.Check("Attestation key for activation", err, "created") {
		return
	}
	defer t.Flush(ak.Handle)
	secret := random(32)
	blob, enc, err := makeCredential(ek.Key.(*rsa.PublicKey), ak.Name, secret)
	var got []byte
	if err == nil {
		got, err = activate(t, ak, ek, blob, enc)
	}
	if err == nil && !bytes.Equal(got, secret) {
		err = errors.New("recovered credential differs")
	}
	r.Check("Credential activation", err, "AK and EK are resident in the same TPM")

	wrongName := slices.Clone(ak.Name)
	wrongName[len(wrongName)-1] ^= 1
	blob, enc, err = makeCredential(ek.Key.(*rsa.PublicKey), wrongName, secret)
	if err == nil {
		_, err = activate(t, ak, ek, blob, enc)
	}
	expectRejected(r, "Activation for a different key refused", err, 0x9F)
}
