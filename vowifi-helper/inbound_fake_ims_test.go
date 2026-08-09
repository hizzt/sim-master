package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/techblack/sim-master/vowifi-helper/smscodec"
	"github.com/1239t/vowifi-go/engine/sim"
	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
	"github.com/emiago/sipgo/sip"
)

type unusedFakeIMSAKA struct{}

func (unusedFakeIMSAKA) CalculateAKA(_, _ []byte) (sim.AKAResult, error) {
	return sim.AKAResult{}, nil
}

// TestFakeIMSInboundSMSReachesAcknowledgedHelperHistory exercises the actual
// UDP SIP listener and UAC against a loopback fake IMS/UAS. It proves the
// complete helper boundary: MESSAGE -> RP-DATA/SMS-DELIVER -> RP-ACK MESSAGE ->
// 2xx result callback -> bounded history -> wait_receive.
func TestFakeIMSInboundSMSReachesAcknowledgedHelperHistory(t *testing.T) {
	uasConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer uasConn.Close()
	if err := uasConn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}

	clientPort := reserveLoopbackUDPPort(t)
	clientAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(clientPort))
	statusPath := t.TempDir() + "/status.json"
	report := newReporter(statusPath, tunnelStatus{})
	decoder := newInboundDecoder(report)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, err := voiceclient.Dial(ctx, voiceclient.Config{
		DeviceID:                "fake-modem",
		LocalIP:                 net.ParseIP("127.0.0.1"),
		LocalPort:               clientPort,
		PCSCFAddr:               uasConn.LocalAddr().String(),
		PrivateID:               "subscriber@ims.example",
		PublicURI:               "sip:subscriber@ims.example",
		HomeDomain:              "ims.example",
		Transport:               "udp",
		AKA:                     unusedFakeIMSAKA{},
		SkipRegister:            true,
		InboundSMSHandler:       decoder.handle,
		InboundSMSResultHandler: decoder.handleResult,
	})
	if err != nil {
		t.Fatalf("start loopback voiceclient: %v", err)
	}
	defer client.Close(context.Background())

	waitResult := make(chan *smsReceiveResult, 1)
	go func() {
		waitResult <- (&controlServer{report: report}).waitReceive(ctx, "", 6*time.Second)
	}()

	tpdu, err := hex.DecodeString("0005810180F600006270502143650005E8329BFD06")
	if err != nil {
		t.Fatal(err)
	}
	rpData := inboundRPDataForTest(0x34, "+447802002606", tpdu)
	incoming := newFakeIMSInboundRequest(uasConn.LocalAddr().String(), rpData)
	clientUDPAddr, err := net.ResolveUDPAddr("udp4", clientAddr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uasConn.WriteTo([]byte(incoming.String()), clientUDPAddr); err != nil {
		t.Fatal(err)
	}

	immediateOK := false
	rpAckSeen := false
	buffer := make([]byte, 64*1024)
	for !immediateOK || !rpAckSeen {
		n, peer, err := uasConn.ReadFrom(buffer)
		if err != nil {
			t.Fatalf("fake IMS read: %v", err)
		}
		message, err := sip.ParseMessage(buffer[:n])
		if err != nil {
			t.Fatalf("parse fake IMS packet: %v", err)
		}
		switch message := message.(type) {
		case *sip.Response:
			if message.StatusCode == sip.StatusOK && message.CSeq() != nil && message.CSeq().MethodName == sip.MESSAGE {
				immediateOK = true
			}
		case *sip.Request:
			if message.Method != sip.MESSAGE {
				continue
			}
			if inReplyTo := message.GetHeader("In-Reply-To"); inReplyTo == nil || inReplyTo.Value() != "fake-inbound-call" {
				t.Fatalf("RP-ACK In-Reply-To=%v", inReplyTo)
			}
			if contentType := message.GetHeader("Content-Type"); contentType == nil || contentType.Value() != "application/vnd.3gpp.sms" {
				t.Fatalf("RP-ACK Content-Type=%v", contentType)
			}
			if !bytes.Equal(message.Body(), smscodec.BuildRPAck(0x34)) {
				t.Fatalf("RP-ACK body=%x", message.Body())
			}
			response := sip.NewResponseFromRequest(message, sip.StatusOK, "OK", nil)
			response.SetBody(nil)
			if _, err := uasConn.WriteTo([]byte(response.String()), peer); err != nil {
				t.Fatal(err)
			}
			rpAckSeen = true
		}
	}

	select {
	case result := <-waitResult:
		if !result.Verified || result.State != "passed" || result.MessageID != "fake-inbound-call:34" ||
			result.From != "10086" || result.Content != "hello" || result.RPAckCode != 200 {
			t.Fatalf("wait_receive result=%+v", result)
		}
	case <-ctx.Done():
		t.Fatal("helper history was not completed after fake IMS accepted RP-ACK")
	}

	status := report.snapshot()
	if len(status.SMSReceivedMessages) != 1 || status.SMSReceivedMessages[0].ID != "fake-inbound-call:34" {
		t.Fatalf("received history=%+v", status.SMSReceivedMessages)
	}
}

func reserveLoopbackUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func newFakeIMSInboundRequest(uasAddr string, body []byte) *sip.Request {
	recipient := sip.Uri{Scheme: "sip", User: "subscriber", Host: "ims.example"}
	request := sip.NewRequest(sip.MESSAGE, recipient)
	request.AppendHeader(sip.NewHeader("Via", fmt.Sprintf("SIP/2.0/UDP %s;branch=z9hG4bK-fake-inbound", uasAddr)))
	request.AppendHeader(sip.NewHeader("From", "<sip:smsc@ims.example>;tag=fake-network"))
	request.AppendHeader(sip.NewHeader("To", "<sip:subscriber@ims.example>"))
	request.AppendHeader(sip.NewHeader("Call-ID", "fake-inbound-call"))
	request.AppendHeader(sip.NewHeader("CSeq", "1 MESSAGE"))
	request.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	request.AppendHeader(sip.NewHeader("Content-Type", "application/vnd.3gpp.sms"))
	request.SetTransport("UDP")
	request.SetBody(body)
	return request
}
