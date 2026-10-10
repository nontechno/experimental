package main

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func syntheticSNPReport(version uint32) []byte {
	rep := make([]byte, snpReportLen)
	le := binary.LittleEndian
	le.PutUint32(rep[0x00:], version)
	le.PutUint32(rep[0x04:], 7)
	// policy: ABI 1.55, SMT allowed, reserved bit 17, DEBUG bit 19
	le.PutUint64(rep[0x08:], 55|1<<8|1<<16|1<<17|1<<19)
	le.PutUint32(rep[0x34:], 1)
	le.PutUint64(rep[0x38:], uint64(7)<<56|uint64(10)<<48|uint64(3)<<8|4)
	le.PutUint64(rep[0x40:], 3) // SMT + TSME
	le.PutUint32(rep[0x48:], 4) // signing key = 1 (VLEK)
	for i := 0; i < 48; i++ {
		rep[0x90+i] = byte(i)
	}
	rep[0x1E8], rep[0x1E9], rep[0x1EA] = 21, 55, 1 // current build, minor, major
	le.PutUint64(rep[0x1E0:], 0x0102)
	return rep
}

func TestParseSNPReport(t *testing.T) {
	r, err := parseSNPReport(syntheticSNPReport(2))
	if err != nil {
		t.Fatal(err)
	}
	pol := r["policy"].(map[string]any)
	if pol["abiMajor"].(uint8) != 1 || pol["abiMinor"].(uint8) != 55 {
		t.Errorf("abi: %v", pol)
	}
	if !pol["debug"].(bool) || !pol["smtAllowed"].(bool) || pol["migrateMA"].(bool) {
		t.Errorf("policy bits: %v", pol)
	}
	tcb := r["currentTcb"].(map[string]any)
	if tcb["bootLoader"].(uint8) != 4 || tcb["tee"].(uint8) != 3 || tcb["snp"].(uint8) != 10 || tcb["microcode"].(uint8) != 7 {
		t.Errorf("tcb: %v", tcb)
	}
	if r["flags"].(map[string]any)["signingKey"] != "VLEK" {
		t.Errorf("flags: %v", r["flags"])
	}
	pi := r["platformInfo"].(map[string]any)
	if !pi["smtEnabled"].(bool) || !pi["tsmeEnabled"].(bool) {
		t.Errorf("platformInfo: %v", pi)
	}
	if !strings.HasPrefix(r["measurement"].(string), "000102") || len(r["measurement"].(string)) != 96 {
		t.Errorf("measurement: %v", r["measurement"])
	}
	if r["signatureAlgo"] != "ecdsa-p384-sha384" {
		t.Errorf("sigalgo: %v", r["signatureAlgo"])
	}
	if r["currentFirmware"] != "1.55.21" {
		t.Errorf("firmware: %v", r["currentFirmware"])
	}
	if _, ok := r["cpuidFamilyId"]; ok {
		t.Error("v2 report must not have cpuid fields")
	}
	if _, err := json.Marshal(r); err != nil {
		t.Errorf("not JSON-serialisable: %v", err)
	}
}

func TestParseSNPReportV3AndShort(t *testing.T) {
	rep := syntheticSNPReport(3)
	rep[0x188], rep[0x189], rep[0x18A] = 0x19, 0x01, 0x02
	r, err := parseSNPReport(rep)
	if err != nil {
		t.Fatal(err)
	}
	if r["cpuidFamilyId"].(uint8) != 0x19 || r["cpuidModelId"].(uint8) != 1 || r["cpuidStepping"].(uint8) != 2 {
		t.Errorf("cpuid: %v %v %v", r["cpuidFamilyId"], r["cpuidModelId"], r["cpuidStepping"])
	}
	if _, err := parseSNPReport(make([]byte, 100)); err == nil {
		t.Error("expected error for short report")
	}
}

func TestScanCCKeys(t *testing.T) {
	raw := json.RawMessage(`{
		"shape":"VM.Standard.E4.Flex",
		"platformConfig":{"isSecureBootEnabled":true,"isMemoryEncryptionEnabled":true},
		"launchOptions":{"isTrustedPlatformModuleEnabled":false},
		"metadata":{"user_data":"c2VjcmV0","ssh_authorized_keys":"ssh-ed25519 AAAA"},
		"freeformTags":{"env":"test"}
	}`)
	got := scanCCKeys(raw)
	if _, ok := got["platformConfig"]; !ok {
		t.Errorf("platformConfig missing: %v", got)
	}
	if v, ok := got["launchOptions.isTrustedPlatformModuleEnabled"]; !ok || v != false {
		t.Errorf("tpm flag missing: %v", got)
	}
	for k := range got {
		if strings.Contains(k, "user_data") || strings.Contains(k, "ssh") || k == "shape" {
			t.Errorf("unexpected key %q", k)
		}
	}
	if scanCCKeys(nil) != nil || scanCCKeys(json.RawMessage(`not json`)) != nil {
		t.Error("bad input should return nil")
	}
}

func TestDecodeCaps(t *testing.T) {
	got := decodeCaps("0000000000000003")
	if len(got) != 2 || got[0] != "CAP_CHOWN" || got[1] != "CAP_DAC_OVERRIDE" {
		t.Errorf("caps: %v", got)
	}
}
