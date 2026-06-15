When an intermediate CA needs to prove its identity to a root CA (typically during the certificate issuance/signing ceremony), there are a few distinct mechanisms:

## 1. CSR + Out-of-Band Identity Verification (most common)

The intermediate CA generates a key pair and submits a **Certificate Signing Request (CSR)** containing its public key and distinguished name. The root CA doesn't cryptographically verify the intermediate's identity from the CSR alone — it relies on **out-of-band trust**: the CSR was delivered via a secure channel, physically in an air-gapped ceremony, or authenticated by organizational process. The root simply signs what it trusts by policy.

## 2. Cross-Certification (CA-to-CA mutual trust)

Two established CAs can **cross-certify** each other — each issues a certificate to the other's public key. Neither is "proving" identity in a challenge-response sense; instead, they're establishing bidirectional trust based on prior bilateral agreement. Common in bridge PKI architectures (e.g., US Federal PKI).

## 3. Proof-of-Possession (PoP) of Private Key

The intermediate proves it **actually controls the private key** corresponding to the public key in the CSR. Mechanisms:

- **Self-signed CSR signature** — the CSR itself is signed with the private key (standard PKCS#10). The root verifies the signature before signing.
- **Challenge-response** — the root sends a nonce encrypted to the public key; the intermediate decrypts and returns it (used in some CMP/CRMF protocols).
- **CMP (RFC 4210) `ir`/`cr` messages** — the Certificate Management Protocol supports explicit PoP via `POPOSigningKey` or `POPOPrivKey` structures.

## 4. Existing Credential Bootstrapping

If the intermediate already has a credential from a trusted source:

- **Another CA's certificate** — present a cert from a mutually trusted third CA; the root accepts it as identity proof.
- **HSM attestation** — the intermediate's key was generated inside an HSM; the HSM vendor's attestation certificate proves the key is non-exportable and was generated in a specific device. The root trusts the HSM vendor's root to extend trust transitively.
- **ACME + existing domain/IP control** — for WebPKI intermediates, ACME's `dns-01`/`tls-alpn-01` challenges prove domain control as a proxy for identity.

## 5. Registration Authority (RA) Vouching

An RA acts as an identity broker — it performs identity vetting out-of-band and then vouches to the root CA via a signed RA assertion. The root trusts the RA's signature rather than directly verifying the intermediate's identity. Used in enterprise PKI (EST protocol, RFC 7030, supports this model).

---

**In practice**, the vast majority of intermediate CA issuance uses **#1 + #3** together: a PKCS#10 CSR (which inherently proves PoP via its self-signature), delivered through a trusted administrative process. HSM attestation (#4) is increasingly common for high-assurance intermediates to prove key confinement.
