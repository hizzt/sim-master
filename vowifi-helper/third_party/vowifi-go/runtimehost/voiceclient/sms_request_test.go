package voiceclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"

	"github.com/1239t/vowifi-go/runtimehost/messaging"
)

type fallbackDeliveryStore struct {
	messageID string
	deviceID  string
	callID    string
	rpMR      int
}

func (s *fallbackDeliveryStore) CreateSMSDelivery(messageID, _, deviceID, _, _ string, _ int, _ time.Time) error {
	s.messageID = messageID
	s.deviceID = deviceID
	return nil
}

func (s *fallbackDeliveryStore) UpsertSMSDeliveryPart(messageID string, _ int, callID string, rpMR int, _ string, _ time.Time) error {
	if messageID != s.messageID {
		return messaging.ErrDeliveryNotFound
	}
	s.callID = callID
	s.rpMR = rpMR
	return nil
}

func (s *fallbackDeliveryStore) MarkSMSDeliveryPartReport(inReplyTo, callID, deviceID string, rpMR int, state string, _ int, _ int, _ string, _ time.Time) (messaging.DeliveryPartMatch, error) {
	if deviceID != s.deviceID || rpMR != s.rpMR || (inReplyTo != s.callID && callID != s.callID) {
		return messaging.DeliveryPartMatch{}, messaging.ErrDeliveryNotFound
	}
	return messaging.DeliveryPartMatch{MessageID: s.messageID, PartNo: 0, State: state}, nil
}

func (*fallbackDeliveryStore) RecomputeSMSDelivery(string, time.Time) error { return nil }
func (*fallbackDeliveryStore) UpdateSMSDeliveryState(string, string, string, int, time.Time) error {
	return nil
}
func (*fallbackDeliveryStore) GetSMSDeliveryStatus(string) (*messaging.DeliveryStatus, error) {
	return nil, messaging.ErrDeliveryNotFound
}

func TestSendSMSRoutesMessageToServiceCentrePSI(t *testing.T) {
	body, err := hex.DecodeString("00010007914487200062602001010d91685155260525f1000014d3743b486ea7dd20ebfb9a36a641f4f29c0e")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{cfg: Config{
		LocalIP:              net.ParseIP("192.0.2.10"),
		LocalPort:            12000,
		PCSCFAddr:            "192.0.2.20:5060",
		PublicURI:            "sip:subscriber@ims.mnc010.mcc234.3gppnetwork.org",
		SMSPreferredIdentity: "tel:+447724304579",
		HomeDomain:           "ims.mnc010.mcc234.3gppnetwork.org",
		SMSC:                 "+447802002606",
		Transport:            "tcp",
	}}
	var request *sip.Request
	client.transactionHook = func(_ context.Context, req *sip.Request, _ ...sipgo.ClientRequestOption) (*sip.Response, error) {
		request = req.Clone()
		return sip.NewResponse(sip.StatusAccepted, "Accepted"), nil
	}

	outcome, err := client.SendSMS(context.Background(), "+8615556250521", "test message", []messaging.SMSPart{{RPMR: 1, Body: body}})
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if outcome.PartsTotal != 1 || outcome.DeliveryState != "pending" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if request == nil {
		t.Fatal("MESSAGE was not sent")
	}
	const serviceCenterPSI = "sip:+447802002606@ims.mnc010.mcc234.3gppnetwork.org;user=phone"
	if got := request.Recipient.String(); got != serviceCenterPSI {
		t.Fatalf("request URI = %q, want %q", got, serviceCenterPSI)
	}
	if got := request.GetHeader("To"); got == nil || got.Value() != "<"+serviceCenterPSI+">" {
		t.Fatalf("To = %v", got)
	}
	if got := request.GetHeader("Content-Transfer-Encoding"); got == nil || got.Value() != "binary" {
		t.Fatalf("Content-Transfer-Encoding = %v", got)
	}
	if got := request.From(); got == nil || got.Address.String() != "sip:subscriber@ims.mnc010.mcc234.3gppnetwork.org" {
		t.Fatalf("From = %v", got)
	}
	if got := request.GetHeader("P-Preferred-Identity"); got == nil || got.Value() != "<tel:+447724304579>" {
		t.Fatalf("P-Preferred-Identity = %v", got)
	}
	if got := request.GetHeader("P-Preferred-Service"); got != nil {
		t.Fatalf("unexpected P-Preferred-Service = %v", got)
	}
	if got := request.GetHeader("Accept-Contact"); got != nil {
		t.Fatalf("unexpected Accept-Contact = %v", got)
	}
	if !bytes.Equal(request.Body(), body) {
		t.Fatalf("RP-DATA changed: got %x want %x", request.Body(), body)
	}
}

func TestSendSMSRequiresServiceCentre(t *testing.T) {
	client := &Client{cfg: Config{HomeDomain: "ims.example"}}
	_, err := client.SendSMS(context.Background(), "+18005551212", "hello", []messaging.SMSPart{{RPMR: 1, Body: []byte{0x00, 0x01}}})
	if err == nil || !strings.Contains(err.Error(), "service centre is required") {
		t.Fatalf("SendSMS error = %v", err)
	}
}

func TestSendSMSBare401RetriesSameRPDATAWithCellularPANIAndTracksRPACK(t *testing.T) {
	store := &fallbackDeliveryStore{}
	client := &Client{
		cfg: Config{
			LocalIP:        net.ParseIP("2001:db8::1"),
			LocalPort:      5064,
			PCSCFAddr:      "[2001:db8::2]:6060",
			PublicURI:      "sip:subscriber@ims.mnc010.mcc234.3gppnetwork.org",
			HomeDomain:     "ims.mnc010.mcc234.3gppnetwork.org",
			SMSC:           "+447802002606@o2.co.uk",
			CellID:         "11ABD9B71FD",
			Transport:      "tcp",
			SecurityVerify: "ipsec-3gpp;alg=hmac-sha-1-96",
			DeviceID:       "modem-1",
			DeliveryStore:  store,
		},
		registerProfile: RegisterProfile{LocationHeaderMode: "cellular_only"},
	}

	body := []byte{0x00, 0x55, 0x01, 0x02, 0x03, 0x04}
	var requests []*sip.Request
	var optionCounts []int
	client.transactionHook = func(_ context.Context, request *sip.Request, opts ...sipgo.ClientRequestOption) (*sip.Response, error) {
		requests = append(requests, request.Clone())
		optionCounts = append(optionCounts, len(opts))
		if len(requests) == 1 {
			return sip.NewResponse(sip.StatusUnauthorized, "Unauthorized"), nil
		}
		return sip.NewResponse(sip.StatusAccepted, "Accepted"), nil
	}

	outcome, err := client.SendSMS(context.Background(), "+447724304579", "A", []messaging.SMSPart{{RPMR: 0x55, Body: body}})
	if err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("MESSAGE count = %d, want exactly initial plus one retry", len(requests))
	}
	initial, retry := requests[0], requests[1]
	if !bytes.Equal(initial.Body(), body) || !bytes.Equal(retry.Body(), body) {
		t.Fatalf("RP-DATA changed: initial=%x retry=%x want=%x", initial.Body(), retry.Body(), body)
	}
	if initial.CallID().Value() != retry.CallID().Value() || store.callID != initial.CallID().Value() {
		t.Fatalf("Call-ID mismatch: initial=%q retry=%q store=%q", initial.CallID().Value(), retry.CallID().Value(), store.callID)
	}
	if cni := initial.GetHeader("Cellular-Network-Info"); cni == nil {
		t.Fatal("initial CNI-only MESSAGE lost Cellular-Network-Info")
	}
	if pani := initial.GetHeader("P-Access-Network-Info"); pani != nil {
		t.Fatalf("initial CNI-only MESSAGE unexpectedly has PANI: %v", pani)
	}
	if cni := retry.GetHeader("Cellular-Network-Info"); cni != nil {
		t.Fatalf("fallback MESSAGE retained Cellular-Network-Info: %v", cni)
	}
	const wantPANI = "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=2341011ABD9B71FD"
	if pani := retry.GetHeader("P-Access-Network-Info"); pani == nil || pani.Value() != wantPANI {
		t.Fatalf("fallback P-Access-Network-Info = %v, want %q", pani, wantPANI)
	}
	if initial.GetHeader("Authorization") != nil || retry.GetHeader("Authorization") != nil {
		t.Fatal("CNI/PANI flow unexpectedly added preemptive Authorization")
	}
	if optionCounts[0] != 0 || optionCounts[1] != 2 {
		t.Fatalf("transaction option counts = %v, want [0 2]", optionCounts)
	}
	if store.rpMR != 0x55 {
		t.Fatalf("DeliveryStore RP-MR = %d, want 85", store.rpMR)
	}
	match, err := store.MarkSMSDeliveryPartReport(store.callID, "report-call", "modem-1", 0x55, "acked", 200, 0, "", time.Now())
	if err != nil || match.MessageID != outcome.MessageID || match.State != "acked" {
		t.Fatalf("RP-ACK match = %+v, err=%v, outcome=%+v", match, err, outcome)
	}
}

func TestSMSServiceCenterURIUsesNetworkAssociatedPublicDomain(t *testing.T) {
	client := &Client{cfg: Config{
		HomeDomain:        "ims.mnc010.mcc234.3gppnetwork.org",
		SMSC:              "+447802002606",
		PreferredIdentity: "sip:+447724304579@o2.co.uk",
	}}
	if got, want := client.smsServiceCenterURI(), "sip:+447802002606@o2.co.uk;user=phone"; got != want {
		t.Fatalf("smsServiceCenterURI() = %q, want %q", got, want)
	}
}

func TestNewSMSRequestUsesPeerURIAndPCSCFRoute(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:    net.ParseIP("2001:db8::1"),
			LocalPort:  12000,
			PCSCFAddr:  "[2001:db8::2]:5060",
			PublicURI:  "sip:subscriber@ims.example",
			HomeDomain: "ims.example",
			Transport:  "tcp",
		},
		registerProfile: RegisterProfile{UserAgent: "SimAdmin VoWiFi", IncludeCellularNetwork: true},
	}

	req, err := client.newSMSRequest("+8615556250521")
	if err != nil {
		t.Fatalf("newSMSRequest: %v", err)
	}
	if got := req.Recipient.String(); got != "sip:+8615556250521@ims.example" {
		t.Fatalf("request URI = %q", got)
	}
	if got := req.Destination(); got != "[2001:db8::2]:5060" {
		t.Fatalf("destination = %q", got)
	}
	if got := req.GetHeader("To"); got == nil || got.Value() != "<sip:+8615556250521@ims.example>" {
		t.Fatalf("To = %v", got)
	}
	if got := req.GetHeader("Route"); got == nil || got.Value() != "<sip:[2001:db8::2]:5060;lr>" {
		t.Fatalf("Route = %v", got)
	}
	if got := req.GetHeader("P-Preferred-Service"); got != nil {
		t.Fatalf("unexpected P-Preferred-Service = %v", got)
	}
	if got := req.GetHeader("Accept-Contact"); got != nil {
		t.Fatalf("unexpected Accept-Contact = %v", got)
	}
	if got := req.GetHeader("Contact"); got == nil || !strings.Contains(got.Value(), "+g.3gpp.smsip") {
		t.Fatalf("Contact does not reuse the registered SMS binding: %v", got)
	}
	if got := req.GetHeader("P-Access-Network-Info"); got == nil || got.Value() != "IEEE-802.11" {
		t.Fatalf("P-Access-Network-Info = %v", got)
	}
	if cellular := req.GetHeader("Cellular-Network-Info"); cellular != nil {
		t.Fatalf("MESSAGE unexpectedly inherited REGISTER Cellular-Network-Info: %v", cellular)
	}
}

func TestSMSChallengeAuthorizationUsesFullPeerURI(t *testing.T) {
	client := &Client{cfg: Config{
		LocalIP:    net.ParseIP("192.0.2.10"),
		LocalPort:  12000,
		PCSCFAddr:  "192.0.2.20:5060",
		PrivateID:  "subscriber@ims.example",
		PublicURI:  "sip:subscriber@ims.example",
		HomeDomain: "ims.example",
		Transport:  "tcp",
	}}
	request, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatalf("newSMSRequest: %v", err)
	}
	response := sip.NewResponse(401, "Unauthorized")
	response.AppendHeader(sip.NewHeader(
		"WWW-Authenticate",
		`Digest realm="ims.example", nonce="YWJjZA==", algorithm=MD5`,
	))
	challenged, err := client.buildSMSChallengeRequest(request, response)
	if err != nil {
		t.Fatalf("buildSMSChallengeRequest: %v", err)
	}
	authorization := challenged.GetHeader("Authorization")
	if authorization == nil {
		t.Fatal("Authorization header missing")
	}
	if value := authorization.Value(); !strings.Contains(value, `uri="sip:+18005551212@ims.example"`) {
		t.Fatalf("Authorization URI = %q", value)
	}
}

func TestSMSChallengeAcceptsProxyAuthenticateOn401(t *testing.T) {
	client := &Client{cfg: Config{
		LocalIP:    net.ParseIP("192.0.2.10"),
		LocalPort:  12000,
		PCSCFAddr:  "192.0.2.20:5060",
		PrivateID:  "subscriber@ims.example",
		PublicURI:  "sip:subscriber@ims.example",
		HomeDomain: "ims.example",
		Transport:  "tcp",
	}}
	request, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatalf("newSMSRequest: %v", err)
	}
	response := sip.NewResponse(401, "Unauthorized")
	response.AppendHeader(sip.NewHeader(
		"Proxy-Authenticate",
		`Digest realm="ims.example", nonce="YWJjZA==", algorithm=MD5`,
	))
	challenged, err := client.buildSMSChallengeRequest(request, response)
	if err != nil {
		t.Fatalf("buildSMSChallengeRequest: %v", err)
	}
	if challenged.GetHeader("Authorization") != nil {
		t.Fatal("unexpected Authorization header")
	}
	proxyAuthorization := challenged.GetHeader("Proxy-Authorization")
	if proxyAuthorization == nil {
		t.Fatal("Proxy-Authorization header missing")
	}
	if value := proxyAuthorization.Value(); !strings.Contains(value, `uri="sip:+18005551212@ims.example"`) {
		t.Fatalf("Proxy-Authorization URI = %q", value)
	}
}

func TestSMSUsesPreemptiveRegisterDigestSession(t *testing.T) {
	client := &Client{cfg: Config{
		LocalIP:    net.ParseIP("192.0.2.10"),
		LocalPort:  12000,
		PCSCFAddr:  "192.0.2.20:5060",
		PrivateID:  "subscriber@ims.example",
		PublicURI:  "sip:subscriber@ims.example",
		HomeDomain: "ims.example",
		Transport:  "tcp",
		PreemptiveAuth: &DigestAuthSession{
			Challenge: digest.Challenge{
				Realm:     "ims.example",
				Nonce:     "register-nonce",
				Algorithm: "AKAv1-MD5",
			},
			AKAResponse: []byte("binary-res"),
		},
	}}
	request, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatalf("newSMSRequest: %v", err)
	}
	request.SetBody([]byte{0x01, 0x02, 0x03})
	if err := client.addPreemptiveSMSAuthorization(request); err != nil {
		t.Fatalf("addPreemptiveSMSAuthorization: %v", err)
	}
	authorization := request.GetHeader("Authorization")
	if authorization == nil {
		t.Fatal("Authorization header missing")
	}
	value := authorization.Value()
	if !strings.Contains(value, `uri="sip:+18005551212@ims.example"`) {
		t.Fatalf("Authorization URI = %q", value)
	}
	if !strings.Contains(value, "algorithm=AKAv1-MD5") {
		t.Fatalf("Authorization algorithm = %q", value)
	}
}

func TestNewSMSRequestUsesRegisteredServiceRoute(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:       net.ParseIP("192.0.2.10"),
			LocalPort:     12000,
			PCSCFAddr:     "192.0.2.20:5060",
			PublicURI:     "sip:subscriber@ims.example",
			HomeDomain:    "ims.example",
			Transport:     "tcp",
			ServiceRoutes: []string{"<sip:service-route.ims.example;lr>"},
		},
	}
	req, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatalf("newSMSRequest: %v", err)
	}
	routes := req.GetHeaders("Route")
	if len(routes) != 2 {
		t.Fatalf("Route count = %d, want 2", len(routes))
	}
	if got := routes[0].Value(); got != "<sip:192.0.2.20:5060;lr>" {
		t.Fatalf("P-CSCF Route = %q", got)
	}
	if got := routes[1].Value(); got != "<sip:service-route.ims.example;lr>" {
		t.Fatalf("Service-Route = %q", got)
	}
}

func TestNewMessageRequestUsesPCSCFDestination(t *testing.T) {
	client := &Client{cfg: Config{
		PCSCFAddr:  "[2001:db8::2]:5060",
		PublicURI:  "sip:subscriber@ims.example",
		HomeDomain: "ims.example",
		Transport:  "tcp",
	}}
	req, err := client.newRequest(sip.MESSAGE, client.cfg.PCSCFAddr, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Destination(); got != client.cfg.PCSCFAddr {
		t.Fatalf("destination=%q want %q", got, client.cfg.PCSCFAddr)
	}
	if got := req.GetHeader("P-Access-Network-Info"); got == nil || got.Value() != "IEEE-802.11" {
		t.Fatalf("P-Access-Network-Info = %v", got)
	}
}

func TestServiceRequestPANIIsIndependentFromRegisterProfile(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:    net.ParseIP("192.0.2.10"),
			LocalPort:  12000,
			PCSCFAddr:  "192.0.2.20:5060",
			PublicURI:  "sip:subscriber@ims.example",
			HomeDomain: "ims.example",
			Transport:  "tcp",
		},
		registerProfile: XiaomiMi11RegisterProfile(),
	}
	if client.registerProfile.IncludePAccessNetworkInfo {
		t.Fatal("test profile unexpectedly enables REGISTER PANI")
	}
	if !client.registerProfile.IncludeCellularNetwork {
		t.Fatal("test profile unexpectedly disables REGISTER Cellular-Network-Info")
	}

	message, err := client.newSMSRequest("+18005551212")
	if err != nil {
		t.Fatal(err)
	}
	if got := message.GetHeader("P-Access-Network-Info"); got == nil || got.Value() != "IEEE-802.11" {
		t.Fatalf("MESSAGE P-Access-Network-Info = %v", got)
	}

	register, err := client.newRequest(sip.REGISTER, client.cfg.PCSCFAddr, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := register.GetHeader("P-Access-Network-Info"); got != nil {
		t.Fatalf("REGISTER unexpectedly gained P-Access-Network-Info = %v", got)
	}
	if got := register.GetHeader("Cellular-Network-Info"); got == nil {
		t.Fatal("REGISTER lost profile-controlled Cellular-Network-Info")
	}
}

func TestNewSMSRequestUsesRegisteredCellularOnlyLocation(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:    net.ParseIP("2001:db8::1"),
			LocalPort:  12000,
			PCSCFAddr:  "[2001:db8::2]:5060",
			PublicURI:  "sip:subscriber@ims.mnc010.mcc234.3gppnetwork.org",
			HomeDomain: "ims.mnc010.mcc234.3gppnetwork.org",
			CellID:     "11ABD9B71FD",
			Transport:  "tcp",
		},
		registerProfile: RegisterProfile{LocationHeaderMode: "cellular_only"},
	}

	request, err := client.newSMSRequest("+8615556250521")
	if err != nil {
		t.Fatalf("newSMSRequest: %v", err)
	}
	if pani := request.GetHeader("P-Access-Network-Info"); pani != nil {
		t.Fatalf("cellular-only MESSAGE unexpectedly contains PANI: %v", pani)
	}
	if got := request.GetHeader("Cellular-Network-Info"); got == nil || got.Value() !=
		"3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=2341011ABD9B71FD;cell-info-age=0" {
		t.Fatalf("Cellular-Network-Info = %v", got)
	}
}

func TestNewSMSRequestAcceptsInboundPeerURI(t *testing.T) {
	client := &Client{cfg: Config{
		PCSCFAddr:  "192.0.2.20:5060",
		PublicURI:  "sip:subscriber@ims.example",
		HomeDomain: "ims.example",
		Transport:  "tcp",
	}}
	req, err := client.newSMSRequest("sip:smsc@ims.example")
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Recipient.String(); got != "sip:smsc@ims.example" {
		t.Fatalf("recipient=%q", got)
	}
	if got := req.GetHeader("To"); got == nil || got.Value() != "<sip:smsc@ims.example>" {
		t.Fatalf("To=%v", got)
	}
	if got := req.Destination(); got != client.cfg.PCSCFAddr {
		t.Fatalf("destination=%q", got)
	}
}
