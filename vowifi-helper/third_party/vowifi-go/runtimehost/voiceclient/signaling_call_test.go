package voiceclient

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func TestNewSignalingInviteRequestUsesIMSVoiceHeaders(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:        net.ParseIP("2001:db8::1"),
			LocalPort:      12000,
			PCSCFAddr:      "[2001:db8::2]:5060",
			PublicURI:      "sip:subscriber@ims.example",
			HomeDomain:     "ims.example",
			Transport:      "tcp",
			ServiceRoutes:  []string{"<sip:service-route.ims.example;lr>"},
			SecurityVerify: "ipsec-3gpp;prot=esp;mod=trans;alg=hmac-sha-1-96;spi-c=101;spi-s=202;port-c=5064;port-s=5063",
		},
		registerProfile: RegisterProfile{UserAgent: "SimAdmin VoWiFi", IncludeCellularNetwork: true},
		sipInstanceURN:  "urn:uuid:registered-instance",
	}

	request, err := client.newSignalingInviteRequest("+8615556250521", 40000, 40001)
	if err != nil {
		t.Fatalf("newSignalingInviteRequest: %v", err)
	}
	if got := request.Method.String(); got != "INVITE" {
		t.Fatalf("method=%q", got)
	}
	if got := request.Recipient.String(); got != "sip:+8615556250521@ims.example;user=phone" {
		t.Fatalf("request URI=%q", got)
	}
	if userParam, ok := request.Recipient.UriParams.Get("user"); !ok || userParam != "phone" {
		t.Fatalf("request URI user parameter=%q present=%v", userParam, ok)
	}
	if got := request.Destination(); got != "[2001:db8::2]:5060" {
		t.Fatalf("destination=%q", got)
	}
	if request.CallID() == nil || strings.TrimSpace(request.CallID().Value()) == "" {
		t.Fatal("Call-ID is missing")
	}
	if request.CSeq() == nil || request.CSeq().MethodName != "INVITE" || request.CSeq().SeqNo != 1 {
		t.Fatalf("CSeq=%v", request.CSeq())
	}
	assertHeaderValue(t, request.GetHeader("To"), "<sip:+8615556250521@ims.example;user=phone>")
	if from := request.GetHeader("From"); from == nil || !strings.HasPrefix(from.Value(), "<sip:subscriber@ims.example>;tag=") {
		t.Fatalf("From=%v", from)
	}
	assertHeaderValue(t, request.GetHeader("P-Preferred-Identity"), "<sip:subscriber@ims.example>")
	assertHeaderValue(t, request.GetHeader("Route"), "<sip:service-route.ims.example;lr>")
	assertHeaderValue(t, request.GetHeader("P-Preferred-Service"), "urn:urn-7:3gpp-service.ims.icsi.mmtel")
	assertHeaderValue(t, request.GetHeader("Accept-Contact"), "*;+g.3gpp.icsi-ref=\"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel\";require;explicit")
	assertHeaderValue(t, request.GetHeader("P-Access-Network-Info"), "IEEE-802.11")
	if cellular := request.GetHeader("Cellular-Network-Info"); cellular != nil {
		t.Fatalf("INVITE unexpectedly inherited REGISTER Cellular-Network-Info: %v", cellular)
	}
	assertHeaderValue(t, request.GetHeader("Security-Verify"), client.cfg.SecurityVerify)
	assertHeaderValue(t, request.GetHeader("Content-Type"), signalingCallContentType)
	contact := request.GetHeader("Contact")
	if contact == nil || !strings.Contains(contact.Value(), ";audio") ||
		!strings.Contains(contact.Value(), "+g.3gpp.icsi-ref") ||
		!strings.Contains(contact.Value(), "urn:uuid:registered-instance") ||
		strings.Contains(strings.ToLower(contact.Value()), ";expires=") {
		t.Fatalf("INVITE Contact does not preserve REGISTER MMTel binding: %v", contact)
	}
	assertHeaderValue(t, request.GetHeader("Supported"), "100rel")
	assertHeaderValue(t, request.GetHeader("Allow"), "INVITE,ACK,CANCEL,BYE,PRACK")
	if body := string(request.Body()); !strings.Contains(body, "m=audio 40000 RTP/AVP 97 96") ||
		!strings.Contains(body, "a=rtpmap:97 AMR-WB/16000/1") ||
		!strings.Contains(body, "a=rtpmap:96 AMR/8000/1") ||
		!strings.Contains(body, "a=sendrecv") || strings.Contains(body, "a=inactive") {
		t.Fatalf("SDP does not advertise the active IMS media endpoint:\n%s", body)
	}
	status := SignalingCallStatus{MediaReady: false, MediaSupported: true, MediaMode: "rtp_transport_waiting"}
	if status.MediaReady || !status.MediaSupported || status.MediaMode != "rtp_transport_waiting" {
		t.Fatalf("media boundary=%+v", status)
	}
}

func TestNewSignalingInviteRequestUsesRegisteredCellularOnlyLocation(t *testing.T) {
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
		registerProfile: RegisterProfile{
			UserAgent:          "SimAdmin VoWiFi",
			LocationHeaderMode: "cellular_only",
		},
	}

	request, err := client.newSignalingInviteRequest("+8615556250521", 40000, 40001)
	if err != nil {
		t.Fatalf("newSignalingInviteRequest: %v", err)
	}
	if pani := request.GetHeader("P-Access-Network-Info"); pani != nil {
		t.Fatalf("cellular-only INVITE unexpectedly contains PANI: %v", pani)
	}
	assertHeaderValue(t, request.GetHeader("Cellular-Network-Info"),
		"3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=2341011ABD9B71FD;cell-info-age=0")
}

func TestNewSignalingInviteRequestUsesAssociatedPreferredIdentity(t *testing.T) {
	client := &Client{cfg: Config{
		LocalIP:           net.ParseIP("2001:db8::1"),
		LocalPort:         12000,
		PCSCFAddr:         "[2001:db8::2]:5060",
		PublicURI:         "sip:001010000000001@ims.example",
		PreferredIdentity: "tel:+15551234567",
		HomeDomain:        "ims.example",
		Transport:         "tcp",
	}}

	request, err := client.newSignalingInviteRequest("+8615556250521", 40000, 40001)
	if err != nil {
		t.Fatalf("newSignalingInviteRequest: %v", err)
	}
	if from := request.GetHeader("From"); from == nil || !strings.HasPrefix(from.Value(), "<tel:+15551234567>;tag=") {
		t.Fatalf("From=%v", from)
	}
	assertHeaderValue(t, request.GetHeader("P-Preferred-Identity"), "<tel:+15551234567>")
	assertHeaderValue(t, request.GetHeader("To"), "<sip:+8615556250521@ims.example;user=phone>")
}

func TestApplyPreferredServiceIdentityRefreshesInviteAndPreservesFromTag(t *testing.T) {
	client := &Client{cfg: Config{
		LocalIP:    net.ParseIP("2001:db8::1"),
		LocalPort:  12000,
		PCSCFAddr:  "[2001:db8::2]:5060",
		PublicURI:  "sip:001010000000001@ims.example",
		HomeDomain: "ims.example",
		Transport:  "tcp",
	}}
	request, err := client.newSignalingInviteRequest("+8615556250521", 40000, 40001)
	if err != nil {
		t.Fatalf("newSignalingInviteRequest: %v", err)
	}
	fromBefore := request.From()
	if fromBefore == nil {
		t.Fatal("From is missing")
	}
	tagBefore, ok := fromBefore.Params.Get("tag")
	if !ok || tagBefore == "" {
		t.Fatalf("From tag=%q present=%v", tagBefore, ok)
	}

	client.cfg.PreferredIdentity = "tel:+15551234567"
	if err := client.applyPreferredServiceIdentity(request); err != nil {
		t.Fatalf("applyPreferredServiceIdentity: %v", err)
	}
	fromAfter := request.From()
	if fromAfter == nil || fromAfter.Address.String() != "tel:+15551234567" {
		t.Fatalf("From=%v", fromAfter)
	}
	if tagAfter, ok := fromAfter.Params.Get("tag"); !ok || tagAfter != tagBefore {
		t.Fatalf("From tag after refresh=%q present=%v, want %q", tagAfter, ok, tagBefore)
	}
	assertHeaderValue(t, request.GetHeader("P-Preferred-Identity"), "<tel:+15551234567>")
}

func TestRetrySignalingInviteRequestCreatesFreshTransactionBranch(t *testing.T) {
	client := &Client{cfg: Config{
		LocalIP:    net.ParseIP("2001:db8::1"),
		LocalPort:  12000,
		PCSCFAddr:  "[2001:db8::2]:5060",
		PublicURI:  "sip:caller@ims.example",
		PrivateID:  "caller@ims.example",
		HomeDomain: "ims.example",
		Transport:  "tcp",
	}}
	request, err := client.newSignalingInviteRequest("+18005551212")
	if err != nil {
		t.Fatalf("newSignalingInviteRequest: %v", err)
	}
	request.AppendHeader(sip.NewHeader("Via", "SIP/2.0/TCP [2001:db8::1]:12000;branch=z9hG4bK-old"))
	retry := retrySignalingInviteRequest(request)
	if retry == nil || retry.Via() != nil {
		t.Fatalf("retry Via=%v, want absent before sipgo rebuild", retry.Via())
	}
	if retry.CallID().Value() != request.CallID().Value() || retry.CSeq().SeqNo != request.CSeq().SeqNo {
		t.Fatalf("retry changed Call-ID/CSeq: original=%s/%d retry=%s/%d", request.CallID().Value(), request.CSeq().SeqNo, retry.CallID().Value(), retry.CSeq().SeqNo)
	}
}

func TestVoicePeerURI(t *testing.T) {
	client := &Client{cfg: Config{HomeDomain: "ims.example"}}
	tests := map[string]string{
		"+18005551212":              "sip:+18005551212@ims.example;user=phone",
		"alice@voice.example":       "sip:alice@voice.example",
		"sip:bob@voice.example":     "sip:bob@voice.example",
		"tel:+441234567890":         "tel:+441234567890",
		"sips:secure@voice.example": "sips:secure@voice.example",
	}
	for input, want := range tests {
		if got := client.voicePeerURI(input); got != want {
			t.Errorf("voicePeerURI(%q)=%q want %q", input, got, want)
		}
	}
}

func TestIMSVoiceSDPAcceptsShortCallID(t *testing.T) {
	body := string(imsVoiceSDP(net.ParseIP("192.0.2.10"), 40000, 40001, "abc"))
	if !strings.Contains(body, "c=IN IP4 192.0.2.10") || !strings.Contains(body, "m=audio 40000 RTP/AVP") ||
		!strings.Contains(body, "a=rtpmap:97 AMR-WB/16000/1") || !strings.Contains(body, "a=rtpmap:96 AMR/8000/1") ||
		!strings.Contains(body, "a=sendrecv") || strings.Contains(body, "a=inactive") {
		t.Fatalf("unexpected SDP:\n%s", body)
	}
}

func TestParseIMSVoiceAnswerSelectsAMRWBAndMediaAddress(t *testing.T) {
	body := []byte("v=0\r\no=- 1 1 IN IP6 2001:db8::20\r\nc=IN IP6 2001:db8::20\r\nt=0 0\r\n" +
		"m=audio 50000 RTP/AVP 97 96\r\na=rtcp:50001 IN IP6 2001:db8::20\r\n" +
		"a=rtpmap:97 AMR-WB/16000/1\r\na=rtpmap:96 AMR/8000/1\r\na=sendrecv\r\n")
	answer, err := parseIMSVoiceAnswer(body)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Codec != "AMR-WB" || answer.Payload != 97 || answer.RTPPort != 50000 || answer.RTCPPort != 50001 ||
		!answer.RemoteIP.Equal(net.ParseIP("2001:db8::20")) || answer.Direction != "sendrecv" {
		t.Fatalf("answer=%+v", answer)
	}
}

func TestParseIMSVoiceAnswerPrefersBuiltInG711WhenOffered(t *testing.T) {
	body := []byte("v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 50000 RTP/AVP 97 96 8 0\r\n" +
		"a=rtpmap:97 AMR-WB/16000/1\r\na=rtpmap:96 AMR/8000/1\r\n" +
		"a=rtpmap:8 PCMA/8000\r\na=rtpmap:0 PCMU/8000\r\na=sendrecv\r\n")
	answer, err := parseIMSVoiceAnswer(body)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Codec != "PCMA" || answer.Payload != 8 {
		t.Fatalf("answer=%+v", answer)
	}
}

func TestParseIMSVoiceAnswerRejectsNoCommonCodec(t *testing.T) {
	body := []byte("v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 50000 RTP/AVP 18\r\na=rtpmap:18 G729/8000\r\n")
	if _, err := parseIMSVoiceAnswer(body); err == nil {
		t.Fatal("expected incompatible SDP answer to fail")
	}
}

func TestParseIMSVoiceAnswerHonorsMediaScopeAndSessionDirection(t *testing.T) {
	body := []byte("v=0\r\nc=IN IP4 192.0.2.20\r\na=sendonly\r\n" +
		"m=audio 50000 RTP/AVP 96\r\na=rtpmap:96 AMR/8000/1\r\n" +
		"m=video 51000 RTP/AVP 99\r\nc=IN IP4 198.51.100.99\r\na=recvonly\r\n")
	answer, err := parseIMSVoiceAnswer(body)
	if err != nil {
		t.Fatal(err)
	}
	if !answer.RemoteIP.Equal(net.ParseIP("192.0.2.20")) || answer.Direction != "sendonly" {
		t.Fatalf("answer=%+v", answer)
	}
}

func TestParseIMSVoiceAnswerUsesIndependentRTCPAddressAndMediaDirection(t *testing.T) {
	body := []byte("v=0\r\nc=IN IP4 192.0.2.20\r\na=sendonly\r\n" +
		"m=audio 50000 RTP/AVP 97\r\nc=IN IP4 192.0.2.21\r\n" +
		"a=rtcp:51000 IN IP4 192.0.2.22\r\na=rtpmap:97 AMR-WB/16000/1\r\na=recvonly\r\n")
	answer, err := parseIMSVoiceAnswer(body)
	if err != nil {
		t.Fatal(err)
	}
	if !answer.RemoteIP.Equal(net.ParseIP("192.0.2.21")) || !answer.RTCPIP.Equal(net.ParseIP("192.0.2.22")) ||
		answer.RTCPPort != 51000 || answer.Direction != "recvonly" {
		t.Fatalf("answer=%+v", answer)
	}
}

func TestParseIMSVoiceAnswerRejectsUnsupportedMediaSemantics(t *testing.T) {
	tests := map[string]string{
		"inactive":        "a=inactive\r\n",
		"rtcp mux":        "a=sendrecv\r\na=rtcp-mux\r\n",
		"AMR clock":       "a=sendrecv\r\na=rtpmap:96 AMR/16000/1\r\n",
		"AMR channels":    "a=sendrecv\r\na=rtpmap:96 AMR/8000/2\r\n",
		"AMR static PT":   "a=sendrecv\r\na=rtpmap:8 AMR/8000/1\r\n",
		"AMR-WB clock":    "a=sendrecv\r\na=rtpmap:96 AMR-WB/8000/1\r\n",
		"AMR-WB channels": "a=sendrecv\r\na=rtpmap:96 AMR-WB/16000/2\r\n",
	}
	for name, attributes := range tests {
		t.Run(name, func(t *testing.T) {
			body := []byte("v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 50000 RTP/AVP 96\r\n" + attributes)
			if name == "AMR static PT" {
				body = []byte("v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 50000 RTP/AVP 8\r\n" + attributes)
			}
			if _, err := parseIMSVoiceAnswer(body); err == nil {
				t.Fatalf("expected %s SDP to fail", name)
			}
		})
	}
}

func TestParseIMSVoiceResponseOnlyAcceptsApplicationSDP(t *testing.T) {
	response := sip.NewResponse(183, "Session Progress")
	response.AppendHeader(sip.NewHeader("Content-Type", "text/plain"))
	response.SetBody([]byte("v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 50000 RTP/AVP 8\r\n"))
	if _, hasSDP, err := parseIMSVoiceResponse(response); err != nil || hasSDP {
		t.Fatalf("non-SDP response hasSDP=%v err=%v", hasSDP, err)
	}
	response.RemoveHeader("Content-Type")
	response.AppendHeader(sip.NewHeader("Content-Type", "application/sdp; charset=utf-8"))
	if answer, hasSDP, err := parseIMSVoiceResponse(response); err != nil || !hasSDP || answer.Codec != "PCMA" {
		t.Fatalf("SDP response answer=%+v hasSDP=%v err=%v", answer, hasSDP, err)
	}
}

func TestValidRTPPacketChecksNegotiatedPayloadAndHeaderBounds(t *testing.T) {
	plain := append([]byte{0x80, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}, 0x55)
	withCSRC := append([]byte{0x81, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 2}, 0x55)
	withExtension := append([]byte{0x90, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0xbe, 0xde, 0, 1, 1, 2, 3, 4}, 0x55)
	withPadding := append([]byte{0xa0, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0x55}, 0, 0, 0, 4)
	for name, packet := range map[string][]byte{"plain": plain, "CSRC": withCSRC, "extension": withExtension, "padding": withPadding} {
		if !validRTPPacket(packet, 96) {
			t.Errorf("valid %s packet rejected", name)
		}
	}
	invalid := map[string][]byte{
		"wrong payload":   append([]byte(nil), plain...),
		"no payload":      plain[:12],
		"short CSRC":      withCSRC[:15],
		"short extension": withExtension[:19],
		"bad padding":     append([]byte(nil), withPadding...),
	}
	invalid["wrong payload"][1] = 97
	invalid["bad padding"][len(invalid["bad padding"])-1] = 20
	for name, packet := range invalid {
		if validRTPPacket(packet, 96) {
			t.Errorf("invalid %s packet accepted", name)
		}
	}
}

func TestValidRTCPPacketChecksCompoundFraming(t *testing.T) {
	rr := []byte{0x80, 201, 0, 1, 0, 0, 0, 1}
	sdes := []byte{0x80, 202, 0, 1, 0, 0, 0, 1}
	if !validRTCPPacket(rr) || !validRTCPPacket(append(append([]byte(nil), rr...), sdes...)) {
		t.Fatal("valid RTCP framing was rejected")
	}
	for name, packet := range map[string][]byte{
		"wrong version":   {0x40, 201, 0, 1, 0, 0, 0, 1},
		"wrong type":      {0x80, 100, 0, 1, 0, 0, 0, 1},
		"unassigned type": {0x80, 222, 0, 1, 0, 0, 0, 1},
		"short length":    {0x80, 201, 0, 2, 0, 0, 0, 1},
		"trailing byte":   append(append([]byte(nil), rr...), 0),
	} {
		if validRTCPPacket(packet) {
			t.Errorf("invalid %s RTCP packet accepted", name)
		}
	}
}

func TestApplySignalingMediaAnswerPublishesEarlyMediaAndResetsChangedEndpoint(t *testing.T) {
	client := &Client{signalingCalls: newSignalingCallStore()}
	media := &signalingMediaEndpoint{}
	now := time.Now().UTC()
	if err := client.signalingCalls.add(&signalingCall{
		status: SignalingCallStatus{CallID: "early-media", State: "proceeding", StartedAt: now, UpdatedAt: now, MediaSupported: true},
		media:  media,
	}); err != nil {
		t.Fatal(err)
	}
	first := imsVoiceAnswer{RemoteIP: net.ParseIP("192.0.2.20"), RTCPIP: net.ParseIP("192.0.2.20"), RTPPort: 50000, RTCPPort: 50001, Codec: "AMR-WB", Payload: 97, Direction: "sendrecv"}
	client.applySignalingMediaAnswer("early-media", media, first)
	status, _ := client.signalingCalls.status("early-media")
	if status.MediaCodec != "AMR-WB" || status.MediaDirection != "sendrecv" || status.MediaMode != "rtp_transport_waiting" ||
		status.AudioReady || status.AudioMode != "codec_not_supported" {
		t.Fatalf("provisional media status=%+v", status)
	}
	client.signalingCalls.update("early-media", func(status *SignalingCallStatus) {
		status.MediaReady = true
		status.MediaMode = "rtp_transport_receiving"
	})
	final := first
	final.RemoteIP = net.ParseIP("192.0.2.21")
	client.applySignalingMediaAnswer("early-media", media, final)
	status, _ = client.signalingCalls.status("early-media")
	if status.MediaReady || status.MediaMode != "rtp_transport_waiting" {
		t.Fatalf("changed final endpoint retained provisional readiness: %+v", status)
	}
}

func TestSignalingMediaReaderRejectsUnnegotiatedPayloadAndRecvOnlyRTP(t *testing.T) {
	client := &Client{cfg: Config{LocalIP: net.ParseIP("127.0.0.1")}, signalingCalls: newSignalingCallStore()}
	media, err := client.openSignalingMedia(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	remote, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	remotePort := remote.LocalAddr().(*net.UDPAddr).Port
	answer := imsVoiceAnswer{RemoteIP: net.ParseIP("127.0.0.1"), RTCPIP: net.ParseIP("127.0.0.1"), RTPPort: remotePort, RTCPPort: remotePort + 1, Codec: "AMR", Payload: 96, Direction: "recvonly"}
	media.setAnswer(answer)
	now := time.Now().UTC()
	if err := client.signalingCalls.add(&signalingCall{status: SignalingCallStatus{CallID: "reject-media", State: "proceeding", StartedAt: now, UpdatedAt: now}, media: media}); err != nil {
		t.Fatal(err)
	}
	client.startSignalingMediaReaders("reject-media", media)
	packet := append([]byte{0x80, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}, 0x55)
	if _, err := remote.WriteToUDP(packet, media.rtp.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	status, _ := client.signalingCalls.status("reject-media")
	if status.MediaReady || status.RTPPacketsReceived != 0 {
		t.Fatalf("recvonly RTP was accepted: %+v", status)
	}
	answer.Direction = "sendrecv"
	media.setAnswer(answer)
	packet[1] = 97
	if _, err := remote.WriteToUDP(packet, media.rtp.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	status, _ = client.signalingCalls.status("reject-media")
	if status.MediaReady || status.RTPPacketsReceived != 0 {
		t.Fatalf("unnegotiated RTP payload was accepted: %+v", status)
	}
}

func TestSignalingMediaReaderMarksReadyAndTerminalCloseReleasesSockets(t *testing.T) {
	client := &Client{
		cfg:            Config{LocalIP: net.ParseIP("127.0.0.1")},
		signalingCalls: newSignalingCallStore(),
	}
	media, err := client.openSignalingMedia(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		media.Close()
		t.Fatal(err)
	}
	defer remote.Close()
	remotePort := remote.LocalAddr().(*net.UDPAddr).Port
	media.setAnswer(imsVoiceAnswer{RemoteIP: net.ParseIP("127.0.0.1"), RTCPIP: net.ParseIP("127.0.0.1"), RTPPort: remotePort, RTCPPort: remotePort + 1, Codec: "AMR", Payload: 96, Direction: "sendrecv"})
	now := time.Now().UTC()
	if err := client.signalingCalls.add(&signalingCall{
		status: SignalingCallStatus{CallID: "media-test", State: "dialing", StartedAt: now, UpdatedAt: now, MediaSupported: true},
		media:  media,
	}); err != nil {
		media.Close()
		t.Fatal(err)
	}
	client.startSignalingMediaReaders("media-test", media)
	rtp := make([]byte, 13)
	rtp[0] = 0x80
	rtp[1] = 96
	rtp[12] = 0x55
	if _, err := remote.WriteToUDP(rtp, media.rtp.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, _ := client.signalingCalls.status("media-test")
		if status.MediaReady && status.RTPPacketsReceived == 1 && status.RTPBytesReceived == 13 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	status, _ := client.signalingCalls.status("media-test")
	if !status.MediaReady || status.MediaMode != "rtp_transport_receiving" || status.RTPPacketsReceived != 1 || status.RTPBytesReceived != 13 {
		t.Fatalf("media status=%+v", status)
	}
	client.finishSignalingCall("media-test", 487, "failed", "test complete")
	status, _ = client.signalingCalls.status("media-test")
	if status.MediaReady || status.MediaMode != "rtp_transport_closed" {
		t.Fatalf("terminal media status=%+v", status)
	}
	if _, err := media.rtp.WriteTo(rtp, remote.LocalAddr()); err == nil {
		t.Fatal("RTP socket remained writable after terminal release")
	}
}

func TestSignalingCallStoreAllowsOnlyOneActiveCall(t *testing.T) {
	store := newSignalingCallStore()
	now := time.Now().UTC()
	first := &signalingCall{status: SignalingCallStatus{CallID: "first", State: "dialing", StartedAt: now, UpdatedAt: now}}
	if err := store.add(first); err != nil {
		t.Fatalf("add first: %v", err)
	}
	second := &signalingCall{status: SignalingCallStatus{CallID: "second", State: "dialing", StartedAt: now, UpdatedAt: now}}
	if err := store.add(second); err == nil {
		t.Fatal("expected concurrent active call to be rejected")
	}
	store.update("first", func(status *SignalingCallStatus) { status.State = "terminated" })
	if err := store.add(second); err != nil {
		t.Fatalf("add after terminal call: %v", err)
	}
}

func TestSignalingCallStoreTerminalHangupIsIdempotent(t *testing.T) {
	store := newSignalingCallStore()
	now := time.Now().UTC()
	call := &signalingCall{status: SignalingCallStatus{
		CallID: "terminal", State: "terminated", Reason: "already ended", StartedAt: now, UpdatedAt: now,
	}}
	if err := store.add(call); err != nil {
		t.Fatalf("add terminal call: %v", err)
	}
	plan, err := store.requestHangup("terminal")
	if err != nil {
		t.Fatalf("request terminal hangup: %v", err)
	}
	if !plan.terminal || plan.status.State != "terminated" || plan.status.Reason != "already ended" {
		t.Fatalf("terminal hangup changed status: %+v", plan)
	}
}

// TestSignalingCallCancel200RaceSendsAckAndBye runs an actual UDP fake UAS.
// It deliberately answers the INVITE with 200 after receiving CANCEL. RFC
// 3261 requires the UAC to ACK that winning 2xx and immediately send BYE.
func TestSignalingCallCancel200RaceSendsAckAndBye(t *testing.T) {
	uasConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer uasConn.Close()
	if err := uasConn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	uasAddr := uasConn.LocalAddr().String()
	events := make(chan string, 4)
	serverDone := make(chan error, 1)
	go func() {
		invite, peer, err := readFakeUASRequest(uasConn, sip.INVITE)
		if err != nil {
			serverDone <- err
			return
		}
		if err := writeFakeUASResponse(uasConn, peer, invite, sip.StatusRinging, "Ringing", uasAddr, true); err != nil {
			serverDone <- err
			return
		}
		events <- "ringing"

		cancelRequest, cancelPeer, err := readFakeUASRequest(uasConn, sip.CANCEL)
		if err != nil {
			serverDone <- err
			return
		}
		if err := writeFakeUASResponse(uasConn, cancelPeer, cancelRequest, sip.StatusOK, "OK", uasAddr, false); err != nil {
			serverDone <- err
			return
		}
		// The INVITE final response wins the CANCEL race.
		if err := writeFakeUASResponse(uasConn, peer, invite, sip.StatusOK, "OK", uasAddr, true); err != nil {
			serverDone <- err
			return
		}

		if _, _, err := readFakeUASRequest(uasConn, sip.ACK); err != nil {
			serverDone <- err
			return
		}
		events <- "ack"
		byeRequest, byePeer, err := readFakeUASRequest(uasConn, sip.BYE)
		if err != nil {
			serverDone <- err
			return
		}
		events <- "bye"
		if err := writeFakeUASResponse(uasConn, byePeer, byeRequest, sip.StatusOK, "OK", uasAddr, false); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer ua.Close()
	sipClient, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatal(err)
	}
	defer sipClient.Close()
	client := &Client{
		cfg: Config{
			LocalIP:    net.ParseIP("127.0.0.1"),
			LocalPort:  15060,
			PCSCFAddr:  uasAddr,
			PublicURI:  "sip:caller@ims.example",
			PrivateID:  "caller@ims.example",
			HomeDomain: "ims.example",
			Transport:  "udp",
		},
		client:         sipClient,
		signalingCalls: newSignalingCallStore(),
	}
	started, err := client.StartSignalingCall(context.Background(), "+18005551212")
	if err != nil {
		t.Fatalf("start signaling call: %v", err)
	}
	select {
	case event := <-events:
		if event != "ringing" {
			t.Fatalf("first UAS event=%q", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fake UAS did not receive INVITE")
	}
	waitForSignalingState(t, client, started.CallID, "ringing")
	if _, err := client.HangupSignalingCall(context.Background(), started.CallID); err != nil {
		t.Fatalf("hangup signaling call: %v", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake UAS did not observe ACK and BYE")
	}
	status := waitForSignalingState(t, client, started.CallID, "terminated")
	if !strings.Contains(status.Reason, "ACK and BYE completed") {
		t.Fatalf("terminal status=%+v", status)
	}
}

// TestSignalingCallReliableProvisionalSendsPRACKAndKeepsInviteAckCSeq covers
// the VoHive outbound-agent flow: advertise 100rel, answer a reliable 183
// with PRACK, then keep ACK on the original INVITE CSeq and advance BYE past
// the PRACK CSeq.
func TestSignalingCallReliableProvisionalSendsPRACKAndKeepsInviteAckCSeq(t *testing.T) {
	uasConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer uasConn.Close()
	if err := uasConn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	uasAddr := uasConn.LocalAddr().String()
	serverDone := make(chan error, 1)
	go func() {
		invite, peer, err := readFakeUASRequest(uasConn, sip.INVITE)
		if err != nil {
			serverDone <- err
			return
		}
		if !signalingHeaderHasToken(invite.GetHeaders("Supported"), "100rel") ||
			!signalingHeaderHasToken(invite.GetHeaders("Allow"), "PRACK") {
			serverDone <- fmt.Errorf("INVITE does not advertise 100rel/PRACK: Supported=%v Allow=%v", invite.GetHeaders("Supported"), invite.GetHeaders("Allow"))
			return
		}

		progress := sip.NewResponseFromRequest(invite, 183, "Session Progress", nil)
		progress.To().Params.Add("tag", "fake-uas")
		host, portText, err := net.SplitHostPort(uasAddr)
		if err != nil {
			serverDone <- err
			return
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			serverDone <- err
			return
		}
		progress.AppendHeader(&sip.ContactHeader{Address: sip.Uri{User: "callee", Host: host, Port: port}})
		progress.AppendHeader(sip.NewHeader("Require", "100rel"))
		progress.AppendHeader(sip.NewHeader("RSeq", "41"))
		progress.SetBody(nil)
		if _, err := uasConn.WriteTo([]byte(progress.String()), peer); err != nil {
			serverDone <- err
			return
		}

		prack, prackPeer, err := readFakeUASRequest(uasConn, sip.PRACK)
		if err != nil {
			serverDone <- err
			return
		}
		if rack := prack.GetHeader("RAck"); rack == nil || rack.Value() != "41 1 INVITE" {
			serverDone <- fmt.Errorf("PRACK RAck=%v", rack)
			return
		}
		if cseq := prack.CSeq(); cseq == nil || cseq.SeqNo != 2 || cseq.MethodName != sip.PRACK {
			serverDone <- fmt.Errorf("PRACK CSeq=%v", cseq)
			return
		}
		if err := writeFakeUASResponse(uasConn, prackPeer, prack, sip.StatusOK, "OK", uasAddr, false); err != nil {
			serverDone <- err
			return
		}
		if err := writeFakeUASResponse(uasConn, peer, invite, sip.StatusOK, "OK", uasAddr, true); err != nil {
			serverDone <- err
			return
		}

		ack, _, err := readFakeUASRequest(uasConn, sip.ACK)
		if err != nil {
			serverDone <- err
			return
		}
		if cseq := ack.CSeq(); cseq == nil || cseq.SeqNo != 1 || cseq.MethodName != sip.ACK {
			serverDone <- fmt.Errorf("ACK CSeq=%v, want 1 ACK", cseq)
			return
		}
		bye, byePeer, err := readFakeUASRequest(uasConn, sip.BYE)
		if err != nil {
			serverDone <- err
			return
		}
		if cseq := bye.CSeq(); cseq == nil || cseq.SeqNo != 3 || cseq.MethodName != sip.BYE {
			serverDone <- fmt.Errorf("BYE CSeq=%v, want 3 BYE", cseq)
			return
		}
		if err := writeFakeUASResponse(uasConn, byePeer, bye, sip.StatusOK, "OK", uasAddr, false); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer ua.Close()
	sipClient, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatal(err)
	}
	defer sipClient.Close()
	client := &Client{
		cfg: Config{
			LocalIP:    net.ParseIP("127.0.0.1"),
			LocalPort:  15061,
			PCSCFAddr:  uasAddr,
			PublicURI:  "sip:caller@ims.example",
			PrivateID:  "caller@ims.example",
			HomeDomain: "ims.example",
			Transport:  "udp",
		},
		client:         sipClient,
		signalingCalls: newSignalingCallStore(),
	}
	started, err := client.StartSignalingCall(context.Background(), "+18005551212")
	if err != nil {
		t.Fatalf("start signaling call: %v", err)
	}
	waitForSignalingState(t, client, started.CallID, "signaling_established")
	if _, err := client.HangupSignalingCall(context.Background(), started.CallID); err != nil {
		t.Fatalf("hangup signaling call: %v", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake UAS did not complete reliable-provisional dialog")
	}
}

func readFakeUASRequest(conn net.PacketConn, method sip.RequestMethod) (*sip.Request, net.Addr, error) {
	buffer := make([]byte, 64*1024)
	for {
		n, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			return nil, nil, err
		}
		message, err := sip.ParseMessage(buffer[:n])
		if err != nil {
			return nil, nil, err
		}
		request, ok := message.(*sip.Request)
		if !ok {
			continue
		}
		if request.Method == method {
			return request, peer, nil
		}
	}
}

func writeFakeUASResponse(conn net.PacketConn, peer net.Addr, request *sip.Request, code int, reason, contactAddr string, addDialogTag bool) error {
	response := sip.NewResponseFromRequest(request, code, reason, nil)
	if addDialogTag {
		response.To().Params.Add("tag", "fake-uas")
	}
	if code >= 180 && request.Method == sip.INVITE {
		host, portText, err := net.SplitHostPort(contactAddr)
		if err != nil {
			return err
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return err
		}
		response.AppendHeader(&sip.ContactHeader{Address: sip.Uri{User: "callee", Host: host, Port: port}})
	}
	if code == sip.StatusOK && request.Method == sip.INVITE {
		host, _, err := net.SplitHostPort(contactAddr)
		if err != nil {
			return err
		}
		response.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		response.SetBody([]byte("v=0\r\no=- 1 1 IN IP4 " + host + "\r\nc=IN IP4 " + host + "\r\nt=0 0\r\n" +
			"m=audio 40000 RTP/AVP 96\r\na=rtcp:40001 IN IP4 " + host + "\r\n" +
			"a=rtpmap:96 AMR/8000/1\r\na=fmtp:96 octet-align=1\r\na=sendrecv\r\n"))
	} else {
		response.SetBody(nil)
	}
	_, err := conn.WriteTo([]byte(response.String()), peer)
	return err
}

func waitForSignalingState(t *testing.T, client *Client, callID, want string) SignalingCallStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, ok := client.signalingCalls.status(callID)
		if ok && status.State == want {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := client.signalingCalls.status(callID)
	t.Fatalf("call %s state=%q want %q", callID, status.State, want)
	return SignalingCallStatus{}
}

type headerValue interface {
	Value() string
}

func assertHeaderValue(t *testing.T, header headerValue, want string) {
	t.Helper()
	if header == nil || header.Value() != want {
		t.Fatalf("header=%v want %q", header, want)
	}
}
