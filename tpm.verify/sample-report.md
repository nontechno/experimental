# TPM verification report

| Item | Value |
|---|---|
| Host | vm |
| Generated | 2026-11-25T06:18:42-08:00 |
| Effective UID | 0 |
| TPM interface | tcp:127.0.0.1:2321 |
| Mode | read-only=false nv-write=false ek-ca="/tmp/sw/trust" |

## Summary

No failures, but there are warnings to review. Skipped checks were not verified.

| PASS | FAIL | WARN | SKIP | INFO |
|---|---|---|---|---|
| 68 | 0 | 8 | 5 | 6 |

## Needs attention

| Status | Section | Check | Detail |
|---|---|---|---|
| WARN | Host and platform | UEFI boot | /sys/firmware/efi not present: legacy boot or hidden |
| WARN | TPM presence | Device nodes | no /dev/tpm* node (check that the TPM is enabled and the tpm_crb/tpm_tis driver is loaded) |
| WARN | TPM identity, state and capabilities | TPM type | manufacturer "IBM" indicates a software or virtual TPM, not a hardware root of trust |
| WARN | TPM identity, state and capabilities | Lockout hierarchy auth | no lockout auth set: any root user could clear the TPM or reset lockout |
| WARN | TPM identity, state and capabilities | Platform hierarchy | still enabled after boot (firmware normally disables or locks it) |
| WARN | PCR values | Firmware measurements sha1 | PCRs [0 1 2 3 4 5 6 7] are all-zero/all-one: firmware did not measure into this bank |
| WARN | PCR values | Firmware measurements sha384 | PCRs [0 1 2 3 4 5 6 7] are all-zero/all-one: firmware did not measure into this bank |
| WARN | Endorsement key and device identity | ECC EK certificate | none at NV 0x01C0000A. Some firmware TPMs (including AMD fTPM) publish it online instead: try tpm2_getekcertificate, then pass -ek-cert |

## Results

### Host and platform

| Status | Check | Detail |
|---|---|---|
| INFO | Execution environment | CPU reports a hypervisor: this is a virtual machine, so any TPM is likely virtual |
| WARN | UEFI boot | /sys/firmware/efi not present: legacy boot or hidden |
| SKIP | SecureBoot variable | cannot read EFI variable |
| SKIP | SetupMode variable | cannot read EFI variable |

### TPM presence

| Status | Check | Detail |
|---|---|---|
| WARN | Device nodes | no /dev/tpm* node (check that the TPM is enabled and the tpm_crb/tpm_tis driver is loaded) |
| PASS | Open TPM | opened tcp:127.0.0.1:2321 |

### TPM identity, state and capabilities

| Status | Check | Detail |
|---|---|---|
| PASS | TPM 2.0 response | family 2.0, revision 1.64 |
| INFO | Manufacturer | "IBM" IBM (software TPM), vendor string "SWTPM", firmware 8217.4131.22.13878 |
| WARN | TPM type | manufacturer "IBM" indicates a software or virtual TPM, not a hardware root of trust |
| INFO | Permanent flags | ownerAuthSet=0 endorsementAuthSet=0 lockoutAuthSet=0 disableClear=0 inLockout=0 tpmGeneratedEPS=1 |
| PASS | Dictionary attack lockout | not in lockout (failures 0 of 3, interval 1000s, recovery 1000s) |
| WARN | Lockout hierarchy auth | no lockout auth set: any root user could clear the TPM or reset lockout |
| INFO | Startup flags | phEnable=1 shEnable=1 ehEnable=1 phEnableNV=1 orderly=1 |
| WARN | Platform hierarchy | still enabled after boot (firmware normally disables or locks it) |
| PASS | Algorithm list | 33 algorithms |
| PASS | Command list | 110 commands |
| PASS | ECC curve list | 8 curves |
| PASS | Active PCR banks | sha1 (24 PCRs), sha256 (24 PCRs), sha384 (24 PCRs) |

### PCR values

| Status | Check | Detail |
|---|---|---|
| PASS | Read sha1 bank | 24 PCRs |
| WARN | Firmware measurements sha1 | PCRs [0 1 2 3 4 5 6 7] are all-zero/all-one: firmware did not measure into this bank |
| PASS | Read sha256 bank | 24 PCRs |
| PASS | Firmware measurements sha256 | PCRs 0-7 have been extended |
| PASS | Read sha384 bank | 24 PCRs |
| WARN | Firmware measurements sha384 | PCRs [0 1 2 3 4 5 6 7] are all-zero/all-one: firmware did not measure into this bank |

### Measured boot event log

| Status | Check | Detail |
|---|---|---|
| PASS | Parse event log | 8 events, 497 bytes |
| SKIP | Replay sha1 | log has no sha1 digests |
| PASS | Replay sha256 | PCRs [0 1 2 3 4 5 6 7] match the log |
| SKIP | Replay sha384 | log has no sha384 digests |
| PASS | Separator events | present for PCRs 0-7 |

### Self test

| Status | Check | Detail |
|---|---|---|
| PASS | Full self test | completed in 21ms |
| PASS | Self test result | TPM reports success |

### Random number generator

| Status | Check | Detail |
|---|---|---|
| PASS | GetRandom | received 4096 bytes |
| PASS | RNG sanity | no repeated 16-byte blocks, monobit z=0.28, chi-square=258.1 (255 df). Smoke test only |
| PASS | StirRandom | accepted additional entropy |

### Clock

| Status | Check | Detail |
|---|---|---|
| PASS | Clock advances | time +1000 ms, clock +1000 ms; resetCount=2 restartCount=0 safe=1 |

### Hashing

| Status | Check | Detail |
|---|---|---|
| PASS | Hash sha1 | matches software |
| PASS | Hash sha256 | matches software |
| PASS | Hash sha384 | matches software |
| PASS | Hash sha512 | matches software |

### Supported key parameters

| Status | Check | Detail |
|---|---|---|
| INFO | TestParms | see the Key parameter support table |

### PCR extend and reset

| Status | Check | Detail |
|---|---|---|
| PASS | Reset PCR 16 | reset |
| PASS | PCR 16 after reset | all zero |
| PASS | Extend arithmetic | new value equals SHA256(old \|\| data) |
| PASS | PCR 0 cannot be reset | refused as expected: TPM_RC 0x907 (LOCALITY) from command 0x13D |

### Asymmetric keys

| Status | Check | Detail |
|---|---|---|
| PASS | RSA-2048 key generation | transient key created |
| PASS | RSA-2048 sign | signature produced |
| PASS | RSA-2048 verify (software) | signature valid outside the TPM |
| PASS | RSA-2048 verify (TPM) | TPM accepts its signature |
| PASS | RSA-2048 rejects altered digest | refused as expected: TPM_RC 0x2DB (SIGNATURE) from command 0x177 |
| PASS | RSA-OAEP decrypt | round trip matches |
| PASS | ECC P-256 key generation | transient key created |
| PASS | ECC P-256 sign | signature produced |
| PASS | ECC P-256 verify (software) | signature valid outside the TPM |
| PASS | ECC P-256 verify (TPM) | TPM accepts its signature |
| PASS | ECC P-256 rejects altered digest | refused as expected: TPM_RC 0x2DB (SIGNATURE) from command 0x177 |
| PASS | ECC P-384 key generation | transient key created |
| PASS | ECC P-384 sign | signature produced |
| PASS | ECC P-384 verify (software) | signature valid outside the TPM |
| PASS | ECC P-384 verify (TPM) | TPM accepts its signature |
| PASS | ECC P-384 rejects altered digest | refused as expected: TPM_RC 0x2DB (SIGNATURE) from command 0x177 |

### Attestation quote

| Status | Check | Detail |
|---|---|---|
| PASS | ECC quote verification | signature, magic, nonce, selection and PCR digest all verified in software |
| PASS | ECC stale nonce detected | verifier rejects a different nonce |
| PASS | ECC tampered quote detected | verifier rejects a modified quote |
| PASS | RSA quote verification | signature, magic, nonce, selection and PCR digest all verified in software |
| PASS | RSA stale nonce detected | verifier rejects a different nonce |
| PASS | RSA tampered quote detected | verifier rejects a modified quote |

### Endorsement key and device identity

| Status | Check | Detail |
|---|---|---|
| PASS | Load trusted EK CAs | 1 root certificate(s) |
| PASS | RSA EK certificate | read and parsed |
| PASS | RSA EK chain | chains to a supplied root (revocation not checked) |
| WARN | ECC EK certificate | none at NV 0x01C0000A. Some firmware TPMs (including AMD fTPM) publish it online instead: try tpm2_getekcertificate, then pass -ek-cert |
| INFO | EK-range certificate at 0x01C00016 | ECDSA key, issuer CN=swtpm-localca, expires 9999-12-31 (key match not tested) |
| PASS | EK-range certificate at 0x01C00016 chain | chains to a supplied root (revocation not checked) |
| PASS | RSA EK creation | created |
| PASS | RSA EK matches certificate | TPM holds the certified key |
| PASS | Attestation key for activation | created |
| PASS | Credential activation | AK and EK are resident in the same TPM |
| PASS | Activation for a different key refused | refused as expected: TPM_RC 0x1DF (INTEGRITY) from command 0x147 |

### Sealing to PCR policy

| Status | Check | Detail |
|---|---|---|
| PASS | Prepare PCR 16 | reset |
| PASS | Storage root key | transient ECC storage key created |
| PASS | Policy digest | PolicyPCR digest bff2d58e9813f97cefc14f72ad8133bc7092d652b7c877959254af140c841f36 |
| PASS | Seal | secret sealed and loaded |
| PASS | Unseal with matching PCR | secret recovered |
| PASS | Unseal with password refused | refused as expected: TPM_RC 0x12F (AUTH_UNAVAILABLE) from command 0x15E |
| PASS | Change PCR 16 | extended |
| PASS | Unseal after PCR change refused | refused as expected: TPM_RC 0x99D (POLICY_FAIL) from command 0x15E |

### NV storage

| Status | Check | Detail |
|---|---|---|
| SKIP | NV write test | not run; pass -nv-write to define, write, read and delete a temporary NV index |

### Side effects

| Status | Check | Detail |
|---|---|---|
| PASS | Dictionary attack counter | unchanged by the tests (0) |

## Inventory

### Host

| Property | Value |
|---|---|
| OS | Ubuntu 24.04.5 LTS |
| Kernel | 6.18.44-fc-v880 |
| Arch | amd64 |
| CPU | Intel(R) Xeon(R) Processor @ 2.10GHz |
| System vendor |  |
| Product |  |
| BIOS version |  |
| BIOS date |  |
| Kernel command line | mitigations=auto,no_guest_host,no_guest_guest console=ttyS0 quiet reboot=k panic=1 nomodule random.trust_cpu=1 swiotlb=noforce psi=1 nvproxy.enable=1 page_reporting.page_reporting_order=11 deferred_init=lazy ipv6.disable=1 dhash_entries=131072 ihash_entries=65536 rdinit=/process_api -- --firecracker-init --addr 0.0.0.0:2024 --max-ws-buffer-size 32768 --block-local-connections --log-vsock-port 5002 --control-vsock-port 5001 --listen-vsock-port 2024 --tcp-readiness-only |
| Kernel lockdown |  |

### Identity

| Property | Value |
|---|---|
| Manufacturer | IBM |
| Vendor string | SWTPM |
| Firmware version | 8217.4131.22.13878 |
| Spec family | 2.0 |
| Spec revision | 1.64 |
| Spec date | day 75 of 2021 |

### All TPM properties

| ID | Name | Decimal | Hex |
|---|---|---|---|
| 0x100 | FAMILY_INDICATOR | 841887744 | 0x322E3000 |
| 0x101 | LEVEL | 0 | 0x00000000 |
| 0x102 | REVISION | 164 | 0x000000A4 |
| 0x103 | DAY_OF_YEAR | 75 | 0x0000004B |
| 0x104 | YEAR | 2021 | 0x000007E5 |
| 0x105 | MANUFACTURER | 1229081856 | 0x49424D00 |
| 0x106 | VENDOR_STRING_1 | 1398218784 | 0x53572020 |
| 0x107 | VENDOR_STRING_2 | 542396493 | 0x2054504D |
| 0x108 | VENDOR_STRING_3 | 0 | 0x00000000 |
| 0x109 | VENDOR_STRING_4 | 0 | 0x00000000 |
| 0x10A | VENDOR_TPM_TYPE | 1 | 0x00000001 |
| 0x10B | FIRMWARE_VERSION_1 | 538513443 | 0x20191023 |
| 0x10C | FIRMWARE_VERSION_2 | 1455670 | 0x00163636 |
| 0x10D | INPUT_BUFFER | 1024 | 0x00000400 |
| 0x10E | HR_TRANSIENT_MIN | 3 | 0x00000003 |
| 0x10F | HR_PERSISTENT_MIN | 7 | 0x00000007 |
| 0x110 | HR_LOADED_MIN | 3 | 0x00000003 |
| 0x111 | ACTIVE_SESSIONS_MAX | 64 | 0x00000040 |
| 0x112 | PCR_COUNT | 24 | 0x00000018 |
| 0x113 | PCR_SELECT_MIN | 3 | 0x00000003 |
| 0x114 | CONTEXT_GAP_MAX | 65535 | 0x0000FFFF |
| 0x116 | NV_COUNTERS_MAX | 0 | 0x00000000 |
| 0x117 | NV_INDEX_MAX | 2048 | 0x00000800 |
| 0x118 | MEMORY | 6 | 0x00000006 |
| 0x119 | CLOCK_UPDATE | 4096 | 0x00001000 |
| 0x11A | CONTEXT_HASH | 13 | 0x0000000D |
| 0x11B | CONTEXT_SYM | 6 | 0x00000006 |
| 0x11C | CONTEXT_SYM_SIZE | 256 | 0x00000100 |
| 0x11D | ORDERLY_COUNT | 255 | 0x000000FF |
| 0x11E | MAX_COMMAND_SIZE | 4096 | 0x00001000 |
| 0x11F | MAX_RESPONSE_SIZE | 4096 | 0x00001000 |
| 0x120 | MAX_DIGEST | 64 | 0x00000040 |
| 0x121 | MAX_OBJECT_CONTEXT | 2692 | 0x00000A84 |
| 0x122 | MAX_SESSION_CONTEXT | 404 | 0x00000194 |
| 0x123 | PS_FAMILY_INDICATOR | 841887744 | 0x322E3000 |
| 0x124 | PS_LEVEL | 0 | 0x00000000 |
| 0x125 | PS_REVISION | 164 | 0x000000A4 |
| 0x126 | PS_DAY_OF_YEAR | 75 | 0x0000004B |
| 0x127 | PS_YEAR | 2021 | 0x000007E5 |
| 0x128 | SPLIT_MAX | 128 | 0x00000080 |
| 0x129 | TOTAL_COMMANDS | 110 | 0x0000006E |
| 0x12A | LIBRARY_COMMANDS | 110 | 0x0000006E |
| 0x12B | VENDOR_COMMANDS | 0 | 0x00000000 |
| 0x12C | NV_BUFFER_MAX | 1024 | 0x00000400 |
| 0x12D | MODES | 0 | 0x00000000 |
| 0x12E |  | 1024 | 0x00000400 |
| 0x200 | PERMANENT | 1024 | 0x00000400 |
| 0x201 | STARTUP_CLEAR | 2147483663 | 0x8000000F |
| 0x202 | HR_NV_INDEX | 2 | 0x00000002 |
| 0x203 | HR_LOADED | 0 | 0x00000000 |
| 0x204 | HR_LOADED_AVAIL | 3 | 0x00000003 |
| 0x205 | HR_ACTIVE | 0 | 0x00000000 |
| 0x206 | HR_ACTIVE_AVAIL | 64 | 0x00000040 |
| 0x207 | HR_TRANSIENT_AVAIL | 3 | 0x00000003 |
| 0x208 | HR_PERSISTENT | 2 | 0x00000002 |
| 0x209 | HR_PERSISTENT_AVAIL | 62 | 0x0000003E |
| 0x20A | NV_COUNTERS | 0 | 0x00000000 |
| 0x20B | NV_COUNTERS_AVAIL | 25 | 0x00000019 |
| 0x20C | ALGORITHM_SET | 0 | 0x00000000 |
| 0x20D | LOADED_CURVES | 8 | 0x00000008 |
| 0x20E | LOCKOUT_COUNTER | 0 | 0x00000000 |
| 0x20F | MAX_AUTH_FAIL | 3 | 0x00000003 |
| 0x210 | LOCKOUT_INTERVAL | 1000 | 0x000003E8 |
| 0x211 | LOCKOUT_RECOVERY | 1000 | 0x000003E8 |
| 0x212 | NV_WRITE_RECOVERY | 0 | 0x00000000 |
| 0x213 | AUDIT_COUNTER_0 | 0 | 0x00000000 |
| 0x214 | AUDIT_COUNTER_1 | 0 | 0x00000000 |

### Algorithms

| ID | Name | Attributes |
|---|---|---|
| 0x0001 | RSA | asymmetric, object |
| 0x0003 | TDES | symmetric |
| 0x0004 | SHA1 | hash |
| 0x0005 | HMAC | hash, signing |
| 0x0006 | AES | symmetric |
| 0x0007 | MGF1 | hash, method |
| 0x0008 | KEYEDHASH | hash, object, signing, encrypting |
| 0x000A | XOR | symmetric, hash |
| 0x000B | SHA256 | hash |
| 0x000C | SHA384 | hash |
| 0x000D | SHA512 | hash |
| 0x0014 | RSASSA | asymmetric, signing |
| 0x0015 | RSAES | asymmetric, encrypting |
| 0x0016 | RSAPSS | asymmetric, signing |
| 0x0017 | OAEP | asymmetric, encrypting |
| 0x0018 | ECDSA | asymmetric, signing |
| 0x0019 | ECDH | asymmetric, method |
| 0x001A | ECDAA | asymmetric, signing |
| 0x001B | SM2 | asymmetric, signing, method |
| 0x001C | ECSCHNORR | asymmetric, signing |
| 0x001D | ECMQV | asymmetric, method |
| 0x0020 | KDF1_SP800_56A | hash, method |
| 0x0021 | KDF2 | hash, method |
| 0x0022 | KDF1_SP800_108 | hash, method |
| 0x0023 | ECC | asymmetric, object |
| 0x0025 | SYMCIPHER | object |
| 0x0026 | CAMELLIA | symmetric |
| 0x003F | CMAC | symmetric, signing |
| 0x0040 | CTR | symmetric, encrypting |
| 0x0041 | OFB | symmetric, encrypting |
| 0x0042 | CBC | symmetric, encrypting |
| 0x0043 | CFB | symmetric, encrypting |
| 0x0044 | ECB | symmetric, encrypting |

### Commands

| Code | Name |
|---|---|
| 0x11F | NV_UndefineSpaceSpecial |
| 0x120 | EvictControl |
| 0x121 | HierarchyControl |
| 0x122 | NV_UndefineSpace |
| 0x124 | ChangeEPS |
| 0x125 | ChangePPS |
| 0x126 | Clear |
| 0x127 | ClearControl |
| 0x128 | ClockSet |
| 0x129 | HierarchyChangeAuth |
| 0x12A | NV_DefineSpace |
| 0x12B | PCR_Allocate |
| 0x12C | PCR_SetAuthPolicy |
| 0x12D | PP_Commands |
| 0x12E | SetPrimaryPolicy |
| 0x130 | ClockRateAdjust |
| 0x131 | CreatePrimary |
| 0x132 | NV_GlobalWriteLock |
| 0x133 | GetCommandAuditDigest |
| 0x134 | NV_Increment |
| 0x135 | NV_SetBits |
| 0x136 | NV_Extend |
| 0x137 | NV_Write |
| 0x138 | NV_WriteLock |
| 0x139 | DictionaryAttackLockReset |
| 0x13A | DictionaryAttackParameters |
| 0x13B | NV_ChangeAuth |
| 0x13C | PCR_Event |
| 0x13D | PCR_Reset |
| 0x13E | SequenceComplete |
| 0x13F | SetAlgorithmSet |
| 0x140 | SetCommandCodeAuditStatus |
| 0x142 | IncrementalSelfTest |
| 0x143 | SelfTest |
| 0x144 | Startup |
| 0x145 | Shutdown |
| 0x146 | StirRandom |
| 0x147 | ActivateCredential |
| 0x148 | Certify |
| 0x149 | PolicyNV |
| 0x14A | CertifyCreation |
| 0x14B | Duplicate |
| 0x14C | GetTime |
| 0x14D | GetSessionAuditDigest |
| 0x14E | NV_Read |
| 0x14F | NV_ReadLock |
| 0x150 | ObjectChangeAuth |
| 0x151 | PolicySecret |
| 0x152 | Rewrap |
| 0x153 | Create |
| 0x154 | ECDH_ZGen |
| 0x155 | HMAC/MAC |
| 0x156 | Import |
| 0x157 | Load |
| 0x158 | Quote |
| 0x159 | RSA_Decrypt |
| 0x15B | HMAC_Start/MAC_Start |
| 0x15C | SequenceUpdate |
| 0x15D | Sign |
| 0x15E | Unseal |
| 0x160 | PolicySigned |
| 0x161 | ContextLoad |
| 0x162 | ContextSave |
| 0x163 | ECDH_KeyGen |
| 0x164 | EncryptDecrypt |
| 0x165 | FlushContext |
| 0x167 | LoadExternal |
| 0x168 | MakeCredential |
| 0x169 | NV_ReadPublic |
| 0x16A | PolicyAuthorize |
| 0x16B | PolicyAuthValue |
| 0x16C | PolicyCommandCode |
| 0x16D | PolicyCounterTimer |
| 0x16E | PolicyCpHash |
| 0x16F | PolicyLocality |
| 0x170 | PolicyNameHash |
| 0x171 | PolicyOR |
| 0x172 | PolicyTicket |
| 0x173 | ReadPublic |
| 0x174 | RSA_Encrypt |
| 0x176 | StartAuthSession |
| 0x177 | VerifySignature |
| 0x178 | ECC_Parameters |
| 0x17A | GetCapability |
| 0x17B | GetRandom |
| 0x17C | GetTestResult |
| 0x17D | Hash |
| 0x17E | PCR_Read |
| 0x17F | PolicyPCR |
| 0x180 | PolicyRestart |
| 0x181 | ReadClock |
| 0x182 | PCR_Extend |
| 0x183 | PCR_SetAuthValue |
| 0x184 | NV_Certify |
| 0x185 | EventSequenceComplete |
| 0x186 | HashSequenceStart |
| 0x187 | PolicyPhysicalPresence |
| 0x188 | PolicyDuplicationSelect |
| 0x189 | PolicyGetDigest |
| 0x18A | TestParms |
| 0x18B | Commit |
| 0x18C | PolicyPassword |
| 0x18D | ZGen_2Phase |
| 0x18E | EC_Ephemeral |
| 0x18F | PolicyNvWritten |
| 0x190 | PolicyTemplate |
| 0x191 | CreateLoaded |
| 0x192 | PolicyAuthorizeNV |
| 0x193 | EncryptDecrypt2 |
| 0x197 |  |

### ECC curves

| ID | Name |
|---|---|
| 0x0001 | NIST P192 |
| 0x0002 | NIST P224 |
| 0x0003 | NIST P256 |
| 0x0004 | NIST P384 |
| 0x0005 | NIST P521 |
| 0x0010 | BN P256 |
| 0x0011 | BN P638 |
| 0x0020 | SM2 P256 |

### Handles in use

| Type | Handle |
|---|---|
| NV index | 0x01C00002 |
| NV index | 0x01C00016 |
| persistent object | 0x81010001 |
| persistent object | 0x81010016 |

### PCR bank sha1

| PCR | Typical use | Value |
|---|---|---|
| 0 | firmware code | 0000000000000000000000000000000000000000 |
| 1 | firmware config | 0000000000000000000000000000000000000000 |
| 2 | option ROM code | 0000000000000000000000000000000000000000 |
| 3 | option ROM config | 0000000000000000000000000000000000000000 |
| 4 | boot loader | 0000000000000000000000000000000000000000 |
| 5 | boot config / GPT | 0000000000000000000000000000000000000000 |
| 6 | platform specific | 0000000000000000000000000000000000000000 |
| 7 | Secure Boot policy | 0000000000000000000000000000000000000000 |
| 8 | GRUB commands | 0000000000000000000000000000000000000000 |
| 9 | files read by boot loader | 0000000000000000000000000000000000000000 |
| 10 | Linux IMA | 0000000000000000000000000000000000000000 |
| 11 |  | 0000000000000000000000000000000000000000 |
| 12 |  | 0000000000000000000000000000000000000000 |
| 13 |  | 0000000000000000000000000000000000000000 |
| 14 | shim MOK | 0000000000000000000000000000000000000000 |
| 15 |  | 0000000000000000000000000000000000000000 |
| 16 | debug (resettable) | 0000000000000000000000000000000000000000 |
| 17 |  | ffffffffffffffffffffffffffffffffffffffff |
| 18 |  | ffffffffffffffffffffffffffffffffffffffff |
| 19 |  | ffffffffffffffffffffffffffffffffffffffff |
| 20 |  | ffffffffffffffffffffffffffffffffffffffff |
| 21 |  | ffffffffffffffffffffffffffffffffffffffff |
| 22 |  | ffffffffffffffffffffffffffffffffffffffff |
| 23 | application (resettable) | 0000000000000000000000000000000000000000 |

### PCR bank sha256

| PCR | Typical use | Value |
|---|---|---|
| 0 | firmware code | 3d458cfe55cc03ea1f443f1562beec8df51c75e14a9fcf9a7234a13f198e7969 |
| 1 | firmware config | 3d458cfe55cc03ea1f443f1562beec8df51c75e14a9fcf9a7234a13f198e7969 |
| 2 | option ROM code | 3d458cfe55cc03ea1f443f1562beec8df51c75e14a9fcf9a7234a13f198e7969 |
| 3 | option ROM config | 3d458cfe55cc03ea1f443f1562beec8df51c75e14a9fcf9a7234a13f198e7969 |
| 4 | boot loader | 3d458cfe55cc03ea1f443f1562beec8df51c75e14a9fcf9a7234a13f198e7969 |
| 5 | boot config / GPT | 3d458cfe55cc03ea1f443f1562beec8df51c75e14a9fcf9a7234a13f198e7969 |
| 6 | platform specific | 3d458cfe55cc03ea1f443f1562beec8df51c75e14a9fcf9a7234a13f198e7969 |
| 7 | Secure Boot policy | 3d458cfe55cc03ea1f443f1562beec8df51c75e14a9fcf9a7234a13f198e7969 |
| 8 | GRUB commands | 0000000000000000000000000000000000000000000000000000000000000000 |
| 9 | files read by boot loader | 0000000000000000000000000000000000000000000000000000000000000000 |
| 10 | Linux IMA | 0000000000000000000000000000000000000000000000000000000000000000 |
| 11 |  | 0000000000000000000000000000000000000000000000000000000000000000 |
| 12 |  | 0000000000000000000000000000000000000000000000000000000000000000 |
| 13 |  | 0000000000000000000000000000000000000000000000000000000000000000 |
| 14 | shim MOK | 0000000000000000000000000000000000000000000000000000000000000000 |
| 15 |  | 0000000000000000000000000000000000000000000000000000000000000000 |
| 16 | debug (resettable) | 0000000000000000000000000000000000000000000000000000000000000000 |
| 17 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 18 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 19 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 20 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 21 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 22 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 23 | application (resettable) | 0000000000000000000000000000000000000000000000000000000000000000 |

### PCR bank sha384

| PCR | Typical use | Value |
|---|---|---|
| 0 | firmware code | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 1 | firmware config | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 2 | option ROM code | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 3 | option ROM config | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 4 | boot loader | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 5 | boot config / GPT | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 6 | platform specific | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 7 | Secure Boot policy | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 8 | GRUB commands | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 9 | files read by boot loader | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 10 | Linux IMA | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 11 |  | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 12 |  | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 13 |  | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 14 | shim MOK | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 15 |  | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 16 | debug (resettable) | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |
| 17 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 18 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 19 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 20 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 21 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 22 |  | ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff |
| 23 | application (resettable) | 000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000 |

### Boot event log

| # | PCR | Type | SHA-256 digest | Data bytes |
|---|---|---|---|---|
| 0 | 0 | EV_SEPARATOR | df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119 | 4 |
| 1 | 1 | EV_SEPARATOR | df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119 | 4 |
| 2 | 2 | EV_SEPARATOR | df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119 | 4 |
| 3 | 3 | EV_SEPARATOR | df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119 | 4 |
| 4 | 4 | EV_SEPARATOR | df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119 | 4 |
| 5 | 5 | EV_SEPARATOR | df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119 | 4 |
| 6 | 6 | EV_SEPARATOR | df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119 | 4 |
| 7 | 7 | EV_SEPARATOR | df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119 | 4 |

### Key parameter support

| Parameters | Result |
|---|---|
| RSA-1024 | supported |
| RSA-2048 | supported |
| RSA-3072 | supported |
| RSA-4096 | not supported: TPM_RC 0x1C4 (VALUE) from command 0x18A |
| AES-128-CFB | supported |
| AES-192-CFB | not supported: TPM_RC 0x1C4 (VALUE) from command 0x18A |
| AES-256-CFB | supported |

### RSA EK certificate

| Field | Value |
|---|---|
| Subject | CN=unknown |
| Issuer | CN=swtpm-localca |
| Serial | 1 |
| Not before | 2026-11-25T14:16:24Z |
| Not after | 9999-12-31T23:59:59Z |
| Signature algorithm | SHA256-RSA |
| SHA-256 fingerprint | 981efe824705ebdf5ffb52013061ef68ddf041470d2d8b7bb2641cd73bc37c83 |
| Issuer URLs | [] |

## What this report does not prove

- It runs on the machine under test. A compromised OS or kernel can falsify every result. For real attestation, a remote verifier must choose the nonce and check the quote and EK chain off the host.
- The kernel resource manager (/dev/tpmrm0) hides other processes' transient objects and sessions, so the handle list is partial.
- Without `-ek-ca`, the TPM's manufacturer identity is not verified.
- Event log replay shows the log is consistent with the PCRs, not that the measured components are ones you trust. Compare against known-good values.
- It does not test physical tamper resistance, TPM bus interposition, or certification claims.
