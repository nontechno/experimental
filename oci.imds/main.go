// oci.imds queries the OCI Instance Metadata Service (IMDS) and saves everything
// it can read into a single JSON file. It also fingerprints the environment it
// runs in (DMI, CPU, hypervisor, container, confidential-computing features,
// DNS/DHCP/NTP hints, cloud-init) and probes other clouds' metadata endpoints.
//
// Usage:
//
//	oci.imds                          # writes ./oci.imds.json
//	oci.imds -o out.json              # custom output path ("-" = stdout)
//	oci.imds -identity                # also collect instance-principal cert + intermediate
//	oci.imds -include-private-key     # ALSO collect the instance-principal private key (sensitive!)
//	oci.imds -decode-user-data        # add base64-decoded cloud-init user_data
//	oci.imds -path /instance/shape    # probe extra IMDS paths (repeatable)
//	oci.imds -env-only                # local environment fingerprint only, no OCI IMDS
//	oci.imds -no-env                  # OCI IMDS only, skip the environment fingerprint
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultBase = "http://169.254.169.254"
	toolVersion = "1.1.0"
)

type endpoint struct {
	Name string
	Path string
}

// Endpoints that return structured data on OCI IMDS.
var coreEndpoints = []endpoint{
	{"instance", "/instance/"},
	{"vnics", "/vnics/"},
	{"volumeAttachments", "/volumeAttachments/"},
	{"host", "/host/"}, // not present on every shape; 404 is tolerated
}

var identityEndpoints = []endpoint{
	{"identity/cert.pem", "/identity/cert.pem"},
	{"identity/intermediate.pem", "/identity/intermediate.pem"},
}

var privateKeyEndpoint = endpoint{"identity/key.pem", "/identity/key.pem"}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

type output struct {
	Tool            string                     `json:"tool"`
	CollectedAt     string                     `json:"collectedAt"`
	BaseURL         string                     `json:"baseUrl"`
	IMDSVersion     int                        `json:"imdsVersion,omitempty"`
	Data            map[string]json.RawMessage `json:"data"`
	DecodedUserData *string                    `json:"decodedUserData,omitempty"`
	IdentityCert    *certInfo                  `json:"identityCert,omitempty"`
	Environment     *Environment               `json:"environment,omitempty"`
	Errors          map[string]string          `json:"errors,omitempty"`
}

type client struct {
	http    *http.Client
	base    string
	version int
	retries int
}

// get performs a GET with retries on transport errors, 429 and 5xx.
// It returns the body and HTTP status. A non-nil error means no usable HTTP response.
func (c *client) get(ctx context.Context, path string) ([]byte, int, error) {
	url := fmt.Sprintf("%s/opc/v%d%s", c.base, c.version, path)
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, err
		}
		if c.version == 2 {
			req.Header.Set("Authorization", "Bearer Oracle")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		return body, resp.StatusCode, nil
	}
	return nil, 0, lastErr
}

// toRaw converts a response body into JSON: valid JSON is kept as-is,
// anything else (PEM, plain text) becomes a JSON string.
func toRaw(body []byte) json.RawMessage {
	trimmed := strings.TrimSpace(string(body))
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	b, _ := json.Marshal(trimmed)
	return b
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func decodeUserData(instance json.RawMessage) (string, bool) {
	var inst struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(instance, &inst); err != nil {
		return "", false
	}
	ud, ok := inst.Metadata["user_data"].(string)
	if !ok || ud == "" {
		return "", false
	}
	dec, err := base64.StdEncoding.DecodeString(ud)
	if err != nil {
		return "", false
	}
	return string(dec), true
}

func writeFile(path string, data []byte) error {
	if path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".oci.imds-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func main() { os.Exit(run()) }

func run() int {
	var (
		outPath    = flag.String("o", "oci.imds.json", "output file path (\"-\" for stdout)")
		base       = flag.String("base", defaultBase, "OCI IMDS base URL (override for testing)")
		timeout    = flag.Duration("timeout", 3*time.Second, "per-request timeout for OCI IMDS")
		total      = flag.Duration("total-timeout", 30*time.Second, "overall timeout for the OCI IMDS phase")
		retries    = flag.Int("retries", 2, "retries per OCI IMDS request on transport error / 429 / 5xx")
		version    = flag.String("imds-version", "auto", "OCI IMDS version: auto, 1 or 2")
		identity   = flag.Bool("identity", false, "also collect instance-principal cert and intermediate cert")
		privateKey = flag.Bool("include-private-key", false, "ALSO collect the instance-principal PRIVATE KEY (implies -identity)")
		decodeUD   = flag.Bool("decode-user-data", false, "add base64-decoded user_data to the output")
		compact    = flag.Bool("compact", false, "emit compact JSON instead of indented")
		quiet      = flag.Bool("q", false, "suppress progress output on stderr")

		noEnv        = flag.Bool("no-env", false, "skip the local environment fingerprint")
		envOnly      = flag.Bool("env-only", false, "only fingerprint the local environment; do not query OCI IMDS")
		attest       = flag.Bool("attest", true, "fetch the AMD SEV-SNP attestation report from /dev/sev-guest when present")
		attestDev    = flag.String("attest-device", "/dev/sev-guest", "SNP guest device used by -attest")
		attestNonce  = flag.String("attest-nonce", "", "hex string (up to 64 bytes) embedded as REPORT_DATA in the attestation report")
		noExec       = flag.Bool("no-exec", false, "never run external commands (systemd-detect-virt)")
		probeClouds  = flag.Bool("probe-clouds", true, "probe AWS/GCP/Azure/DigitalOcean/Hetzner/Alibaba metadata endpoints")
		probeTimeout = flag.Duration("probe-timeout", 1500*time.Millisecond, "per-request timeout for other-cloud probes")
		probeBudget  = flag.Duration("probe-budget", 5*time.Second, "overall cap for the whole other-cloud probe phase")
		envTimeout   = flag.Duration("env-timeout", 10*time.Second, "overall timeout for the local environment fingerprint (separate from -total-timeout)")
		probeBase    = flag.String("probe-base", defaultBase, "base URL for AWS/GCP/Azure/DO/Hetzner probes (testing)")
		probeAli     = flag.String("probe-alibaba-base", "http://100.100.100.200", "base URL for the Alibaba probe (testing)")
		root         = flag.String("root", "/", "filesystem root for local probing of /sys, /proc, /etc (testing)")
		extra        stringList
	)
	flag.Var(&extra, "path", "extra OCI IMDS path to fetch, e.g. /instance/shape (repeatable)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "oci.imds %s - dump OCI instance metadata and environment fingerprint to JSON\n\nUsage: %s [flags]\n\n", toolVersion, os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	logf := func(format string, a ...any) {
		if !*quiet {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}

	if *noEnv && *envOnly {
		fmt.Fprintln(os.Stderr, "error: -no-env and -env-only are mutually exclusive")
		return 2
	}
	for name, d := range map[string]time.Duration{
		"-timeout": *timeout, "-total-timeout": *total, "-probe-timeout": *probeTimeout,
		"-probe-budget": *probeBudget, "-env-timeout": *envTimeout,
	} {
		if d <= 0 {
			fmt.Fprintf(os.Stderr, "error: %s must be positive\n", name)
			return 2
		}
	}
	if *retries < 0 || *retries > 10 {
		fmt.Fprintln(os.Stderr, "error: -retries must be between 0 and 10")
		return 2
	}
	var nonce []byte
	if *attestNonce != "" {
		b, err := hex.DecodeString(*attestNonce)
		if err != nil || len(b) > 64 {
			fmt.Fprintln(os.Stderr, "error: -attest-nonce must be a hex string of at most 64 bytes")
			return 2
		}
		nonce = b
	}
	if *privateKey {
		*identity = true
	}
	switch *version {
	case "auto", "1", "2":
	default:
		fmt.Fprintln(os.Stderr, "error: -imds-version must be auto, 1 or 2")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *total)
	defer cancel()

	out := output{
		Tool:        "oci.imds " + toolVersion,
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		BaseURL:     strings.TrimRight(*base, "/"),
		Data:        map[string]json.RawMessage{},
		Errors:      map[string]string{},
	}

	// ------------------------------------------------------------ OCI IMDS
	imdsOK := false
	if !*envOnly {
		// IMDS is link-local; never route it through an HTTP proxy.
		c := &client{
			http: &http.Client{Timeout: *timeout, Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: *timeout}).DialContext,
				ResponseHeaderTimeout: *timeout,
				IdleConnTimeout:       5 * time.Second,
			}},
			base:    out.BaseURL,
			retries: *retries,
		}
		imdsOK = collectIMDS(ctx, c, &out, *version, *identity, *privateKey, *decodeUD, extra, logf)
	}

	if !*envOnly && !imdsOK && *noEnv {
		return 1 // nothing useful to write
	}

	// ------------------------------------------------------------ environment
	if !*noEnv {
		logf("collecting local environment fingerprint")
		// Own deadline: a slow or hung IMDS must not starve the fingerprint.
		envCtx, envCancel := context.WithTimeout(context.Background(), *envTimeout)
		defer envCancel()
		env := collectEnvironment(envCtx, envOptions{
			Root:             *root,
			Probe:            *probeClouds,
			ProbeTimeout:     *probeTimeout,
			ProbeBudget:      *probeBudget,
			ProbeBase:        *probeBase,
			ProbeAlibabaBase: *probeAli,
			RunCommands:      !*noExec,
			Attest:           *attest,
			AttestDevice:     *attestDev,
			AttestNonce:      nonce,
		})
		finalizeVerdict(env, !*envOnly, imdsOK, out.Data["instance"])
		out.Environment = env
		v := env.Verdict
		logf("environment: provider=%s (%s) virtualization=%s container=%s confidential=%s",
			v.Provider, v.Confidence, v.Virtualization, v.ContainerRuntime, strings.Join(v.ConfidentialComputing, ","))
	}

	if len(out.Errors) == 0 {
		out.Errors = nil
	}

	var (
		enc []byte
		err error
	)
	if *compact {
		enc, err = json.Marshal(out)
	} else {
		enc, err = json.MarshalIndent(out, "", "  ")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: encoding JSON: %v\n", err)
		return 1
	}
	enc = append(enc, '\n')

	if err := writeFile(*outPath, enc); err != nil {
		fmt.Fprintf(os.Stderr, "error: writing %s: %v\n", *outPath, err)
		return 1
	}

	if *outPath != "-" {
		keys := make([]string, 0, len(out.Data))
		for k := range out.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		logf("saved %d IMDS section(s) [%s]%s to %s", len(keys), strings.Join(keys, ", "),
			map[bool]string{true: " + environment", false: ""}[out.Environment != nil], *outPath)
	}

	exit := 0
	if !*envOnly && !imdsOK {
		exit = 1 // file was still written with the local fingerprint
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		logf("warning: -total-timeout reached; OCI IMDS data may be incomplete")
		exit = 1
	}
	return exit
}

// collectIMDS fills out.Data/out.Errors from the OCI IMDS. It returns false if
// the IMDS could not be reached or /instance/ did not return 200.
func collectIMDS(ctx context.Context, c *client, out *output, version string,
	identity, privateKey, decodeUD bool, extra []string, logf func(string, ...any)) bool {

	if version == "1" {
		c.version = 1
	} else {
		c.version = 2
	}

	body, status, err := c.get(ctx, "/instance/")
	if err != nil {
		msg := fmt.Sprintf("cannot reach OCI IMDS at %s: %v", c.base, err)
		fmt.Fprintf(os.Stderr, "error: %s\n(are you running on an OCI instance?)\n", msg)
		out.Errors["imds"] = msg
		return false
	}
	if status != http.StatusOK && version == "auto" {
		logf("IMDS v2 returned HTTP %d, trying v1", status)
		c.version = 1
		body, status, err = c.get(ctx, "/instance/")
		if err != nil {
			msg := fmt.Sprintf("IMDS v1 request failed: %v", err)
			fmt.Fprintf(os.Stderr, "error: %s\n", msg)
			out.Errors["imds"] = msg
			return false
		}
	}
	if status != http.StatusOK {
		msg := fmt.Sprintf("/instance/ returned HTTP %d: %s", status, snippet(body))
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
		out.Errors["imds"] = msg
		return false
	}
	logf("using IMDS v%d at %s", c.version, c.base)
	out.IMDSVersion = c.version
	out.Data["instance"] = toRaw(body)
	logf("  %-28s ok (%d bytes)", "instance", len(body))

	fetch := func(e endpoint) {
		body, status, err := c.get(ctx, e.Path)
		switch {
		case err != nil:
			out.Errors[e.Name] = err.Error()
			logf("  %-28s error: %v", e.Name, err)
		case status == http.StatusNotFound:
			out.Errors[e.Name] = "not available (HTTP 404)"
			logf("  %-28s not available", e.Name)
		case status != http.StatusOK:
			out.Errors[e.Name] = fmt.Sprintf("HTTP %d: %s", status, snippet(body))
			logf("  %-28s HTTP %d", e.Name, status)
		default:
			out.Data[e.Name] = toRaw(body)
			logf("  %-28s ok (%d bytes)", e.Name, len(body))
		}
	}

	for _, e := range coreEndpoints[1:] {
		fetch(e)
	}
	if identity {
		for _, e := range identityEndpoints {
			fetch(e)
		}
		if raw, ok := out.Data["identity/cert.pem"]; ok {
			var pemText string
			if json.Unmarshal(raw, &pemText) == nil {
				if ci, err := parseIdentityCert(pemText); err == nil {
					out.IdentityCert = ci
				} else {
					out.Errors["identityCert"] = "cannot parse identity/cert.pem: " + err.Error()
				}
			}
		}
	}
	if privateKey {
		logf("WARNING: collecting instance-principal private key; protect the output file")
		fetch(privateKeyEndpoint)
	}
	for _, p := range extra {
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		fetch(endpoint{Name: strings.TrimPrefix(p, "/"), Path: p})
	}

	if decodeUD {
		if ud, ok := decodeUserData(out.Data["instance"]); ok {
			out.DecodedUserData = &ud
		} else {
			out.Errors["decodedUserData"] = "no user_data found or not valid base64"
		}
	}
	return true
}
