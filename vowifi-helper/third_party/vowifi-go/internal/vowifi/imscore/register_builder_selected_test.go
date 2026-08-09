package imscore

import (
	"strings"
	"testing"

	"github.com/1239t/vowifi-go/internal/vowifi/imsheaders"
)

func TestBuildSelectedSecurityClientUsesNegotiatedMechanism(t *testing.T) {
	got := buildSelectedSecurityClient(&imsheaders.SecurityOffer{
		SecurityMechanism: imsheaders.SecurityMechanism{
			Alg:  "hmac-sha-1-96",
			EAlg: "aes-cbc",
			Mode: "trans",
		},
	}, 101, 202, 5064, 5063)

	for _, want := range []string{
		"alg=hmac-sha-1-96",
		"ealg=aes-cbc",
		"prot=esp",
		"mod=trans",
		"spi-c=101",
		"spi-s=202",
		"port-c=5064",
		"port-s=5063",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Security-Client = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, ",") {
		t.Fatalf("Security-Client = %q, want one selected mechanism", got)
	}
}
