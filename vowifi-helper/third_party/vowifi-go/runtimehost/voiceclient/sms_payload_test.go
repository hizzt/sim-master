package voiceclient

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"github.com/1239t/vowifi-go/runtimehost/messaging"
	"github.com/emiago/sipgo/sip"
)

type recordingSMSServerTransaction struct {
	response *sip.Response
	done     chan struct{}
}

func (tx *recordingSMSServerTransaction) Terminate()                         {}
func (tx *recordingSMSServerTransaction) OnTerminate(sip.FnTxTerminate) bool { return true }
func (tx *recordingSMSServerTransaction) Done() <-chan struct{} {
	if tx.done == nil {
		tx.done = make(chan struct{})
	}
	return tx.done
}
func (tx *recordingSMSServerTransaction) Err() error                   { return nil }
func (tx *recordingSMSServerTransaction) Acks() <-chan *sip.Request    { return make(chan *sip.Request) }
func (tx *recordingSMSServerTransaction) OnCancel(sip.FnTxCancel) bool { return true }
func (tx *recordingSMSServerTransaction) Respond(response *sip.Response) error {
	tx.response = response
	return nil
}

func TestDecodeSMSInboundPayloadAcceptsParameterizedRawSMS(t *testing.T) {
	body := []byte{0x01, 0x22, 0x00, 0x00}
	got, err := decodeSMSInboundPayload(`Application/Vnd.3Gpp.Sms; version="1"`, body)
	if err != nil {
		t.Fatalf("decodeSMSInboundPayload() error = %v", err)
	}
	if got.replyCPIM || !bytes.Equal(got.body, body) {
		t.Fatalf("payload=%+v", got)
	}
}

func TestDecodeSMSInboundPayloadAcceptsCPIMBase64SMS(t *testing.T) {
	raw := []byte{0x01, 0x33, 0x00, 0x00}
	encoded := base64.StdEncoding.EncodeToString(raw)
	cpim := []byte("From: <sip:smsc@ims.example>\r\nTo: <sip:user@ims.example>\r\n\r\nContent-Type: application/vnd.3gpp.sms\r\nContent-Transfer-Encoding: base64\r\nContent-Length: " + strconv.Itoa(len(encoded)) + "\r\n\r\n" + encoded)

	got, err := decodeSMSInboundPayload(cpimContentType, cpim)
	if err != nil {
		t.Fatalf("decodeSMSInboundPayload() error = %v", err)
	}
	if !got.replyCPIM || !bytes.Equal(got.body, raw) {
		t.Fatalf("payload=%+v body=%x", got, got.body)
	}
	if got.cpimFrom != "<sip:smsc@ims.example>" || got.cpimTo != "<sip:user@ims.example>" {
		t.Fatalf("CPIM identities were not retained: %+v", got)
	}
}

func TestInboundCPIMMessageDispatchesRawNetworkRPData(t *testing.T) {
	rpData := []byte{0x01, 0x2a, 0x00, 0x00, 0x01, 0x00}
	cpim := []byte("From: <sip:+8615556250521@ims.example>\r\n" +
		"To: <sip:subscriber@ims.example>\r\n" +
		"DateTime: 2026-08-08T10:00:00Z\r\n\r\n" +
		"Content-Type: application/vnd.3gpp.sms\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Length: " + strconv.Itoa(base64.StdEncoding.EncodedLen(len(rpData))) + "\r\n\r\n" +
		base64.StdEncoding.EncodeToString(rpData))

	var inbound messaging.InboundSMS
	client := &Client{cfg: Config{InboundSMSHandler: func(message messaging.InboundSMS) (messaging.InboundSMSReply, error) {
		inbound = message
		return messaging.InboundSMSReply{}, nil
	}}}
	request := newInboundSMSRequestForTest(cpimContentType, cpim)
	tx := &recordingSMSServerTransaction{}
	client.handleIncomingMessage(request, tx)

	if tx.response == nil || tx.response.StatusCode != 200 {
		t.Fatalf("SIP response=%+v", tx.response)
	}
	if inbound.ID != "inbound-call:2a" || inbound.CallID != "inbound-call" || !bytes.Equal(inbound.Body, rpData) {
		t.Fatalf("inbound=%+v body=%x", inbound, inbound.Body)
	}
}

func TestBuildSMSCPIMReplyReversesCPIMEnvelopeIdentities(t *testing.T) {
	client := &Client{cfg: Config{PublicURI: "sip:fallback@ims.example"}}
	request := newInboundSMSRequestForTest(cpimContentType, nil)
	payload := smsInboundPayload{
		replyCPIM: true,
		cpimFrom:  "<sip:+8615556250521@ims.example>",
		cpimTo:    "<sip:subscriber@ims.example>",
	}
	rpAck := []byte{0x02, 0x2a}
	wire, err := client.buildSMSCPIMReply(request, payload, rpAck)
	if err != nil {
		t.Fatal(err)
	}
	messageHeaders, contentHeaders, body, err := parseSMSCPIM(wire)
	if err != nil {
		t.Fatal(err)
	}
	if smsHeaderValue(messageHeaders, "From") != "<sip:subscriber@ims.example>" {
		t.Fatalf("From=%q", smsHeaderValue(messageHeaders, "From"))
	}
	if smsHeaderValue(messageHeaders, "To") != "<sip:+8615556250521@ims.example>" {
		t.Fatalf("To=%q", smsHeaderValue(messageHeaders, "To"))
	}
	if contentHeaders.Get("Content-Type") != smsContentType || !bytes.Equal(body, rpAck) {
		t.Fatalf("content headers=%v body=%x", contentHeaders, body)
	}
}

func TestInboundMalformedCPIMIsRejectedBeforeHandler(t *testing.T) {
	called := false
	client := &Client{cfg: Config{InboundSMSHandler: func(message messaging.InboundSMS) (messaging.InboundSMSReply, error) {
		called = true
		return messaging.InboundSMSReply{}, nil
	}}}
	request := newInboundSMSRequestForTest(cpimContentType, []byte("From: <sip:a>\r\n\r\nmissing-content-headers"))
	tx := &recordingSMSServerTransaction{}
	client.handleIncomingMessage(request, tx)
	if called || tx.response == nil || tx.response.StatusCode != 400 {
		t.Fatalf("called=%v response=%+v", called, tx.response)
	}
}

func newInboundSMSRequestForTest(contentType string, body []byte) *sip.Request {
	recipient := sip.Uri{Scheme: "sip", User: "subscriber", Host: "ims.example"}
	request := sip.NewRequest(sip.MESSAGE, recipient)
	request.AppendHeader(sip.NewHeader("Via", "SIP/2.0/TCP pcscf.ims.example;branch=z9hG4bK-inbound"))
	request.AppendHeader(sip.NewHeader("From", "<sip:network@ims.example>;tag=network"))
	request.AppendHeader(sip.NewHeader("To", "<sip:subscriber@ims.example>"))
	request.AppendHeader(sip.NewHeader("Call-ID", "inbound-call"))
	request.AppendHeader(sip.NewHeader("CSeq", "1 MESSAGE"))
	request.AppendHeader(sip.NewHeader("Content-Type", contentType))
	request.SetBody(body)
	if strings.TrimSpace(request.Destination()) == "" {
		request.SetDestination("pcscf.ims.example:5060")
	}
	return request
}

func TestDecodeSMSInboundPayloadSelectsMultipartSMSPart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary("sms-boundary"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/sdp"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("v=0\r\n"))
	part, err = writer.CreatePart(textproto.MIMEHeader{"Content-Type": {smsContentType}})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte{0x01, 0x44, 0x00, 0x00}
	_, _ = part.Write(raw)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := decodeSMSInboundPayload(`multipart/mixed; boundary="sms-boundary"`, body.Bytes())
	if err != nil {
		t.Fatalf("decodeSMSInboundPayload() error = %v", err)
	}
	if !bytes.Equal(got.body, raw) || got.replyCPIM {
		t.Fatalf("payload=%+v", got)
	}
}

func TestParseSMSCPIMHonorsDeclaredLength(t *testing.T) {
	_, headers, body, err := parseSMSCPIM([]byte("From: <sip:a>\r\n\r\nContent-Type: application/vnd.3gpp.sms\r\nContent-Length: 2\r\n\r\nabc"))
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("Content-Type") != smsContentType || !bytes.Equal(body, []byte("ab")) {
		t.Fatalf("headers=%v body=%q", headers, body)
	}
}
