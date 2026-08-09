package voiceclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"

	"github.com/1239t/vowifi-go/engine/sim"
)

type smsFlowAKA struct{}

func (smsFlowAKA) CalculateAKA(_, _ []byte) (sim.AKAResult, error) {
	return sim.AKAResult{
		RES: []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
		CK:  make([]byte, 16),
		IK:  make([]byte, 16),
	}, nil
}

func smsFlowChallenge() *sip.Response {
	nonceBytes := make([]byte, 32)
	for i := range nonceBytes {
		nonceBytes[i] = byte(i + 1)
	}
	response := sip.NewResponse(sip.StatusUnauthorized, "Unauthorized")
	response.AppendHeader(sip.NewHeader(
		"WWW-Authenticate",
		`Digest realm="ims.example", nonce="`+base64.StdEncoding.EncodeToString(nonceBytes)+`", algorithm=AKAv1-MD5`,
	))
	return response
}

func newSMSFlowTestClient() *Client {
	profile := LegacyVohiveRegisterProfile()
	config := Config{
		LocalIP:         net.ParseIP("192.0.2.10"),
		LocalPort:       12000,
		PCSCFAddr:       "192.0.2.20:5060",
		PrivateID:       "subscriber@ims.example",
		PublicURI:       "sip:subscriber@ims.example",
		HomeDomain:      "ims.example",
		Realm:           "ims.example",
		Transport:       "tcp",
		AKA:             smsFlowAKA{},
		RegisterProfile: profile,
	}
	return &Client{
		cfg:             config,
		registerProfile: profile,
		basePrivateID:   config.PrivateID,
		basePublicURI:   config.PublicURI,
		securityClient:  newSecurityClientState(),
	}
}

type smsFlowObservation struct {
	method             sip.RequestMethod
	authorization      bool
	proxyAuthorization bool
	from               string
	preferredIdentity  string
}

func observeSMSFlowRequest(request *sip.Request) smsFlowObservation {
	observation := smsFlowObservation{method: request.Method}
	observation.authorization = request.GetHeader("Authorization") != nil
	observation.proxyAuthorization = request.GetHeader("Proxy-Authorization") != nil
	if from := request.From(); from != nil {
		observation.from = from.Address.String()
	}
	if preferred := request.GetHeader("P-Preferred-Identity"); preferred != nil {
		observation.preferredIdentity = preferred.Value()
	}
	return observation
}

func TestSMSBare401RecoversRegisterAndRetriesWithFreshIdentityAndAuthorization(t *testing.T) {
	client := newSMSFlowTestClient()
	var observations []smsFlowObservation
	var postRegisterDiagnostics smsSubmitDiagnostics
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		observations = append(observations, observeSMSFlowRequest(request))
		switch len(observations) {
		case 1:
			return sip.NewResponse(sip.StatusUnauthorized, "Unauthorized"), nil
		case 2:
			return smsFlowChallenge(), nil
		case 3:
			response := sip.NewResponse(sip.StatusOK, "OK")
			response.AppendHeader(sip.NewHeader("P-Associated-URI", `<sip:network-default@ims.example>, <tel:+18005550123>`))
			return response, nil
		case 4:
			postRegisterDiagnostics = inspectSMSSubmit("post_register_retry", request, nil, nil)
			return sip.NewResponse(sip.StatusAccepted, "Accepted"), nil
		default:
			t.Fatalf("unexpected transaction %d", len(observations))
			return nil, nil
		}
	}

	request, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatal(err)
	}
	request.SetBody([]byte{0x01, 0x02, 0x03})
	request.AppendHeader(sip.NewHeader("Authorization", `Digest username="stale", nonce="stale"`))
	response, err := client.doSMSSubmitTransaction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != sip.StatusAccepted || len(observations) != 4 {
		t.Fatalf("response=%v observations=%d", response, len(observations))
	}
	if observations[0].method != sip.MESSAGE || observations[0].authorization || observations[0].proxyAuthorization {
		t.Fatalf("initial MESSAGE carried auth: %+v", observations[0])
	}
	if observations[1].method != sip.REGISTER || observations[2].method != sip.REGISTER || !observations[2].authorization {
		t.Fatalf("REGISTER recovery sequence=%+v", observations)
	}
	last := observations[3]
	if last.method != sip.MESSAGE || !last.authorization || last.proxyAuthorization {
		t.Fatalf("post-REGISTER MESSAGE did not carry fresh authorization: %+v", last)
	}
	if last.from != "sip:network-default@ims.example" || last.preferredIdentity != "<tel:+18005550123>" {
		t.Fatalf("post-REGISTER identity not refreshed: %+v", last)
	}
	if postRegisterDiagnostics.Phase != "post_register_retry" || !postRegisterDiagnostics.AuthorizationPresent ||
		postRegisterDiagnostics.AuthorizationHeader != "Authorization" || !postRegisterDiagnostics.DigestURIMatchesRequest {
		t.Fatalf("post-REGISTER diagnostics=%+v", postRegisterDiagnostics)
	}
}

func TestProtectedSMSBare401DoesNotAttemptPlainRegisterRecovery(t *testing.T) {
	client := newSMSFlowTestClient()
	client.cfg.SecurityVerify = "ipsec-3gpp;alg=hmac-sha-1-96"
	client.cfg.PreemptiveAuth = &DigestAuthSession{
		Challenge: digest.Challenge{
			Realm:     "ims.example",
			Nonce:     "register-nonce",
			Algorithm: "AKAv1-MD5",
		},
		AKAResponse: []byte("binary-res"),
		Username:    "subscriber@ims.example",
	}

	transactions := 0
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		transactions++
		if request.Method != sip.MESSAGE {
			t.Fatalf("method = %s, want MESSAGE", request.Method)
		}
		if request.GetHeader("Authorization") != nil || request.GetHeader("Proxy-Authorization") != nil {
			t.Fatal("protected MESSAGE unexpectedly carried preemptive authorization")
		}
		return sip.NewResponse(sip.StatusUnauthorized, "Unauthorized"), nil
	}

	request, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.doSMSSubmitTransaction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != sip.StatusUnauthorized || transactions != 1 {
		t.Fatalf("response=%v transactions=%d", response, transactions)
	}
}

func TestSMSAnswersMessageSpecificChallenge(t *testing.T) {
	client := newSMSFlowTestClient()
	var observations []smsFlowObservation
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		observations = append(observations, observeSMSFlowRequest(request))
		if len(observations) == 1 {
			return smsFlowChallenge(), nil
		}
		return sip.NewResponse(sip.StatusAccepted, "Accepted"), nil
	}

	request, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.doSMSSubmitTransaction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != sip.StatusAccepted || len(observations) != 2 {
		t.Fatalf("response=%v observations=%d", response, len(observations))
	}
	if observations[0].authorization || observations[0].proxyAuthorization || !observations[1].authorization {
		t.Fatalf("MESSAGE challenge auth sequence=%+v", observations)
	}
}

func TestSMSSecondBare401StopsAfterSingleRegisterRecovery(t *testing.T) {
	client := newSMSFlowTestClient()
	transactions := 0
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		transactions++
		switch transactions {
		case 1:
			return sip.NewResponse(sip.StatusUnauthorized, "Unauthorized"), nil
		case 2:
			return smsFlowChallenge(), nil
		case 3:
			return sip.NewResponse(sip.StatusOK, "OK"), nil
		case 4:
			if request.GetHeader("Authorization") == nil || request.GetHeader("Proxy-Authorization") != nil {
				t.Fatal("post-REGISTER MESSAGE must carry only fresh Authorization")
			}
			return sip.NewResponse(sip.StatusUnauthorized, "Unauthorized"), nil
		default:
			t.Fatalf("unexpected transaction %d", transactions)
			return nil, nil
		}
	}

	request, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.doSMSSubmitTransaction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != sip.StatusUnauthorized || transactions != 4 {
		t.Fatalf("response=%v transactions=%d", response, transactions)
	}
}

func TestSMSDiagnosticsAreSecretFree(t *testing.T) {
	client := newSMSFlowTestClient()
	client.cfg.PreferredIdentity = "sip:secret-imsi@ims.example"
	request, err := client.newSMSRequest("+8615556250521")
	if err != nil {
		t.Fatal(err)
	}
	request.SetBody([]byte("secret-rpdu-and-message"))
	request.AppendHeader(sip.NewHeader(
		"Authorization",
		`Digest username="secret-private", realm="ims.example", nonce="secret-nonce", uri="sip:+8615556250521@ims.example", algorithm=AKAv1-MD5, response="secret-response"`,
	))
	response := sip.NewResponse(sip.StatusUnauthorized, "Unauthorized")
	response.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest nonce="response-secret-nonce"`))

	diagnostics := inspectSMSSubmit("initial_challenge_1", request, response, nil)
	if !diagnostics.AuthorizationPresent || !diagnostics.AuthenticationChallenge || !diagnostics.DigestURIMatchesRequest {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	rendered := fmt.Sprintf("%+v", diagnostics)
	for _, secret := range []string{
		"+8615556250521", "secret-imsi", "secret-private", "secret-nonce",
		"secret-response", "response-secret-nonce", "secret-rpdu-and-message",
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, rendered)
		}
	}
}
