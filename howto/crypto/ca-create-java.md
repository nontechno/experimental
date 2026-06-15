## Creating a CA in Java — complete step-by-step guide

Java doesn't have CA primitives in the standard library the way Go's `crypto/x509` does. You need **Bouncy Castle** — the de facto standard for X.509 work in Java. The JDK's `java.security` and `javax.security.cert` packages are too thin for CA construction.

---

### Dependencies

```xml
<!-- Maven -->
<dependency>
    <groupId>org.bouncycastle</groupId>
    <artifactId>bcprov-jdk18on</artifactId>
    <version>1.78.1</version>
</dependency>
<dependency>
    <groupId>org.bouncycastle</groupId>
    <artifactId>bcpkix-jdk18on</artifactId>
    <version>1.78.1</version>
</dependency>
```

`bcprov` = crypto primitives. `bcpkix` = X.509 / PKIX / PEM tooling. Both are required.

Register the provider once at JVM startup:

```java
Security.addProvider(new BouncyCastleProvider());
```

---

### Step 1 — Entropy / SecureRandom

```java
// Default: pulls from OS entropy (/dev/urandom on Linux, CryptGenRandom on Windows)
SecureRandom rng = new SecureRandom();

// Explicit algorithm — prefer this in server contexts
SecureRandom rng = SecureRandom.getInstanceStrong(); // blocking, highest entropy
SecureRandom rng = SecureRandom.getInstance("NativePRNGNonBlocking"); // non-blocking

// In FIPS environments
SecureRandom rng = SecureRandom.getInstance("DRBG"); // NIST SP 800-90A
```

**What to watch:**
- `getInstanceStrong()` can block on low-entropy systems (containers, early-boot CVMs). Prefer `NativePRNGNonBlocking` for automated pipelines and feed entropy via `virtio-rng` or `haveged`.
- Never use `new Random()` or seed `SecureRandom` with a predictable value (timestamp, PID).
- In Android, `SecureRandom` had a known weakness before API 18 — irrelevant for server CA work but worth knowing.

---

### Step 2 — Generate or load the private key

#### Option A: generate fresh (most common)

```java
// ECDSA P-256 — recommended default
KeyPairGenerator kpg = KeyPairGenerator.getInstance("EC", "BC");
kpg.initialize(new ECGenParameterSpec("P-256"), rng);
KeyPair caKeyPair = kpg.generateKeyPair();

// ECDSA P-384 — higher security margin, slower
kpg.initialize(new ECGenParameterSpec("P-384"), rng);

// RSA 4096 — broadest compatibility, much slower keygen
KeyPairGenerator kpg = KeyPairGenerator.getInstance("RSA", "BC");
kpg.initialize(4096, rng);
KeyPair caKeyPair = kpg.generateKeyPair();

// Ed25519 — fastest, smallest sigs; not all TLS stacks accept it yet
KeyPairGenerator kpg = KeyPairGenerator.getInstance("Ed25519", "BC");
kpg.initialize(new ECNamedCurveGenParameterSpec("Ed25519"), rng);
KeyPair caKeyPair = kpg.generateKeyPair();
```

#### Option B: load existing PEM key

```java
try (PEMParser parser = new PEMParser(new FileReader("ca.key"))) {
    Object obj = parser.readObject();
    JcaPEMKeyConverter converter = new JcaPEMKeyConverter().setProvider("BC");

    if (obj instanceof PEMEncryptedKeyPair encrypted) {
        // passphrase-protected key
        PEMDecryptorProvider decryptor =
            new JcePEMDecryptorProviderBuilder().build("passphrase".toCharArray());
        caKeyPair = converter.getKeyPair(encrypted.decryptKeyPair(decryptor));
    } else if (obj instanceof PEMKeyPair plain) {
        caKeyPair = converter.getKeyPair(plain);
    } else if (obj instanceof PrivateKeyInfo pki) {
        // PKCS#8 unencrypted
        PrivateKey priv = converter.getPrivateKey(pki);
        // reconstruct public key from private (EC only)
        // ...
    }
}
```

#### Option C: HSM / PKCS#11

```java
// Point to your HSM's PKCS#11 library
String config = "name=SoftHSM\nlibrary=/usr/lib/softhsm/libsofthsm2.so\nslot=0";
Provider p11 = Security.getProvider("SunPKCS11")
                       .configure(config);  // Java 9+
Security.addProvider(p11);

KeyStore ks = KeyStore.getInstance("PKCS11", p11);
ks.load(null, "pin".toCharArray());

// Key stays inside HSM — private key handle never leaves hardware
PrivateKey caPrivKey = (PrivateKey) ks.getKey("ca-key", null);
PublicKey  caPubKey  = ks.getCertificate("ca-key").getPublicKey();
```

---

### Step 3 — Build the subject DN

```java
// Simple form
X500Name subject = new X500Name("CN=My Root CA, O=Example Corp, C=US");

// Structured builder — recommended, avoids escaping bugs
X500NameBuilder builder = new X500NameBuilder(BCStyle.INSTANCE);
builder.addRDN(BCStyle.CN, "My Root CA");
builder.addRDN(BCStyle.O,  "Example Corp");
builder.addRDN(BCStyle.OU, "Security");
builder.addRDN(BCStyle.C,  "US");
builder.addRDN(BCStyle.ST, "Washington");
builder.addRDN(BCStyle.L,  "Seattle");
// Optional: email, domain component
builder.addRDN(BCStyle.EmailAddress, "ca@example.com");
X500Name subject = builder.build();
```

**Note on CN:** RFC 5280 doesn't require CN on a CA cert — `O` is sufficient. Many modern validators ignore CN on CAs entirely. But most PKI tooling and human operators expect it.

---

### Step 4 — Generate the serial number

```java
// Required: must be unique per CA, positive, max 20 octets (RFC 5280 §4.1.2.2)
BigInteger serial = new BigInteger(128, rng); // 128-bit random — correct approach

// Wrong: sequential from 1 — breaks if you restart issuance
// Wrong: System.currentTimeMillis() — too short, predictable
```

---

### Step 5 — Set validity period

```java
Date notBefore = Date.from(
    Instant.now()
           .minus(5, ChronoUnit.MINUTES)  // backdate to absorb clock skew
           .truncatedTo(ChronoUnit.SECONDS)
);

Date notAfter = Date.from(
    Instant.now()
           .plus(3650, ChronoUnit.DAYS)   // 10 years for a root CA
           .truncatedTo(ChronoUnit.SECONDS)
);

// For intermediate CAs: shorter — 1–3 years
// For leaf certs:       shorter still — 90 days (Let's Encrypt model) to 2 years
```

**The 5-minute backdating** absorbs clock skew between the issuing machine and verifiers. Without it, a verifier whose clock is even a few seconds behind will reject the cert as "not yet valid."

---

### Step 6 — Build the certificate with extensions

This is the core Bouncy Castle builder pattern:

```java
JcaX509v3CertificateBuilder certBuilder = new JcaX509v3CertificateBuilder(
    subject,      // issuer  — same as subject for self-signed root
    serial,
    notBefore,
    notAfter,
    subject,      // subject — same as issuer for self-signed root
    caKeyPair.getPublic()
);
```

#### Extension 1: Basic Constraints — CRITICAL, must be first

```java
// Root CA — no path length constraint (can issue intermediates)
certBuilder.addExtension(
    Extension.basicConstraints,
    true,   // critical — MUST be marked critical per RFC 5280
    new BasicConstraints(true)   // isCA = true, no maxPathLen
);

// OR: constrain chain depth
// maxPathLen=0 means: this CA can only issue leaf certs, not sub-CAs
certBuilder.addExtension(
    Extension.basicConstraints,
    true,
    new BasicConstraints(0)   // maxPathLen = 0
);

// maxPathLen=1 means: this CA can issue one level of sub-CAs
certBuilder.addExtension(
    Extension.basicConstraints,
    true,
    new BasicConstraints(1)
);
```

#### Extension 2: Key Usage — CRITICAL

```java
certBuilder.addExtension(
    Extension.keyUsage,
    true,   // critical
    new KeyUsage(
        KeyUsage.keyCertSign    // sign certificates — required for a CA
        | KeyUsage.cRLSign      // sign CRLs — required if you issue CRLs
        | KeyUsage.digitalSignature  // optional for root; needed if CA also signs OCSP
    )
);
```

#### Extension 3: Subject Key Identifier — non-critical, recommended

```java
JcaX509ExtensionUtils extUtils = new JcaX509ExtensionUtils();

certBuilder.addExtension(
    Extension.subjectKeyIdentifier,
    false,  // non-critical per RFC 5280
    extUtils.createSubjectKeyIdentifier(caKeyPair.getPublic())
    // SHA-1 hash of the public key's BIT STRING — standard method
);
```

#### Extension 4: Authority Key Identifier — non-critical, recommended

For a self-signed root, AKID references itself. For an intermediate, it references the issuing CA's public key.

```java
// Self-signed root: AKID == SKID
certBuilder.addExtension(
    Extension.authorityKeyIdentifier,
    false,
    extUtils.createAuthorityKeyIdentifier(caKeyPair.getPublic())
);

// Intermediate signed by rootCert:
certBuilder.addExtension(
    Extension.authorityKeyIdentifier,
    false,
    extUtils.createAuthorityKeyIdentifier(rootCert)
);
```

#### Extension 5: Extended Key Usage — optional, use to constrain the CA

```java
// Unconstrained CA: omit ExtendedKeyUsage entirely
// Constrained CA (e.g. only for TLS):
certBuilder.addExtension(
    Extension.extendedKeyUsage,
    false,  // typically non-critical on CAs
    new ExtendedKeyUsage(new KeyPurposeId[]{
        KeyPurposeId.id_kp_serverAuth,
        KeyPurposeId.id_kp_clientAuth
    })
);
```

#### Extension 6: CRL Distribution Points — optional, needed for revocation

```java
DistributionPointName dpName = new DistributionPointName(
    new GeneralNames(new GeneralName(
        GeneralName.uniformResourceIdentifier,
        "http://crl.example.com/root.crl"
    ))
);
DistributionPoint[] dps = {
    new DistributionPoint(dpName, null, null)
};
certBuilder.addExtension(
    Extension.cRLDistributionPoints,
    false,
    new CRLDistPoint(dps)
);
```

#### Extension 7: Authority Information Access — optional, for OCSP

```java
AccessDescription ocsp = new AccessDescription(
    AccessDescription.id_ad_ocsp,
    new GeneralName(
        GeneralName.uniformResourceIdentifier,
        "http://ocsp.example.com"
    )
);
AccessDescription caIssuers = new AccessDescription(
    AccessDescription.id_ad_caIssuers,
    new GeneralName(
        GeneralName.uniformResourceIdentifier,
        "http://certs.example.com/root.crt"
    )
);
certBuilder.addExtension(
    Extension.authorityInfoAccess,
    false,
    new AuthorityInformationAccess(new AccessDescription[]{ocsp, caIssuers})
);
```

#### Extension 8: Name Constraints — optional, high-value for constrained CAs

This restricts what DNS names / IP ranges the CA can issue certs for. Extremely useful for internal CAs.

```java
GeneralSubtree[] permitted = {
    new GeneralSubtree(
        new GeneralName(GeneralName.dNSName, ".example.com"), // subtree
        new ASN1Integer(0), null
    ),
    new GeneralSubtree(
        new GeneralName(GeneralName.iPAddress,
            new DEROctetString(new byte[]{10, 0, 0, 0, (byte)255, 0, 0, 0})), // 10.0.0.0/8
        new ASN1Integer(0), null
    )
};
certBuilder.addExtension(
    Extension.nameConstraints,
    true,  // MUST be critical per RFC 5280 §4.2.1.10
    new NameConstraints(permitted, null)
);
```

#### Extension 9: Certificate Policies — optional

```java
PolicyInformation[] policyInfo = {
    new PolicyInformation(new ASN1ObjectIdentifier("2.23.140.1.2.1")) // DV OID (example)
};
certBuilder.addExtension(
    Extension.certificatePolicies,
    false,
    new CertificatePolicies(policyInfo)
);
```

---

### Step 7 — Sign the certificate

```java
// Choose signature algorithm to match key type
String sigAlg = switch (caKeyPair.getPrivate().getAlgorithm()) {
    case "EC"      -> "SHA256withECDSA";   // P-256
    // P-384 should use SHA384withECDSA
    case "RSA"     -> "SHA256withRSA";     // RSA any size
    case "Ed25519" -> "Ed25519";           // no hash prefix — EdDSA hashes internally
    default        -> throw new IllegalStateException("unknown key type");
};

ContentSigner signer = new JcaContentSignerBuilder(sigAlg)
    .setProvider("BC")
    .build(caKeyPair.getPrivate());

// For intermediate CA: sign with root's private key instead
// ContentSigner signer = new JcaContentSignerBuilder(sigAlg).build(rootPrivKey);

X509CertificateHolder certHolder = certBuilder.build(signer);

// Convert to JCA type
X509Certificate caCert = new JcaX509CertificateConverter()
    .setProvider("BC")
    .getCertificate(certHolder);
```

---

### Step 8 — Verify the result

```java
// Self-verify the signature (catches signing bugs immediately)
caCert.verify(caKeyPair.getPublic());

// Check the fields you set
assert caCert.getBasicConstraints() >= 0 : "not a CA cert";
assert (caCert.getKeyUsage()[5])          : "keyCertSign not set";  // index 5
assert caCert.getSubjectX500Principal()
             .equals(caCert.getIssuerX500Principal()) : "not self-signed";

// Validate via PKIX (builds a chain — for root, pool contains itself)
CertPathValidator validator = CertPathValidator.getInstance("PKIX");
CertificateFactory cf = CertificateFactory.getInstance("X.509");
CertPath path = cf.generateCertPath(List.of(caCert));
TrustAnchor anchor = new TrustAnchor(caCert, null);
PKIXParameters params = new PKIXParameters(Set.of(anchor));
params.setRevocationEnabled(false); // root validates against itself
validator.validate(path, params);
```

---

### Step 9 — Store the private key

#### Encrypted PEM (passphrase)

```java
JcePEMEncryptorBuilder encryptorBuilder =
    new JcePEMEncryptorBuilder("AES-256-CBC")
        .setProvider("BC")
        .setSecureRandom(rng);
PEMEncryptor encryptor = encryptorBuilder.build("passphrase".toCharArray());

try (JcaPEMWriter writer = new JcaPEMWriter(new FileWriter("ca.key"))) {
    writer.writeObject(caKeyPair.getPrivate(), encryptor);
}
```

#### PKCS#12 keystore (cert + key together)

```java
KeyStore pkcs12 = KeyStore.getInstance("PKCS12", "BC");
pkcs12.load(null, null);
pkcs12.setKeyEntry(
    "ca",
    caKeyPair.getPrivate(),
    "keystorePassword".toCharArray(),
    new Certificate[]{caCert}
);
try (FileOutputStream fos = new FileOutputStream("ca.p12")) {
    pkcs12.store(fos, "keystorePassword".toCharArray());
}
```

#### JKS keystore (legacy, Java-only)

```java
KeyStore jks = KeyStore.getInstance("JKS");
jks.load(null, null);
jks.setKeyEntry("ca", caKeyPair.getPrivate(),
    "keyPassword".toCharArray(), new Certificate[]{caCert});
try (FileOutputStream fos = new FileOutputStream("ca.jks")) {
    jks.store(fos, "storePassword".toCharArray());
}
```

---

### Step 10 — Export the public certificate (PEM)

```java
try (JcaPEMWriter writer = new JcaPEMWriter(new FileWriter("ca.crt"))) {
    writer.writeObject(caCert);
}

// Or DER format (binary, used by some Java APIs and system trust stores)
Files.write(Path.of("ca.der"), caCert.getEncoded());
```

---

### Step 11 — Use the CA to sign a leaf certificate

```java
// Leaf's keypair (generated separately)
KeyPairGenerator leafKpg = KeyPairGenerator.getInstance("EC", "BC");
leafKpg.initialize(new ECGenParameterSpec("P-256"), rng);
KeyPair leafKeyPair = leafKpg.generateKeyPair();

BigInteger leafSerial = new BigInteger(128, rng);
Date leafNotBefore = Date.from(Instant.now().minus(5, ChronoUnit.MINUTES));
Date leafNotAfter  = Date.from(Instant.now().plus(90, ChronoUnit.DAYS));

// Issuer = CA subject; subject = leaf's own DN
JcaX509v3CertificateBuilder leafBuilder = new JcaX509v3CertificateBuilder(
    caCert,          // issuer taken from CA cert
    leafSerial,
    leafNotBefore,
    leafNotAfter,
    new X500Name("CN=service.example.com"),
    leafKeyPair.getPublic()
);

// BasicConstraints: NOT a CA
leafBuilder.addExtension(Extension.basicConstraints, true, new BasicConstraints(false));

// Subject Alternative Names — required for TLS (CN alone is rejected by modern browsers)
leafBuilder.addExtension(
    Extension.subjectAlternativeName,
    false,
    new GeneralNames(new GeneralName[]{
        new GeneralName(GeneralName.dNSName, "service.example.com"),
        new GeneralName(GeneralName.dNSName, "*.service.example.com"),
        new GeneralName(GeneralName.iPAddress, "10.0.0.1")
    })
);

leafBuilder.addExtension(Extension.keyUsage, true,
    new KeyUsage(KeyUsage.digitalSignature | KeyUsage.keyEncipherment));

leafBuilder.addExtension(Extension.extendedKeyUsage, false,
    new ExtendedKeyUsage(new KeyPurposeId[]{
        KeyPurposeId.id_kp_serverAuth,
        KeyPurposeId.id_kp_clientAuth
    })
);

// AKID points to CA
leafBuilder.addExtension(Extension.authorityKeyIdentifier, false,
    extUtils.createAuthorityKeyIdentifier(caCert));

// Sign with CA's private key
ContentSigner leafSigner = new JcaContentSignerBuilder("SHA256withECDSA")
    .setProvider("BC").build(caKeyPair.getPrivate());

X509Certificate leafCert = new JcaX509CertificateConverter()
    .setProvider("BC")
    .getCertificate(leafBuilder.build(leafSigner));
```

---

### Step 12 — Verify a chain

```java
// Build trust store containing the CA
KeyStore trustStore = KeyStore.getInstance("PKCS12");
trustStore.load(null, null);
trustStore.setCertificateEntry("ca", caCert);

// Build the chain to validate
CertificateFactory cf = CertificateFactory.getInstance("X.509");
CertPath chain = cf.generateCertPath(List.of(leafCert)); // intermediates go here too

// PKIX validation
CertPathValidator cpv = CertPathValidator.getInstance("PKIX");
PKIXBuilderParameters params = new PKIXBuilderParameters(trustStore, null);
params.setRevocationEnabled(false); // enable if you have CRL/OCSP
PKIXCertPathValidatorResult result =
    (PKIXCertPathValidatorResult) cpv.validate(chain, params);

TrustAnchor usedAnchor = result.getTrustAnchor(); // confirms which CA was used
```

---

### Common pitfalls specific to Java / Bouncy Castle

**Provider not registered**: If you call BC APIs without `Security.addProvider(new BouncyCastleProvider())`, you get a `NoSuchProviderException` or silently fall back to the JDK provider, which lacks many algorithms. Register it once in a `static {}` block.

**`JcaX509v3CertificateBuilder` vs `X509v3CertificateBuilder`**: The `Jca` prefixed variant accepts JCA types (`PublicKey`, `X509Certificate`). The non-`Jca` variant works with raw BC ASN.1 types (`SubjectPublicKeyInfo`). Mixing them without converting causes `ClassCastException` at runtime.

**`BasicConstraints` not marked critical**: RFC 5280 §4.2.1.9 requires `BasicConstraints` to be critical on CA certs. Bouncy Castle won't enforce this for you — passing `false` as the critical flag will produce a cert that some strict validators reject.

**Signature algorithm mismatch**: Using `SHA256withECDSA` with an RSA key throws at signing time. Using `SHA256withRSA` with an Ed25519 key also throws. The algorithm string must match the key type.

**`KeyUsage` bit index confusion**: `caCert.getKeyUsage()` returns a `boolean[]` where index 5 is `keyCertSign`, index 6 is `cRLSign`. The bit ordering is defined in RFC 5280 and is not the same as the `KeyUsage.*` constants' numeric values.

**PKCS#12 vs JKS**: JKS is a proprietary Oracle format, deprecated since Java 9. New code should always use PKCS#12. Some older Java tooling (Tomcat configs, legacy Spring setups) still defaults to JKS — explicitly set `KeyStore.getInstance("PKCS12")`.

**Clock skew without backdate**: Java's `Date` has millisecond precision but X.509 `UTCTime` and `GeneralizedTime` only have second precision. Bouncy Castle truncates milliseconds. Always truncate to seconds yourself (`.truncatedTo(ChronoUnit.SECONDS)`) to avoid off-by-one second issues in strict parsers.
