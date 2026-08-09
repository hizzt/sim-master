package voiceclient

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/emiago/sipgo/sip"
)

func TestSignalingInviteDiagnosticsAreUsefulAndSecretFree(t *testing.T) {
	client := &Client{
		cfg: Config{
			LocalIP:        net.ParseIP("2001:db8::10"),
			LocalPort:      5060,
			PCSCFAddr:      "[2001:db8::20]:5060",
			PublicURI:      "sip:secret-user@ims.example",
			PrivateID:      "secret-private@ims.example",
			HomeDomain:     "ims.example",
			Transport:      "tcp",
			ServiceRoutes:  []string{"<sip:route.ims.example:5070;lr>"},
			SecurityVerify: "ipsec-3gpp;spi-c=secret-spi;spi-s=another-secret",
		},
	}
	request, err := client.newSignalingInviteRequest("+8615556250521", 40000, 40001)
	if err != nil {
		t.Fatal(err)
	}
	request.AppendHeader(sip.NewHeader(
		"Authorization",
		`Digest username="secret-private@ims.example", realm="ims.example", nonce="secret-nonce", uri="sip:+8615556250521@ims.example;user=phone", algorithm=AKAv1-MD5, response="secret-response"`,
	))
	request.AppendHeader(sip.NewHeader("Supported", "100rel"))
	request.AppendHeader(sip.NewHeader("Allow", "INVITE,PRACK"))

	diagnostics := inspectSignalingInvite(request)
	if diagnostics.Method != "INVITE" || diagnostics.RequestHost != "ims.example" || !diagnostics.RequestUserPhone {
		t.Fatalf("request diagnostics=%+v", diagnostics)
	}
	if !diagnostics.AcceptContactMMTel || !diagnostics.AcceptContactRequire || !diagnostics.AcceptContactExplicit || !diagnostics.PPreferredService {
		t.Fatalf("MMTel service diagnostics=%+v", diagnostics)
	}
	if diagnostics.RouteCount != 1 || diagnostics.RouteHost != "route.ims.example" || diagnostics.RoutePort != 5070 {
		t.Fatalf("route diagnostics=%+v", diagnostics)
	}
	if !diagnostics.ContactPresent || diagnostics.ContactPort != 5060 || diagnostics.ContactTransport != "tcp" {
		t.Fatalf("contact diagnostics=%+v", diagnostics)
	}
	if diagnostics.SDPAudioPort != 40000 || diagnostics.SDPAudioProtocol != "RTP/AVP" || diagnostics.SDPDirection != "sendrecv" ||
		!diagnostics.SDPAMR || !diagnostics.SDPAMRWB {
		t.Fatalf("SDP diagnostics=%+v", diagnostics)
	}
	if !diagnostics.AuthParsed || diagnostics.DigestMethod != "INVITE" || !diagnostics.DigestURIMatchesRequest {
		t.Fatalf("digest diagnostics=%+v", diagnostics)
	}
	if !diagnostics.SecurityVerifyPresent || diagnostics.SecurityVerifyMode != "ipsec-3gpp" || diagnostics.SecurityVerifySHA256 == "" {
		t.Fatalf("Security-Verify diagnostics=%+v", diagnostics)
	}
	if !diagnostics.Supported100rel || !diagnostics.AllowPRACK {
		t.Fatalf("100rel diagnostics=%+v", diagnostics)
	}

	rendered := fmt.Sprintf("%+v", diagnostics)
	for _, secret := range []string{
		"+8615556250521", "secret-user", "secret-private", "secret-nonce",
		"secret-response", "secret-spi", "another-secret",
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, rendered)
		}
	}
}

func TestSignalingResponseDiagnosticsIdentifyPRACKAnd487Source(t *testing.T) {
	request := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "callee", Host: "ims.example"})
	request.AppendHeader(sip.NewHeader("From", "<sip:caller@ims.example>;tag=from-tag"))
	request.AppendHeader(sip.NewHeader("To", "<sip:callee@ims.example>"))
	request.AppendHeader(sip.NewHeader("Call-ID", "safe-call-id"))
	request.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))

	progress := sip.NewResponseFromRequest(request, 183, "Session Progress", nil)
	progress.To().Params.Add("tag", "to-tag")
	progress.AppendHeader(sip.NewHeader("Require", "100rel"))
	progress.AppendHeader(sip.NewHeader("Supported", "timer,100rel"))
	progress.AppendHeader(sip.NewHeader("RSeq", "42"))
	progress.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	progress.SetBody([]byte("v=0\r\nc=IN IP6 2001:db8::20\r\nm=audio 50000 RTP/AVP 97\r\na=rtpmap:97 AMR-WB/16000/1\r\na=sendrecv\r\n"))
	diagnostics := inspectSignalingResponse("safe-call-id", progress, false)
	if !diagnostics.PRACKRequired || diagnostics.RSeq != 42 || diagnostics.ResponseSource != signalingResponseSourceRemote ||
		!diagnostics.SDPAnswerParsed || diagnostics.SDPAnswerCodec != "AMR-WB" || diagnostics.SDPAnswerRTPPort != 50000 ||
		diagnostics.SDPAnswerIPFamily != "IP6" || diagnostics.SDPAnswerDirection != "sendrecv" {
		t.Fatalf("progress diagnostics=%+v", diagnostics)
	}

	terminated := sip.NewResponseFromRequest(request, sip.StatusRequestTerminated, "Request Terminated", nil)
	terminated.AppendHeader(sip.NewHeader("Reason", `SIP;cause=488;text="Not acceptable here",Q.850;cause=88;text="Incompatible destination"`))
	terminated.AppendHeader(sip.NewHeader("Warning", `399 pcscf.example "media policy"`))
	remote := inspectSignalingResponse("safe-call-id", terminated, false)
	if remote.ResponseSource != signalingResponseSourceRemote487 || remote.ReasonHeaderCount != 2 ||
		remote.ReasonProtocols != "SIP,Q.850" || remote.ReasonCauses != "488,88" ||
		!remote.ReasonTextPresent || remote.ReasonTextSHA256 == "" || !remote.WarningPresent || remote.WarningCode != 399 {
		t.Fatalf("remote 487 diagnostics=%+v", remote)
	}
	local := inspectSignalingResponse("safe-call-id", terminated, true)
	if local.ResponseSource != signalingResponseSourceLocalCancel {
		t.Fatalf("local CANCEL diagnostics=%+v", local)
	}
}
