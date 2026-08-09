package carrier

import (
	"os"
	"testing"
	"time"
)

func TestResolveIMSRegisterProfileXiaomiMi11(t *testing.T) {
	t.Cleanup(ClearCarrierOverrides)
	path := t.TempDir() + "/carrier_overrides.json"
	payload := `[{"mcc":"234","mnc":"10","ims_register_profile":"xiaomi_mi11","phone_imei":"869988776655443"}]`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}
	if _, err := LoadCarrierOverrides(path); err != nil {
		t.Fatalf("LoadCarrierOverrides() error = %v", err)
	}
	got := ResolveIMSRegisterProfile("234", "10")
	if got.Profile.ContactFeatures != "phone_xiaomi" {
		t.Fatalf("profile = %q", got.Profile.ContactFeatures)
	}
	if got.SIPInstanceURN != "urn:gsma:imei:86998877-665544-0" {
		t.Fatalf("sip instance = %q", got.SIPInstanceURN)
	}
	if got.RegisterExpiry != 600000*time.Second {
		t.Fatalf("register expiry = %v", got.RegisterExpiry)
	}
}

func TestResolveIMSRegisterProfileSimAdminGBEE(t *testing.T) {
	t.Cleanup(ClearCarrierOverrides)
	path := t.TempDir() + "/carrier_overrides.json"
	payload := `[{"mcc":"234","mnc":"10","ims_register_profile":"simadmin_gb_ee"}]`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}
	if _, err := LoadCarrierOverrides(path); err != nil {
		t.Fatalf("LoadCarrierOverrides() error = %v", err)
	}
	got := ResolveIMSRegisterProfile("234", "10")
	if got.Profile.VariantSet != "simadmin_gb_ee" {
		t.Fatalf("variant set = %q", got.Profile.VariantSet)
	}
	if got.Profile.UserAgent != "rmx3366 VoWiFi" {
		t.Fatalf("user agent = %q", got.Profile.UserAgent)
	}
}

func TestResolveIMSRegisterProfileLocationHeaderMode(t *testing.T) {
	t.Cleanup(ClearCarrierOverrides)
	path := t.TempDir() + "/carrier_overrides.json"
	payload := `[{"mcc":"234","mnc":"10","ims_location_header_mode":"pani_only"}]`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}
	if _, err := LoadCarrierOverrides(path); err != nil {
		t.Fatalf("LoadCarrierOverrides() error = %v", err)
	}
	got := ResolveIMSRegisterProfile("234", "10")
	if got.Profile.LocationHeaderMode != "pani_only" {
		t.Fatalf("location header mode = %q", got.Profile.LocationHeaderMode)
	}
}

func TestResolveIMSRegisterProfileRMX3366SIPInstance(t *testing.T) {
	t.Cleanup(ClearCarrierOverrides)
	path := t.TempDir() + "/carrier_overrides.json"
	payload := `[{"mcc":"234","mnc":"10","ims_register_profile":"rmx3366","phone_imei":"861234567890123"}]`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}
	if _, err := LoadCarrierOverrides(path); err != nil {
		t.Fatalf("LoadCarrierOverrides() error = %v", err)
	}
	got := ResolveIMSRegisterProfile("234", "10")
	if got.SIPInstanceURN != "urn:gsma:imei:86123456-789012-0" {
		t.Fatalf("sip instance = %q", got.SIPInstanceURN)
	}
}

func TestResolveIMSRegisterProfileSimAdminIOS(t *testing.T) {
	t.Cleanup(ClearCarrierOverrides)
	path := t.TempDir() + "/carrier_overrides.json"
	payload := `[{"mcc":"234","mnc":"10","ims_register_profile":"simadmin_ios"}]`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}
	if _, err := LoadCarrierOverrides(path); err != nil {
		t.Fatalf("LoadCarrierOverrides() error = %v", err)
	}
	got := ResolveIMSRegisterProfile("234", "10")
	if got.Profile.UserAgent != "iphone15,4_like VoWiFi" {
		t.Fatalf("user agent = %q", got.Profile.UserAgent)
	}
	if !got.Profile.IncludeRequireSecAgree {
		t.Fatal("expected require sec-agree for iOS profile")
	}
}

func TestResolveIMSRegisterProfilePixel7Pro(t *testing.T) {
	t.Cleanup(ClearCarrierOverrides)
	path := t.TempDir() + "/carrier_overrides.json"
	payload := `[{"mcc":"234","mnc":"10","ims_register_profile":"pixel7_pro","ims_location_header_mode":"cellular_only"}]`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}
	if _, err := LoadCarrierOverrides(path); err != nil {
		t.Fatalf("LoadCarrierOverrides() error = %v", err)
	}
	got := ResolveIMSRegisterProfile("234", "10")
	if got.Profile.UserAgent != "Google_Pixel7Pro_Android16_CP1A.260405.005" {
		t.Fatalf("user agent = %q", got.Profile.UserAgent)
	}
	if !got.Profile.IncludeRequireSecAgree || got.Profile.SecurityClientFormat != "phone_multi" {
		t.Fatalf("profile = %+v", got.Profile)
	}
	if got.Profile.LocationHeaderMode != "cellular_only" {
		t.Fatalf("location mode = %q", got.Profile.LocationHeaderMode)
	}
}
