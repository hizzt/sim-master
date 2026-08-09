package imscore

import (
	"net"
	"strings"
	"testing"

	"github.com/emiago/sipgo/sip"

	"github.com/1239t/vowifi-go/internal/vowifi/ipsec3gpp"
)

func TestPrepareProtectedRegisterRequestReplacesViaAndPreservesDialogHeaders(t *testing.T) {
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: "o2.co.uk"})
	req.AppendHeader(sip.NewHeader("Via", "SIP/2.0/TCP [2001:db8::1]:5060;branch=z9hG4bK-old"))
	req.AppendHeader(sip.NewHeader("Call-ID", "register-call-id"))
	req.AppendHeader(sip.NewHeader("CSeq", "38140 REGISTER"))

	cfg := Config{LocalIP: net.ParseIP("2001:db8::1")}
	state := registerState{ipsecPolicy: ipsec3gpp.Policy{LocalPortC: 5064}}
	if err := prepareProtectedRegisterRequest(cfg, state, req); err != nil {
		t.Fatal(err)
	}

	via := req.GetHeader("Via")
	if via == nil {
		t.Fatal("Via header missing")
	}
	if got := via.Value(); !strings.HasPrefix(got, "SIP/2.0/TCP [2001:db8::1]:5064;branch=z9hG4bK") || strings.Contains(got, "old") {
		t.Fatalf("Via = %q", got)
	}
	if got := req.GetHeader("Call-ID"); got == nil || got.Value() != "register-call-id" {
		t.Fatalf("Call-ID = %v", got)
	}
	if got := req.GetHeader("CSeq"); got == nil || got.Value() != "38140 REGISTER" {
		t.Fatalf("CSeq = %v", got)
	}
}

func TestPrepareProtectedRegisterRequestRequiresPortC(t *testing.T) {
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: "o2.co.uk"})
	if err := prepareProtectedRegisterRequest(Config{}, registerState{}, req); err == nil {
		t.Fatal("expected missing protected port-c error")
	}
}
