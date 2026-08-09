package imscore

import "testing"

func TestServiceStatusExposesPhoneNumberWithoutPrivateIdentity(t *testing.T) {
	service := &Service{
		cfg: Config{
			IMSI:      "234100123456789",
			PrivateID: "234100123456789@ims.example",
		},
		phoneNumber: "+447700900123",
	}
	status := service.Status()
	if got, ok := status["phone_number"].(string); !ok || got != "+447700900123" {
		t.Fatalf("phone number = %#v", status["phone_number"])
	}
	for _, key := range []string{"imsi", "private_id", "preferred_identity"} {
		if _, present := status[key]; present {
			t.Fatalf("private identity field %q present in status", key)
		}
	}
}

func TestSecurityModeReflectsWhetherSecurityAgreementWasInstalled(t *testing.T) {
	plain := (&Service{}).Status()
	if got := plain["sip_security_mode"]; got != "" {
		t.Fatalf("zero-value service security mode = %#v", got)
	}

	service := &Service{ipsecInstalled: false, sipSecurityMode: securityModeLabel(false)}
	if got := service.Status()["sip_security_mode"]; got != "plain" {
		t.Fatalf("plain security mode = %#v", got)
	}

	service.ipsecInstalled = true
	service.sipSecurityMode = securityModeLabel(true)
	if got := service.Status()["sip_security_mode"]; got != "ipsec3gpp" {
		t.Fatalf("IPsec security mode = %#v", got)
	}
}
