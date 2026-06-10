## Cryptographic Certificate Generation — Full Component Breakdown

---

### 1. RNG — Random Number Generator
The foundation of everything. Used to generate unpredictable key material. Must be a **CSPRNG** (Cryptographically Secure PRG) — e.g., `/dev/urandom`, `crypto/rand` in Go. Weak RNG = weak keys. Everything downstream depends on this.

---

### 2. Key Pair (Asymmetric Key Generation)
The actual crypto asset being created. Composed of:

- **Private key** — kept secret, used to sign or decrypt. Never leaves the generating system (ideally).
- **Public key** — derived from private key, freely shareable, used to verify signatures or encrypt.

Common algorithms:
- **RSA** — key size matters (2048 min, 4096 preferred). Slow but universal.
- **ECDSA** — elliptic curve; smaller keys, faster. P-256, P-384 common in TLS.
- **Ed25519** — modern EdDSA variant. Fastest, simplest, no weak-parameter risk. Preferred for SSH, increasingly for TLS.

---

### 3. CSR — Certificate Signing Request
A message sent to a CA asking it to vouch for your public key. Contains:
- Your **public key**
- **Subject** fields (CN, O, OU, C, ST, L)
- **SANs** — Subject Alternative Names (DNS names, IPs) — the actual thing browsers validate today; CN is legacy
- **Key usage** extensions requested (e.g., digital signature, key encipherment)
- A **self-signature** using your private key — proves you possess the private key corresponding to the public key in the request

In Go: `x509.CertificateRequest` + `x509.CreateCertificateRequest()`

---

### 4. CA — Certificate Authority
The trust anchor that signs your CSR and produces a certificate. Can be:

- **Root CA** — self-signed, the top of the chain. Its cert is hardcoded/distributed as a trust anchor.
- **Intermediate CA** — signed by root, signs end-entity certs. Root stays offline; intermediate is the operational signer.
- **End-entity / leaf cert** — the actual TLS/client cert. Not authorized to sign other certs (`CA:false` in Basic Constraints).

The CA:
1. Validates the CSR (policy checks, domain ownership, etc.)
2. Assigns a **serial number**
3. Sets **validity period** (`NotBefore` / `NotAfter`)
4. Sets **extensions** (Key Usage, Extended Key Usage, Subject Key Identifier, Authority Key Identifier, CRL/OCSP URLs)
5. Signs the certificate with its own private key

---

### 5. Certificate Extensions
What the cert is *allowed to do*:

| Extension | Purpose |
|---|---|
| **Basic Constraints** | Is this a CA? What's the max chain depth? |
| **Key Usage** | Bit flags: DigitalSignature, KeyEncipherment, CertSign, CRLSign, etc. |
| **Extended Key Usage** | TLS server auth, client auth, code signing, email, etc. |
| **Subject Key Identifier (SKID)** | Hash of the subject's public key — used to build chains |
| **Authority Key Identifier (AKID)** | Identifies which CA key signed this cert |
| **SAN** | DNS names, IPs, emails the cert is valid for |
| **CRL Distribution Points** | URL(s) to download revocation lists |
| **OCSP / AIA** | URL for real-time revocation checking + CA cert URL |
| **Name Constraints** | CA-only: limits what domains this CA can sign |

---

### 6. Signature
The CA runs: `Sign(hash(tbsCertificate), CA_private_key)` where `tbsCertificate` is the "to be signed" portion. The signature algorithm is declared in the cert (e.g., `sha256WithRSAEncryption`, `ecdsa-with-SHA384`).

---

### 7. Certificate Chain / Trust Store
- **Chain** — the ordered sequence: leaf → intermediate(s) → root
- **Trust store** — collection of root CA certs that a verifier (OS, browser, Go's `x509.CertPool`) trusts unconditionally
- Verification walks the chain, checks signatures, validity windows, key usage, and revocation at each step

---

### 8. Revocation Infrastructure
How compromised certs are invalidated before expiry:

- **CRL** (Certificate Revocation List) — CA periodically publishes a signed list of revoked serial numbers. Bulky, not real-time.
- **OCSP** (Online Certificate Status Protocol) — per-cert real-time query to CA's responder.
- **OCSP Stapling** — server fetches OCSP response itself and staples it to the TLS handshake. Avoids client-to-CA round trips.

---

### 9. Key Storage / Protection
Where the private key lives:

- **PEM file** — common, optionally passphrase-encrypted (PKCS#8 or legacy PKCS#1)
- **PKCS#12 / PFX** — bundled cert + key, password-protected container
- **HSM** (Hardware Security Module) — key never leaves hardware; operations happen inside. Used in CA infrastructure.
- **TPM / AMD SEV secret injection** — relevant to your CVM work; attestation-tied key unsealing

---

### 10. Encoding Formats

| Format | Description |
|---|---|
| **DER** | Raw binary ASN.1 encoding |
| **PEM** | Base64-wrapped DER with `-----BEGIN ...-----` headers |
| **PKCS#1** | RSA-specific key format |
| **PKCS#8** | Algorithm-agnostic private key format (preferred) |
| **PKCS#10** | CSR format |
| **PKCS#12** | Cert + key bundle |
| **X.509** | The certificate structure standard itself (ITU-T / RFC 5280) |

---

### End-to-End Flow Summary

```
CSPRNG
  └─► Key Pair (private + public)
        └─► CSR (public key + subject + SANs + self-sig)
              └─► CA validates + signs
                    └─► X.509 Certificate
                          ├─ Extensions (KU, EKU, SAN, SKID, AKID…)
                          ├─ Serial number + validity window
                          └─ CA signature (over DER-encoded TBS)
                                └─► Chain (leaf → intermediate → root)
                                      └─► Trust store verification
```

In Go, `crypto/x509`, `crypto/rsa`, `crypto/ecdsa`, `crypto/elliptic`, `crypto/rand`, and `encoding/pem` cover almost everything. For HSM or TPM-backed keys, the `crypto.Signer` interface lets you plug in hardware-resident keys transparently.

