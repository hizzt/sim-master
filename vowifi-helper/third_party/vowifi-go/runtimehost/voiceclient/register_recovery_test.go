package voiceclient

import (
	"errors"
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

func TestCommitRegisterSuccessRefreshesDigestAndRouting(t *testing.T) {
	client := &Client{cfg: Config{PreemptiveAuth: &DigestAuthSession{
		Challenge:   digest.Challenge{Nonce: "old-nonce"},
		AKAResponse: []byte("old-res"),
	}}}
	response := sip.NewResponse(200, "OK")
	response.AppendHeader(sip.NewHeader("Authentication-Info", `qop=auth, nextnonce="fresh-nonce"`))
	response.AppendHeader(sip.NewHeader("Service-Route", "<sip:route-a.ims.example;lr>"))
	response.AppendHeader(sip.NewHeader("Service-Route", "<sip:route-b.ims.example;lr>"))
	response.AppendHeader(sip.NewHeader("P-Associated-URI", `"Subscriber, One" <sip:preferred@ims.example>, <tel:+18005550123>`))
	refreshed := &DigestAuthSession{
		Challenge: digest.Challenge{
			Realm: "ims.example", Nonce: "challenge-nonce", Algorithm: "AKAv1-MD5",
			QOP: []string{"auth"},
		},
		AKAResponse: []byte("fresh-res"),
		Username:    "subscriber@ims.example",
	}

	client.commitRegisterSuccess(response, refreshed)
	auth := client.preemptiveAuthSnapshot()
	if auth == nil || auth.Challenge.Nonce != "fresh-nonce" || string(auth.AKAResponse) != "fresh-res" {
		t.Fatalf("refreshed auth=%+v", auth)
	}
	if len(client.cfg.ServiceRoutes) != 2 || client.cfg.ServiceRoutes[1] != "<sip:route-b.ims.example;lr>" {
		t.Fatalf("service routes=%v", client.cfg.ServiceRoutes)
	}
	if client.cfg.PreferredIdentity != "sip:preferred@ims.example" {
		t.Fatalf("preferred identity=%q", client.cfg.PreferredIdentity)
	}

	// Both commit and snapshot must deep-copy secret and slice fields.
	refreshed.AKAResponse[0] = 'X'
	refreshed.Challenge.QOP[0] = "auth-int"
	if string(client.preemptiveAuthSnapshot().AKAResponse) != "fresh-res" || client.preemptiveAuthSnapshot().Challenge.QOP[0] != "auth" {
		t.Fatal("digest session aliases caller-owned memory")
	}
}

func TestRegisteredFlowRecoveryNeededOnlyForTransportOrBareAuthFailure(t *testing.T) {
	if !registeredFlowRecoveryNeeded(nil, errors.Join(errors.New("write failed"), sip.ErrTransactionTransport)) {
		t.Fatal("transport close must recover registration")
	}
	bare := sip.NewResponse(401, "Unauthorized")
	if !registeredFlowRecoveryNeeded(bare, nil) {
		t.Fatal("bare 401 must recover registration")
	}
	challenged := sip.NewResponse(401, "Unauthorized")
	challenged.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="ims.example", nonce="abc"`))
	if registeredFlowRecoveryNeeded(challenged, nil) {
		t.Fatal("ordinary challenge is handled in-transaction, not by flow recovery")
	}
	if registeredFlowRecoveryNeeded(sip.NewResponse(487, "Request Terminated"), nil) {
		t.Fatal("unrelated final SIP response must not trigger registration recovery")
	}
}

func TestNewVoiceDigestAuthSessionRetainsProxyChallenge(t *testing.T) {
	challenge := &digest.Challenge{Realm: "ims.example", Nonce: "nonce", Domain: []string{"sip:ims.example"}, QOP: []string{"auth"}}
	response := sip.NewResponse(407, "Proxy Authentication Required")
	response.AppendHeader(sip.NewHeader("Proxy-Authenticate", `Digest realm="ims.example", nonce="nonce"`))
	auth := newVoiceDigestAuthSession(challenge, []byte("res"), "subscriber", response)
	if auth == nil || !auth.Proxy || auth.Username != "subscriber" {
		t.Fatalf("auth=%+v", auth)
	}
	challenge.Domain[0] = "changed"
	challenge.QOP[0] = "changed"
	if auth.Challenge.Domain[0] != "sip:ims.example" || auth.Challenge.QOP[0] != "auth" {
		t.Fatal("challenge slices were not cloned")
	}
}
