package voiceclient

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

func TestProbeSIPOptionsUsesExactPSIAndRegisteredRouting(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:              net.ParseIP("2001:db8::1"),
			LocalPort:            5060,
			PCSCFAddr:            "[2001:db8::2]:5060",
			ServiceRoutes:        []string{"<sip:service-route.ims.example;lr>"},
			PublicURI:            "sip:subscriber@ims.example",
			SMSPreferredIdentity: "tel:+447700900123",
			HomeDomain:           "ims.example",
			Transport:            "tcp",
		},
		registerProfile: RegisterProfile{UserAgent: "probe-test", LocationHeaderMode: "cellular_only"},
	}
	var observed *sip.Request
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		observed = request.Clone()
		response := sip.NewResponse(200, "OK")
		response.SetSource("[2001:db8::2]:5060")
		response.AppendHeader(sip.NewHeader("Via", "SIP/2.0/TCP [2001:db8::1]:5060"))
		response.AppendHeader(sip.NewHeader("Server", "example-ims"))
		response.AppendHeader(sip.NewHeader("P-Asserted-Service", "urn:urn-7:3gpp-service.ims.icsi.sms"))
		return response, nil
	}

	result, err := client.ProbeSIPOptions(context.Background(), SIPOptionsProbeRequest{
		TargetURI:          "tel:+447802002606",
		LocationHeaderMode: "pani_only",
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed == nil || observed.Method != sip.OPTIONS || observed.Recipient.String() != "tel:+447802002606" {
		t.Fatalf("observed request = %v", observed)
	}
	if got := observed.GetHeader("To"); got == nil || got.Value() != "<tel:+447802002606>" {
		t.Fatalf("To = %v", got)
	}
	if routes := observed.GetHeaders("Route"); len(routes) != 2 {
		t.Fatalf("Route count = %d", len(routes))
	}
	if observed.GetHeader("P-Access-Network-Info") == nil || observed.GetHeader("Cellular-Network-Info") != nil {
		t.Fatalf("unexpected location headers: PANI=%v cellular=%v", observed.GetHeader("P-Access-Network-Info"), observed.GetHeader("Cellular-Network-Info"))
	}
	if got := observed.GetHeader("P-Preferred-Identity"); got == nil || got.Value() != "<tel:+447700900123>" {
		t.Fatalf("P-Preferred-Identity = %v", got)
	}
	if result.ResponseCode != 200 || result.ResponseViaCount != 1 || !result.ServerPresent || result.ServerSHA256 == "" {
		t.Fatalf("probe result = %+v", result)
	}
	if result.RouteMode != "registered" || result.RouteCount != 2 || result.ConfiguredServiceRoutes != 1 {
		t.Fatalf("probe routing result = %+v", result)
	}
	if result.PAssertedService != "urn:urn-7:3gpp-service.ims.icsi.sms" {
		t.Fatalf("P-Asserted-Service = %q", result.PAssertedService)
	}
}

func TestProbeRouteModes(t *testing.T) {
	client := &Client{cfg: Config{
		PCSCFAddr:     "[2001:db8::2]:5060",
		ServiceRoutes: []string{"<sip:route-a.ims.example;lr>", " ", "<sip:route-b.ims.example;lr>"},
	}}
	tests := map[string]int{
		"":                   3,
		"registered":         3,
		"pcscf_only":         1,
		"service_route_only": 2,
		"none":               0,
	}
	for requested, wantCount := range tests {
		request := sip.NewRequest(sip.MESSAGE, sip.Uri{Scheme: "sip", Host: "ims.example"})
		request.AppendHeader(sip.NewHeader("Route", "<sip:stale.example;lr>"))
		request.AppendHeader(sip.NewHeader("Route", "<sip:also-stale.example;lr>"))
		gotMode, err := client.applyProbeRoutes(request, requested)
		if err != nil {
			t.Fatalf("mode %q: %v", requested, err)
		}
		wantMode := requested
		if wantMode == "" {
			wantMode = "registered"
		}
		if gotMode != wantMode || len(request.GetHeaders("Route")) != wantCount {
			t.Fatalf("mode %q normalized=%q route_count=%d", requested, gotMode, len(request.GetHeaders("Route")))
		}
	}
	if got := client.configuredServiceRouteCount(); got != 2 {
		t.Fatalf("configured service route count=%d", got)
	}
	request := sip.NewRequest(sip.MESSAGE, sip.Uri{Scheme: "sip", Host: "ims.example"})
	if _, err := client.applyProbeRoutes(request, "invalid"); err == nil {
		t.Fatal("invalid route mode unexpectedly accepted")
	}
}

func TestProbeSIPOptionsRejectsImplicitOrInvalidTarget(t *testing.T) {
	client := &Client{}
	for _, target := range []string{"", "+447802002606", "mailto:test@example.com"} {
		if _, err := client.ProbeSIPOptions(context.Background(), SIPOptionsProbeRequest{TargetURI: target}); err == nil {
			t.Fatalf("target %q unexpectedly accepted", target)
		}
	}
}

func TestProbeSIPEmptyMessageHasNoRPDataBody(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:              net.ParseIP("2001:db8::1"),
			LocalPort:            5060,
			PCSCFAddr:            "[2001:db8::2]:5060",
			PublicURI:            "sip:subscriber@ims.example",
			SMSPreferredIdentity: "tel:+447700900123",
			HomeDomain:           "ims.example",
			Transport:            "tcp",
		},
		registerProfile: RegisterProfile{LocationHeaderMode: "cellular_only"},
	}
	var observed *sip.Request
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		observed = request.Clone()
		return sip.NewResponse(400, "Bad Request"), nil
	}

	result, err := client.ProbeSIPEmptyMessage(context.Background(), SIPOptionsProbeRequest{
		TargetURI:            "sip:+447802002606@ims.example;user=phone",
		LocationHeaderMode:   "cellular_only",
		SMSServiceHeaders:    true,
		OmitTransferEncoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed == nil {
		t.Fatal("MESSAGE probe was not sent")
	}
	if observed.Method != sip.MESSAGE || len(observed.Body()) != 0 {
		t.Fatalf("MESSAGE probe method=%s body=%x", observed.Method, observed.Body())
	}
	if got := observed.GetHeader("Content-Type"); got == nil || got.Value() != smsContentType {
		t.Fatalf("Content-Type = %v", got)
	}
	if got := observed.GetHeader("P-Preferred-Service"); got == nil || got.Value() != smsPreferredService {
		t.Fatalf("P-Preferred-Service = %v", got)
	}
	if got := observed.GetHeader("Accept-Contact"); got == nil || got.Value() != smsAcceptContact {
		t.Fatalf("Accept-Contact = %v", got)
	}
	if got := observed.GetHeader("Content-Transfer-Encoding"); got != nil {
		t.Fatalf("Content-Transfer-Encoding = %v", got)
	}
	if result.RequestMethod != "MESSAGE" || result.BodyBytes != 0 || result.ResponseCode != 400 || !result.PPreferredServicePresent || !result.AcceptContactPresent || result.TransferEncodingPresent {
		t.Fatalf("probe result = %+v", result)
	}
}

func TestProbeSIPEmptyMessageCanOmitSMSServiceHeaders(t *testing.T) {
	client := &Client{cfg: Config{
		LocalIP:              net.ParseIP("2001:db8::1"),
		LocalPort:            5060,
		PCSCFAddr:            "[2001:db8::2]:5060",
		PublicURI:            "sip:subscriber@ims.example",
		SMSPreferredIdentity: "tel:+447700900123",
		HomeDomain:           "ims.example",
		Transport:            "tcp",
	}}
	var observed *sip.Request
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		observed = request.Clone()
		return sip.NewResponse(202, "Accepted"), nil
	}

	result, err := client.ProbeSIPEmptyMessage(context.Background(), SIPOptionsProbeRequest{
		TargetURI:         "sip:+447802002606@ims.example;user=phone",
		PayloadProfile:    "rp_submit_unroutable",
		SMSServiceHeaders: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed == nil {
		t.Fatal("MESSAGE probe was not sent")
	}
	if got := observed.GetHeader("P-Preferred-Service"); got != nil {
		t.Fatalf("P-Preferred-Service = %v", got)
	}
	if got := observed.GetHeader("Accept-Contact"); got != nil {
		t.Fatalf("Accept-Contact = %v", got)
	}
	if result.PPreferredServicePresent || result.AcceptContactPresent {
		t.Fatalf("probe result = %+v", result)
	}
}

func TestProbeSIPEmptyMessageStrictMinimalMatchesTS24341Example(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:           net.ParseIP("2001:db8::1"),
			LocalPort:         5060,
			PCSCFAddr:         "[2001:db8::2]:5060",
			ServiceRoutes:     []string{"<sip:orig@scscf.ims.example;lr>"},
			PublicURI:         "sip:register@ims.example",
			PreferredIdentity: "sip:associated@ims.example",
			HomeDomain:        "ims.example",
			SecurityVerify:    "ipsec-3gpp;alg=hmac-md5-96",
			Transport:         "tcp",
		},
		registerProfile: RegisterProfile{
			LocationHeaderMode: "both",
			UserAgent:          "diagnostic-user-agent",
		},
	}
	var observed *sip.Request
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		observed = request.Clone()
		return sip.NewResponse(401, "Unauthorized"), nil
	}

	result, err := client.ProbeSIPEmptyMessage(context.Background(), SIPOptionsProbeRequest{
		TargetURI:             "sip:+447802002606@ims.example;user=phone",
		PayloadProfile:        "rp_submit_unroutable",
		PreferredIdentityMode: "associated",
		SMSServiceHeaders:     true,
		StrictMinimalHeaders:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed == nil {
		t.Fatal("MESSAGE probe was not sent")
	}
	for _, name := range []string{
		"Contact", "Allow", "Accept", "P-Preferred-Service", "Accept-Contact",
		"Security-Verify", "P-Access-Network-Info", "Cellular-Network-Info",
		"User-Agent", "Content-Transfer-Encoding",
	} {
		if got := observed.GetHeader(name); got != nil {
			t.Fatalf("strict-minimal %s = %v", name, got)
		}
	}
	for _, name := range []string{"From", "To", "Call-ID", "CSeq", "Max-Forwards", "P-Preferred-Identity", "Content-Type"} {
		if got := observed.GetHeader(name); got == nil {
			t.Fatalf("strict-minimal %s is missing", name)
		}
	}
	if got := observed.From().Address.String(); got != "sip:associated@ims.example" {
		t.Fatalf("strict-minimal From = %q", got)
	}
	if result.RouteCount != 2 || !result.StrictMinimalHeaders || result.TransferEncodingPresent ||
		result.PAccessNetworkInfoPresent || result.CellularNetworkInfoPresent ||
		result.PPreferredServicePresent || result.AcceptContactPresent ||
		result.RequestContactPresent || result.RequestAllowPresent || result.RequestAcceptPresent ||
		result.RequestUserAgentPresent || result.RequestSecurityVerify {
		t.Fatalf("strict-minimal result = %+v", result)
	}
}

func TestProbeSIPEmptyMessageCanExerciseRegisteredFlowRecovery(t *testing.T) {
	client := newSMSFlowTestClient()
	client.cfg.SMSC = "+447802002606"
	transactions := 0
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		transactions++
		switch transactions {
		case 1:
			if request.Method != sip.MESSAGE {
				t.Fatalf("first method=%s, want MESSAGE", request.Method)
			}
			return sip.NewResponse(sip.StatusUnauthorized, "Unauthorized"), nil
		case 2:
			if request.Method != sip.REGISTER {
				t.Fatalf("second method=%s, want REGISTER", request.Method)
			}
			return smsFlowChallenge(), nil
		case 3:
			response := sip.NewResponse(sip.StatusOK, "OK")
			response.AppendHeader(sip.NewHeader("P-Associated-URI", `<sip:network-default@ims.example>, <tel:+18005550123>`))
			return response, nil
		case 4:
			if request.Method != sip.MESSAGE || request.GetHeader("Authorization") == nil {
				t.Fatalf("recovered request method=%s authorization=%v", request.Method, request.GetHeader("Authorization"))
			}
			return sip.NewResponse(sip.StatusAccepted, "Accepted"), nil
		default:
			t.Fatalf("unexpected transaction %d", transactions)
			return nil, nil
		}
	}

	result, err := client.ProbeSIPEmptyMessage(context.Background(), SIPOptionsProbeRequest{
		TargetURI:              "sip:+447802002606@ims.example;user=phone",
		PayloadProfile:         "rp_submit_unroutable",
		StrictMinimalHeaders:   true,
		RegisteredFlowRecovery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseCode != sip.StatusAccepted || !result.RegisteredFlowRecovery || transactions != 4 {
		t.Fatalf("result=%+v transactions=%d", result, transactions)
	}
}

func TestProbeSIPEmptyMessageCanUseRealWLANNodePANI(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:       net.ParseIP("2001:db8::1"),
			LocalPort:     5060,
			PCSCFAddr:     "[2001:db8::2]:5060",
			PublicURI:     "sip:subscriber@ims.example",
			HomeDomain:    "ims.example",
			SMSC:          "+447802002606",
			Transport:     "tcp",
			ServiceRoutes: []string{"<sip:orig@scscf.ims.example;lr>"},
		},
		registerProfile: RegisterProfile{LocationHeaderMode: "cellular_only"},
	}
	var observed *sip.Request
	client.transactionHook = func(_ context.Context, request *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		observed = request.Clone()
		return sip.NewResponse(sip.StatusUnauthorized, "Unauthorized"), nil
	}

	result, err := client.ProbeSIPEmptyMessage(context.Background(), SIPOptionsProbeRequest{
		TargetURI:               "sip:+447802002606@ims.example;user=phone",
		LocationHeaderMode:      "cellular_only",
		PayloadProfile:          "rp_submit_unroutable",
		PAccessNetworkInfoValue: "IEEE-802.11;i-wlan-node-id=001122334455",
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed == nil {
		t.Fatal("MESSAGE probe was not sent")
	}
	if got := observed.GetHeader("P-Access-Network-Info"); got == nil || got.Value() != "IEEE-802.11;i-wlan-node-id=001122334455" {
		t.Fatalf("P-Access-Network-Info=%v", got)
	}
	if got := observed.GetHeader("Cellular-Network-Info"); got != nil {
		t.Fatalf("Cellular-Network-Info=%v", got)
	}
	if result.LocationHeaderMode != "custom_pani" || !result.PAccessNetworkInfoPresent || result.CellularNetworkInfoPresent {
		t.Fatalf("result=%+v", result)
	}
}

func TestSafeProbeMessageBodiesCannotContainDeliverableTPDU(t *testing.T) {
	client := &Client{cfg: Config{SMSC: "+447802002606"}}
	tests := map[string][]byte{
		"empty":                nil,
		"rp_empty":             {0x00, 0x01, 0x00, 0x00, 0x00},
		"rp_smsc_only":         {0x00, 0x01, 0x00, 0x07, 0x91, 0x44, 0x87, 0x20, 0x00, 0x62, 0x60, 0x00},
		"rp_invalid_tpdu":      {0x00, 0x01, 0x00, 0x07, 0x91, 0x44, 0x87, 0x20, 0x00, 0x62, 0x60, 0x01, 0x00},
		"rp_submit_marker":     {0x00, 0x01, 0x00, 0x07, 0x91, 0x44, 0x87, 0x20, 0x00, 0x62, 0x60, 0x01, 0x01},
		"rp_submit_srr_marker": {0x00, 0x01, 0x00, 0x07, 0x91, 0x44, 0x87, 0x20, 0x00, 0x62, 0x60, 0x01, 0x21},
		"rp_submit_minimal":    {0x00, 0x01, 0x00, 0x07, 0x91, 0x44, 0x87, 0x20, 0x00, 0x62, 0x60, 0x03, 0x01, 0x01, 0x00},
		"rp_submit_bad_address": {
			0x00, 0x01, 0x00, 0x07, 0x91, 0x44, 0x87, 0x20, 0x00, 0x62, 0x60,
			0x08, 0x01, 0x01, 0x01, 0x91, 0xff, 0x00, 0x00, 0x00,
		},
		"rp_submit_srr_bad_address": {
			0x00, 0x01, 0x00, 0x07, 0x91, 0x44, 0x87, 0x20, 0x00, 0x62, 0x60,
			0x08, 0x21, 0x01, 0x01, 0x91, 0xff, 0x00, 0x00, 0x00,
		},
		"rp_submit_unroutable": {
			0x00, 0x01, 0x00, 0x07, 0x91, 0x44, 0x87, 0x20, 0x00, 0x62, 0x60,
			0x0d, 0x21, 0x01, 0x0b, 0x91, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x00, 0x00, 0x00,
		},
	}
	for profile, want := range tests {
		got, normalized, err := client.safeProbeMessageBody(profile)
		if err != nil {
			t.Fatalf("profile %s: %v", profile, err)
		}
		if normalized != profile || string(got) != string(want) {
			t.Fatalf("profile %s: normalized=%q body=%x want=%x", profile, normalized, got, want)
		}
	}
}

func TestProbePreferredIdentityModes(t *testing.T) {
	client := &Client{cfg: Config{
		PublicURI:            "sip:register@ims.example",
		PreferredIdentity:    "sip:associated@ims.example",
		SMSPreferredIdentity: "tel:+447700900123",
	}}
	tests := map[string]string{
		"":             "tel:+447700900123",
		"sms":          "tel:+447700900123",
		"associated":   "sip:associated@ims.example",
		"register_aor": "sip:register@ims.example",
	}
	for mode, want := range tests {
		request := sip.NewRequest(sip.MESSAGE, sip.Uri{Scheme: "sip", User: "smsc", Host: "ims.example"})
		request.AppendHeader(sip.NewHeader("From", "<sip:original@ims.example>;tag=test"))
		gotMode, err := client.applyProbePreferredIdentity(request, mode)
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if mode == "" && gotMode != "sms" || mode != "" && gotMode != mode {
			t.Fatalf("mode %q normalized to %q", mode, gotMode)
		}
		if got := request.From().Address.String(); got != want {
			t.Fatalf("mode %q From=%q want=%q", mode, got, want)
		}
		if got := request.GetHeader("P-Preferred-Identity"); got == nil || got.Value() != "<"+want+">" {
			t.Fatalf("mode %q P-Preferred-Identity=%v", mode, got)
		}
	}
	mixed := sip.NewRequest(sip.MESSAGE, sip.Uri{Scheme: "sip", Host: "ims.example"})
	mixed.AppendHeader(sip.NewHeader("From", "<sip:original@ims.example>;tag=test"))
	if gotMode, err := client.applyProbePreferredIdentity(mixed, "associated_from_sms_ppi"); err != nil {
		t.Fatal(err)
	} else if gotMode != "associated_from_sms_ppi" {
		t.Fatalf("mixed mode normalized to %q", gotMode)
	}
	if got := mixed.From().Address.String(); got != "sip:associated@ims.example" {
		t.Fatalf("mixed From=%q", got)
	}
	if got := mixed.GetHeader("P-Preferred-Identity"); got == nil || got.Value() != "<tel:+447700900123>" {
		t.Fatalf("mixed P-Preferred-Identity=%v", got)
	}
	request := sip.NewRequest(sip.MESSAGE, sip.Uri{Scheme: "sip", Host: "ims.example"})
	if _, err := client.applyProbePreferredIdentity(request, "invalid"); err == nil {
		t.Fatal("invalid preferred identity mode unexpectedly accepted")
	}
}

func TestProbeAuthorizationModes(t *testing.T) {
	client := &Client{cfg: Config{
		PrivateID:  "subscriber@ims.example",
		HomeDomain: "ims.example",
		PreemptiveAuth: &DigestAuthSession{
			Challenge:   digest.Challenge{Realm: "ims.example", Nonce: "nonce", Algorithm: "MD5"},
			AKAResponse: []byte("response"),
			Username:    "subscriber@ims.example",
		},
	}}
	tests := map[string]struct {
		header    string
		digestURI string
	}{
		"session":           {header: "Authorization", digestURI: "sip:smsc@ims.example"},
		"proxy":             {header: "Proxy-Authorization", digestURI: "sip:smsc@ims.example"},
		"home_domain":       {header: "Authorization", digestURI: "sip:ims.example"},
		"proxy_home_domain": {header: "Proxy-Authorization", digestURI: "sip:ims.example"},
	}
	for mode, want := range tests {
		request := sip.NewRequest(sip.MESSAGE, sip.Uri{Scheme: "sip", User: "smsc", Host: "ims.example"})
		gotMode, err := client.applyProbeAuthorization(request, false, mode)
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if gotMode != mode {
			t.Fatalf("mode %q normalized to %q", mode, gotMode)
		}
		header := request.GetHeader(want.header)
		if header == nil || !strings.Contains(header.Value(), `uri="`+want.digestURI+`"`) {
			t.Fatalf("mode %q %s=%v", mode, want.header, header)
		}
	}
	request := sip.NewRequest(sip.MESSAGE, sip.Uri{Scheme: "sip", Host: "ims.example"})
	if _, err := client.applyProbeAuthorization(request, false, "invalid"); err == nil {
		t.Fatal("invalid preemptive authorization mode unexpectedly accepted")
	}
}

func TestBuildSelfProbeRPDataProducesValidLocalSubmit(t *testing.T) {
	client := &Client{cfg: Config{
		SMSPreferredIdentity: "tel:+447724304579",
		SMSC:                 "+447802002606",
	}}
	body, err := client.buildSelfProbeRPData("rp_submit_self")
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 26 {
		t.Fatalf("body length = %d, want 26: %x", len(body), body)
	}
	if body[0] != 0x00 || body[2] != 0x00 || body[3] != 0x07 || body[11] != 0x0e {
		t.Fatalf("RP-DATA framing = %x", body)
	}
	// TPDU starts after RP type/MR/OA, seven-byte SMSC value and RP-UDL.
	tpdu := body[12:]
	if tpdu[0] != 0x01 || tpdu[2] != 12 || tpdu[3] != 0x91 || tpdu[len(tpdu)-2] != 0x01 || tpdu[len(tpdu)-1] != 0x41 {
		t.Fatalf("SMS-SUBMIT TPDU = %x", tpdu)
	}

	srr, err := client.buildSelfProbeRPData("rp_submit_self_srr")
	if err != nil {
		t.Fatal(err)
	}
	if srr[12]&0x20 == 0 {
		t.Fatalf("TP-SRR missing: %x", srr)
	}

	noRPDA, err := client.buildSelfProbeRPData("rp_submit_self_no_rpda")
	if err != nil {
		t.Fatal(err)
	}
	if noRPDA[3] != 0x00 {
		t.Fatalf("RP-DA is not empty: %x", noRPDA)
	}

	client.cfg.SMSPreferredIdentity = "sip:+447724304579@ims.example"
	if _, err := client.buildSelfProbeRPData("rp_submit_self"); err == nil {
		t.Fatal("non-tel associated identity unexpectedly accepted for self probe")
	}
}
