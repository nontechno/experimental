# tpmverify

TPM 2.0 inventory and verification tool for Linux (written for Oracle Linux on AMD).
Standard library only: no modules to download.

## Build and run

    sudo dnf install -y go-toolset make     # Go 1.21 or newer
    make test build
    sudo ./tpmverify

If `make` or Go is unavailable on the target, build elsewhere and copy the static binary:

    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o tpmverify .

## Flags

| Flag | Meaning |
|---|---|
| `-read-only` | Inventory, PCR values and event log only. Creates no keys, changes no PCRs |
| `-nv-write` | Also define, write, read and delete a temporary NV index (writes TPM flash) |
| `-ek-ca DIR` | Validate EK certificates against the manufacturer root/intermediates in DIR |
| `-ek-cert FILE` | Use this EK certificate when the TPM does not store one |
| `-eventlog FILE` | Boot event log (default: the kernel's securityfs file) |
| `-device PATH` | TPM device (default `/dev/tpmrm0`, then `/dev/tpm0`) |
| `-out FILE` | Report path. Existing files are never overwritten |
| `-no-color` | Plain console output |
| `-tcp HOST:PORT` | Software TPM (swtpm) raw socket, for development |

Exit code: 0 no failures, 1 a check failed, 2 could not run.

## What it checks

- Host: UEFI, Secure Boot, Setup Mode, lockdown, virtualization flag, device nodes, sysfs
- TPM: identity, firmware, all properties, state flags, lockout, algorithms, commands, curves, PCR banks, handles
- PCRs in every bank, and replay of the boot event log against them
- Self test, RNG smoke test, clock, hashing against software, key size support
- PCR extend/reset; RSA and ECC keygen, sign, verify, decrypt
- Quotes (nonce, tamper, PCR digest), verified in software
- EK certificates, chain, key match, credential activation
- Seal/unseal bound to a PCR, including refusal after the PCR changes

## Side effects

Unless `-read-only` is given:

- PCR 16 (debug) is reset, extended and reset again. Do not run if something relies on it.
- Transient keys and sessions are created and flushed. RSA keygen can take several seconds.
- It never clears the TPM, changes passwords or persists keys.
- If a hierarchy has a password, the tests needing it are reported as SKIP.

## Reading results

WARN means review it, not necessarily broken (for example, no lockout password).
SKIP means **not verified**. The report holds device identifiers (EK certificate), so it is
written with mode 0600 and owned by root under sudo.

## Testing status

Developed and exercised against swtpm on Ubuntu with Go 1.24. Not yet run on Oracle Linux,
on an AMD firmware TPM, on a discrete TPM, through `/dev/tpmrm0`, or against a real
firmware event log. `sample-report.md` is from the swtpm run.
