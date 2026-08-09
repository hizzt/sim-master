package voiceclient

import "testing"

func TestAssociatedPhoneNumberUsesOnlyTelephoneIdentity(t *testing.T) {
	identities := []string{
		"sip:234100123456789@ims.example",
		"tel:+447700900123;phone-context=ims.example",
	}
	if got := AssociatedPhoneNumber(identities); got != "+447700900123" {
		t.Fatalf("phone number = %q", got)
	}
}

func TestAssociatedTelephoneIdentityReturnsNetworkTelURI(t *testing.T) {
	identities := []string{
		"sip:234100000000001@ims.example",
		"tel:+447700900123;phone-context=ims.example",
	}
	if got := AssociatedTelephoneIdentity(identities); got != "tel:+447700900123;phone-context=ims.example" {
		t.Fatalf("telephone identity = %q", got)
	}
}

func TestAssociatedPhoneNumberRejectsPrivateAndMalformedIdentities(t *testing.T) {
	identities := []string{
		"sip:+447700900123@ims.example;user=phone",
		"tel:not-a-number",
		"tel:+1234567890123456",
	}
	if got := AssociatedPhoneNumber(identities); got != "" {
		t.Fatalf("unexpected phone number = %q", got)
	}
}
