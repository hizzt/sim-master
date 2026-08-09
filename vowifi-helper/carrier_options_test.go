//go:build jane_vowifi

package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/jane-rui/vowifi-go/runtimehost/carrier"
)

func TestResolveCarrierRuntimeOptionsUsesKnownGBEEPCSCF(t *testing.T) {
	options, loaded, err := resolveCarrierRuntimeOptions("234", "10", "", t.TempDir()+"/missing.json")
	if err != nil || loaded {
		t.Fatalf("loaded=%t err=%v", loaded, err)
	}
	if !reflect.DeepEqual(options.PCSCFCandidates, gbEEPCSCFFallbacks) {
		t.Fatalf("P-CSCF = %+v", options.PCSCFCandidates)
	}
	if options.CellID != "" || options.CellIDSource != "unavailable" {
		t.Fatalf("cell = %q source=%q", options.CellID, options.CellIDSource)
	}
	if options.UserAgent != "SimAdmin VoWiFi" || options.RegisterProfile != "jane-rui-wire-ims" {
		t.Fatalf("profile = %+v", options)
	}
}

func TestResolveCarrierRuntimeOptionsAppliesLegacyOverride(t *testing.T) {
	path := t.TempDir() + "/carrier_overrides.json"
	payload := `[{
		"id":"giffgaff-phone","mcc":"234","mnc":"10",
		"ims_cell_id_mode":"carrier_only","ims_tac":4660,"ims_cell_id":36984440,
		"ims_register_profile":"phone-compatible","ims_user_agent":"test-phone",
		"ims_pcscf_addr":"[2001:db8::1]:5060"
	}]`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	options, loaded, err := resolveCarrierRuntimeOptions("234", "010", "11abd9b71fd", path)
	if err != nil || !loaded {
		t.Fatalf("loaded=%t err=%v", loaded, err)
	}
	if options.CellID != "12342345678" || options.CellIDSource != "carrier_default" {
		t.Fatalf("cell options = %+v", options)
	}
	if options.UserAgent != "test-phone" || options.RegisterProfile != "phone-compatible" {
		t.Fatalf("register options = %+v", options)
	}
	if !reflect.DeepEqual(options.PCSCFCandidates, []string{"[2001:db8::1]:5060"}) {
		t.Fatalf("P-CSCF = %+v", options.PCSCFCandidates)
	}
}

func TestResolveCarrierRuntimeOptionsPrefersLiveQMICell(t *testing.T) {
	options, _, err := resolveCarrierRuntimeOptions("234", "10", "11abd9b71fd", "")
	if err != nil {
		t.Fatal(err)
	}
	if options.CellID != "11ABD9B71FD" || options.CellIDSource != "qmi" {
		t.Fatalf("cell options = %+v", options)
	}
	wantPANI := "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=2341011ABD9B71FD;cell-info-age=0"
	if options.AccessInfo != wantPANI {
		t.Fatalf("PANI=%q want=%q", options.AccessInfo, wantPANI)
	}
}

func TestResolveCarrierRuntimeOptionsCanDisableCellID(t *testing.T) {
	path := t.TempDir() + "/carrier_overrides.json"
	if err := os.WriteFile(path, []byte(`[{
		"mcc":"234","mnc":"10","ims_cell_id_mode":"none"
	}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	options, _, err := resolveCarrierRuntimeOptions("234", "10", "11ABD9B71FD", path)
	if err != nil {
		t.Fatal(err)
	}
	if options.CellID != "" || options.CellIDSource != "disabled" || options.AccessInfo != "" {
		t.Fatalf("cell options = %+v", options)
	}
}

func TestApplyJaneCarrierProfilePublishesPANIAndPCSCF(t *testing.T) {
	carrier.ClearCarrierOverrides()
	t.Cleanup(carrier.ClearCarrierOverrides)
	options, _, err := resolveCarrierRuntimeOptions("234", "10", "11ABD9B71FD", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyJaneCarrierProfile("234100266805104", "234", "10", "epdg.example", options); err != nil {
		t.Fatal(err)
	}
	profile := carrier.IMSAccessProfileForSubscriber(carrier.IMSAccessProfileInput{
		IMSI: "234100266805104", MCC: "234", MNC: "10",
	})
	if profile.AccessNetworkInfo != options.AccessInfo || !reflect.DeepEqual(profile.PCSCFFQDNs, options.PCSCFCandidates) {
		t.Fatalf("Jane carrier profile = %+v", profile)
	}
}

func TestPermanentEAPNAIUsesThreeDigitMNC(t *testing.T) {
	got := permanentEAPNAI("234100266805104", "234", "10")
	want := "0234100266805104@nai.epc.mnc010.mcc234.3gppnetwork.org"
	if got != want {
		t.Fatalf("permanentEAPNAI()=%q want=%q", got, want)
	}
}
