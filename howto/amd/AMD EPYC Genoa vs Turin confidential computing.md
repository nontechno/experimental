Here's a comparison of `epyc-genoa-2` (EPYC 9004, Zen 4) and `epyc-turin-1` (EPYC 9005, Zen 5) specifically from a confidential computing perspective.

---

## Overview

Both generations support **SEV-SNP** (the full stack: SME → SEV → SEV-ES → SEV-SNP), but Turin (Zen 5) introduces several new security primitives, some of which were still landing in the Linux kernel at Turin's launch.

---

## Differences Table

| Feature | `epyc-genoa-2` (Zen 4 / 9004) | `epyc-turin-1` (Zen 5 / 9005) |
|---|---|---|
| **SEV-SNP** | ✅ Full support | ✅ Full support |
| **Memory encryption cipher** | AES-256-XTS | AES-256-XTS (unchanged) |
| **Secure AVIC** | ❌ | ✅ New in Zen 5 |
| **CipherTextHiding (CTH)** | ❌ | ✅ New in Zen 5 |
| **Secure TSC for SNP guests** | ❌ | ✅ New in Zen 5 |
| **RDSEED side-channel fix** | ❌ Vulnerable | ✅ Mitigated in hardware |
| **Trusted I/O (TIO) enhancements** | Partial / earlier version | ✅ Enhanced in Zen 5 |
| **Attestation report version** | v3 | v4 (breaking change for parsers) |
| **VMPL / SVSM support** | ✅ | ✅ (unchanged) |
| **SEV-SNP overhead** | ~2–10% typical | ~3–15% (similar range, more cores) |
| **Known CVEs** | CVE-2023-31355, CVE-2024-21978, CVE-2024-21980 | Separate Zen 5 vuln (GCP-2025-058) |
| **Linux kernel support maturity** | Battle-tested, upstream | Some features still in patch/staging at launch |

---

## Key Details

**Secure AVIC** is the most practically significant addition. Secure AVIC (Advanced Virtual Interrupt Controller) allows managing guest-owned APIC state for SEV-SNP VM guests with a private, guest-owned backing page on a per-vCPU basis, and works with KVM guests running SEV-SNP VMs. This removes the hypervisor from the interrupt delivery path for confidential guests — a meaningful reduction in attack surface.

**CipherTextHiding, Secure TSC, and AVIC** were available at Turin's launch only as kernel mailing list patches, not yet upstreamed. So if you're on an older kernel (e.g., OCI's Oracle Linux kernel), you may not be using these even on Turin hardware.

**RDSEED fix**: Zen 5 introduces mitigations for the RDSEED entropy bug that affected Zen 4 — relevant if your confidential workloads depend on hardware RNG (e.g., key generation inside a guest).

**Attestation report v4**: Following a firmware update, Confidential VM instances with AMD SEV-SNP on Turin generate v4 attestation reports; attestation report parsers designed for v3 may break, and Go users should update `go-sev-guest` to v0.14.0 or above. This is a real gotcha if you're building an attestation flow in Go (relevant given your `snp-check` tooling).

**Memory encryption**: Genoa (4th gen) already upgraded to AES-256-XTS, so there's no improvement here in Turin — both use AES-256.

**SVSM** (Secure VM Service Module) support exists on both generations — it's a SEV-SNP-level feature, not generation-specific.

---

## Practical Takeaway for OCI/QEMU Work

- The QEMU CPU type string matters for the **launch measurement** (VMSA includes the CPU signature). `EPYC-v4` is the Genoa guest type; there's now an `EPYC-v5` or equivalent for Turin. Using the wrong vcpu-type produces a different measurement hash.
- If you're parsing attestation reports in Go, the **v3→v4 format change** is the most immediate operational concern.
- **Secure AVIC** is the long-term performance/security win on Turin, but needs kernel ≥ 6.18 on the host.
