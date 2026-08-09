package imscore

import (
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/1239t/vowifi-go/internal/vowifi/policy"
)

func TestBuildSecurityVerifyFromChallengeEchoesRawSecurityServer(t *testing.T) {
	raw := `IPSEC-3GPP;Q=0.7;PORT-S=5063;SPI-S=222;PORT-C=5062;SPI-C=111;EALG=NULL;ALG=HMAC-SHA-1-96;PROT=ESP;MODE=TRANSPORT,ipsec-3gpp;alg=hmac-md5-96;ealg=null;spi-c=333;spi-s=444;port-c=5064;port-s=5065;q=0.1`
	res := sip.NewResponse(401, "Unauthorized")
	res.AppendHeader(sip.NewHeader("Security-Server", raw))
	cfg := Config{Template: policy.DefaultGiffgaffTemplate()}
	got, selected, err := buildSecurityVerifyFromChallenge(cfg, res)
	if err != nil {
		t.Fatalf("buildSecurityVerifyFromChallenge() error = %v", err)
	}
	if got != raw {
		t.Fatalf("Security-Verify = %q, want raw %q", got, raw)
	}
	if selected == nil || selected.SPIC != 333 || selected.PortS != 5065 {
		t.Fatalf("selected offer = %+v", selected)
	}
}
