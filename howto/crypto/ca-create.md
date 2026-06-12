## CA creation (breakdown and comprehensive workflow diagram)

---

## CA creation: full breakdown

### What a CA actually is

A CA is just a self-signed X.509 certificate paired with a private key, where the `IsCA` basic constraint is set to `true` and a `KeyUsage` includes `KeyUsageCertSign` (and usually `KeyUsageCRLSign`). Everything else — trust, hierarchy, policy — is convention layered on top of that structure.

---

### Sources and dependencies

**Entropy / RNG**
The single most critical input. The private key is only as good as the randomness it came from. In Go, `crypto/rand.Reader` is the correct source — it delegates to `getrandom(2)` on Linux (or `/dev/urandom`). You must never substitute `math/rand`. On early boot or in containers, the kernel entropy pool may not be fully seeded; this is a real risk for automated provisioning pipelines.

**Private key**
Generated fresh (most common) or loaded from existing material (HSM, KMS, or an existing PEM). Choices:
- **RSA 4096**: broadest compatibility, slow signing
- **ECDSA P-256 / P-384**: fast, short sigs, FIPS-approved, the default choice now
- **Ed25519**: fastest, smallest, but not universally accepted in older TLS stacks or PKCS#11 interfaces

In Go, `ecdsa.GenerateKey(elliptic.P256(), rand.Reader)` is the right idiom for a new CA key.

**CA certificate template** (`x509.Certificate`)
Fields that matter most:
- `SerialNumber`: must be unique per CA. For a root, typically a large random integer (not `1`). Use `crypto/rand` to generate it.
- `Subject`: the `pkix.Name` — CN, O, OU, Country. No strict requirements for a private CA, but must be distinguishable across your hierarchy.
- `NotBefore` / `NotAfter`: root CAs often get 10–20 year lifetimes; intermediate CAs 1–5 years. Clock skew at issuance is a subtle bug — subtract a few minutes from `NotBefore` to tolerate skew.
- `KeyUsage`: `x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature`
- `BasicConstraintsValid: true` + `IsCA: true`
- `MaxPathLen` / `MaxPathLenZero`: controls how deep a chain can go below this CA. A root issuing directly to leaf certs should set `MaxPathLen: 0` on intermediates.
- `SubjectKeyId`: should be the SHA-1 hash of the public key (used by many validators). `x509.SubjectKeyIdFromPublicKey` in recent Go versions.
- `AuthorityKeyIdentifier`: for a self-signed root, same as `SubjectKeyId`.

**Signing**
`x509.CreateCertificate(rand.Reader, template, parent, pubKey, privKey)` — for a self-signed root, `template == parent` and both keys refer to the same pair.

---

### CA hierarchy patterns

**Root-only (flat)**: Simple, one key to protect. Every issued cert chains directly to the root. Risk: root key exposure invalidates everything.

**Root → Intermediate(s) → Leaf**: The root is kept offline (air-gapped). Intermediates are online and do the actual signing. Compromise of an intermediate can be handled by revoking it without touching the root.

**Root → Issuing CA → Per-service intermediates**: Used in large deployments (OCI, etc.) where different services or regions need isolated signing authority.

---

### Potential failures, dangers, and things to watch

**RNG starvation**: In a newly booted container or VM (relevant to your CVM work), `rand.Reader` may block or produce low-entropy output if the kernel hasn't accumulated enough entropy. Mitigations: use `virtio-rng` or `rngd`, check `/proc/sys/kernel/random/entropy_avail`, or use hardware RNG passthrough.

**Weak key material**: RSA < 2048 bits is broken for practical purposes. RSA 2048 is technically sufficient but 4096 is preferred for CAs with long lifetimes. Never use DSA.

**Serial number collisions**: If you issue serials sequentially starting from 1 and restart your issuance process, you can reuse a serial on a different certificate — this breaks RFC 5280 and causes validation failures in strict implementations (OpenSSL, Go's `x509` verifier). Always generate serials as large random numbers.

**Clock skew / `NotBefore` in the future**: If the issuing machine's clock is ahead of the verifying machine's clock, the cert is "not yet valid." Always backdate `NotBefore` by 1–5 minutes. This is especially relevant in CVM boot flows where the guest clock starts from firmware time.

**Missing `BasicConstraints`**: A cert without `IsCA: true` will fail chain validation when used as a CA in Go's `x509` verifier. `BasicConstraintsValid` must also be `true`.

**CA key on disk in plaintext**: The private key PEM should be encrypted at rest (`x509.MarshalECPrivateKey` → `pem.EncodeToMemory` with a passphrase, or stored in a KMS/HSM). For your AMD SEV-SNP work, the obvious approach is to seal the CA key to the platform's measurement — only a CVM in the correct state can unseal it.

**`MaxPathLen` misconfiguration**: Setting `MaxPathLen: -1` (the zero value in Go when `MaxPathLenZero` is `false`) means "no constraint," allowing infinite chain depth. If you intend to prevent intermediates from creating sub-intermediates, explicitly set `MaxPathLen: 0` and `MaxPathLenZero: true`.

**Key usage mismatch**: A CA cert used for TLS server auth needs both `KeyUsageCertSign` (for signing certs) and the right Extended Key Usages if you're constraining the CA. Leaving `ExtKeyUsage` empty on a CA means no constraint — any cert it signs can claim any EKU. For a constrained CA (e.g. only for code signing), set `ExtKeyUsage` explicitly.

**Self-signed trust anchors**: The root CA must be explicitly trusted by your verifier — in Go, this means adding it to a `x509.CertPool`. It is not implicitly trusted just because it's self-signed. This catches people off guard when they hand off a cert chain without bundling the root.

**CRL / OCSP**: A CA without a revocation mechanism means you can't invalidate compromised leaf certs without rotating the entire CA. For production CAs, set `CRLDistributionPoints` or `OCSPServer` in issued leaf certs.

---

Now the diagram:---

### Go-specific implementation notes

**Generating the serial**:
```go
serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
```

**Backdate `NotBefore` to absorb clock skew**:
```go
NotBefore: time.Now().Add(-5 * time.Minute).UTC(),
NotAfter:  time.Now().AddDate(10, 0, 0).UTC(),
```

**SubjectKeyId** (Go 1.20+):
```go
skid, _ := x509.SubjectKeyIdFromPublicKey(privKey.Public())
template.SubjectKeyId = skid
```

**The self-signed root signing call**:
```go
derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, privKey.Public(), privKey)
```
For an intermediate, `parent` is the root `*x509.Certificate` and the signing key is the root's private key.

**Always parse back and verify**:
```go
cert, err := x509.ParseCertificate(derBytes)
// then check cert.IsCA, cert.KeyUsage, cert.Subject, etc.
```
`CreateCertificate` can succeed even with a misconfigured template — the parse-and-verify step catches issues like a missing `BasicConstraintsValid` before the cert is stored.

