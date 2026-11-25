// Command tpmverify inventories and tests a TPM 2.0 and writes a Markdown report.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() { os.Exit(run()) }

// run returns 0 (no failures), 1 (at least one check failed) or 2 (could not run).
func run() int {
	device := flag.String("device", "", "TPM device (default: /dev/tpmrm0, then /dev/tpm0)")
	tcp := flag.String("tcp", "", "host:port of a software TPM raw command socket, for testing")
	out := flag.String("out", "", "report path (default: tpm-report-<host>-<time>.md)")
	caDir := flag.String("ek-ca", "", "directory of TPM manufacturer root/intermediate certificates (PEM or DER)")
	ekCert := flag.String("ek-cert", "", "EK certificate file (PEM or DER) when the TPM does not store one")
	logPath := flag.String("eventlog", "/sys/kernel/security/tpm0/binary_bios_measurements", "boot event log")
	nvWrite := flag.Bool("nv-write", false, "also test NV define/write/read/delete (writes TPM flash)")
	readOnly := flag.Bool("read-only", false, "inventory, PCR read and event log only: create no keys, change no PCRs")
	noColor := flag.Bool("no-color", false, "disable colored output")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments:", flag.Args())
		return 2
	}

	st, _ := os.Stdout.Stat()
	r := &Report{out: os.Stdout, color: !*noColor && st != nil && st.Mode()&os.ModeCharDevice != 0}
	host, _ := os.Hostname()
	now := time.Now()
	if *out == "" {
		*out = fmt.Sprintf("tpm-report-%s-%s.md", host, now.Format("20060102-150405"))
	}
	fmt.Println("tpmverify: TPM 2.0 inventory and verification")

	hostInfo(r)
	deviceInfo(r)

	code := 0
	t, err := OpenTPM(*device, *tcp)
	r.Meta = [][]string{{"Host", host}, {"Generated", now.Format(time.RFC3339)}, {"Effective UID", fmt.Sprint(os.Geteuid())}}
	if err != nil {
		r.Add("Open TPM", Fail, "%v (run with sudo; check the TPM is enabled)", err)
		code = 2
	} else {
		defer t.Close()
		r.Meta = append(r.Meta, []string{"TPM interface", t.Path},
			[]string{"Mode", fmt.Sprintf("read-only=%v nv-write=%v ek-ca=%q", *readOnly, *nvWrite, *caDir)})
		r.Add("Open TPM", Pass, "opened %s", t.Path)
		if caps, err := capabilities(r, t); err != nil {
			code = 2
		} else {
			pcrDump(r, t, caps)
			eventLogTests(r, t, caps, *logPath)
			if *readOnly {
				r.Section("Active tests")
				r.Add("Key, PCR, seal and attestation tests", Skip, "disabled by -read-only")
			} else {
				basicTests(r, t, caps)
				pcrOps(r, t, caps)
				cryptoTests(r, t, caps)
				quoteTests(r, t, caps)
				ekTests(r, t, caps, *caDir, *ekCert)
				sealTests(r, t, caps)
				nvTest(r, t, caps, *nvWrite)
				lockoutUnchanged(r, t, caps.Props[0x20E])
			}
		}
	}

	// 0600: the report holds device identifiers. O_EXCL: never overwrite or follow a link.
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = f.WriteString(r.Markdown())
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		return 2
	}
	fmt.Printf("\nPASS %d  FAIL %d  WARN %d  SKIP %d\nReport: %s\n",
		r.Count(Pass), r.Count(Fail), r.Count(Warn), r.Count(Skip), *out)
	if code == 0 && r.Count(Fail) > 0 {
		code = 1
	}
	return code
}
