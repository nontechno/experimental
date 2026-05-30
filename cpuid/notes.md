Here's what this does:

**File layout:**
```
sysinfo/
├── go.mod
├── main.go           (build: linux && amd64)
├── cpuid_amd64.go    (forward declaration)
└── cpuid_amd64.s     (CPUID instruction via Go assembly)
```

**What each section detects:**

| Section | Technique |
|---|---|
| **Environment type** | CPUID hypervisor bit, `/.dockerenv`, `/run/.containerenv`, `/proc/1/cgroup` content, cgroup namespace diff, PID 1 exe |
| **Hypervisor** | CPUID leaf `0x40000000` brand string (12 bytes: EBX+ECX+EDX), leaf 1 ECX[31], DMI product/vendor, `/sys/hypervisor/type` (Xen), KVM feature leaf `0x40000001` |
| **Confidential VM** | CPUID `0x8000001F` (AMD SEV caps), MSR `0xC0010131` (SEV_STATUS, bits 0/1/2 = SEV/ES/SNP), `/proc/cpuinfo` `sev`/`sev_es`/`sev_snp`/`tdx_guest` flags, `/dev/sev-guest`, `/dev/tdx-guest`, CPUID leaf `0x21` (TDX signature), ACPI CCEL table |
| **CPU** | `/proc/cpuinfo`, AMD topology leaf `0x8000001E`, CPUID leaf 7 (CET/SGX/SHA-NI), all flags annotated |
| **Memory** | `/proc/meminfo` — total/available/used/swap/hugepages/THP/direct-map |
| **Firmware** | `/sys/class/dmi/id/*`, EFI vars, Secure Boot EFI variable |
| **Modules** | `/sys/module/` — KVM, virtio, SEV, TPM, VFIO, vboxguest, etc. |
| **Cmdline** | Highlights security-relevant params (`mitigations=off`, `nokaslr`, `mem_encrypt=on`, `iommu=`, etc.) |

**Notes for OCI use:**
- Run as root to get MSR `0xC0010131` (requires `modprobe msr` first on OCI Linux)
- On an AMD SEV-SNP CVM, you'll see all three MSR bits set, `/dev/sev-guest` present, and the `sev_snp` cpuinfo flag
- On TDX, CPUID leaf `0x21` returns `"IntelTDX    "` and `/dev/tdx-guest` appears