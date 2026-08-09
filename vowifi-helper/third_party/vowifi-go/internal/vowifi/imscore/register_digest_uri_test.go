package imscore

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"

	"github.com/1239t/vowifi-go/engine/sim"
)

type digestURIAKAProvider struct{}

func (digestURIAKAProvider) CalculateAKA(_, _ []byte) (sim.AKAResult, error) {
	return sim.AKAResult{
		RES: []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88},
		CK:  make([]byte, 16),
		IK:  make([]byte, 16),
	}, nil
}

func TestComputeAKAAuthUsesFullRegisterRequestURI(t *testing.T) {
	recipient := sip.Uri{}
	if err := sip.ParseUri("sip:ims.mnc010.mcc234.3gppnetwork.org", &recipient); err != nil {
		t.Fatalf("ParseUri() error = %v", err)
	}
	req := sip.NewRequest(sip.REGISTER, recipient)
	nonceBytes := append(make([]byte, 16), make([]byte, 16)...)
	challenge := &digest.Challenge{
		Realm:     "ims.mnc010.mcc234.3gppnetwork.org",
		Nonce:     base64.StdEncoding.EncodeToString(nonceBytes),
		Algorithm: "AKAv1-MD5",
	}

	_, header, err := computeAKAAuth(Config{
		AKA:       digestURIAKAProvider{},
		PrivateID: "234100123456789@ims.mnc010.mcc234.3gppnetwork.org",
	}, challenge, req)
	if err != nil {
		t.Fatalf("computeAKAAuth() error = %v", err)
	}
	if !strings.Contains(header, `uri="sip:ims.mnc010.mcc234.3gppnetwork.org"`) {
		t.Fatalf("Authorization = %q, want full SIP Request-URI", header)
	}
}
