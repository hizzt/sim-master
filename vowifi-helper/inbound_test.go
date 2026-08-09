package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/techblack/sim-master/vowifi-helper/smscodec"
	"github.com/1239t/vowifi-go/runtimehost/messaging"
)

func TestInboundDecoderDecodesDeliverAndAcknowledges(t *testing.T) {
	tpdu, err := hex.DecodeString("0005810180F600006270502143650005E8329BFD06")
	if err != nil {
		t.Fatal(err)
	}
	body := inboundRPDataForTest(0x34, "+447802002606", tpdu)
	report := newReporter(filepath.Join(t.TempDir(), "status.json"), tunnelStatus{})
	decoder := newInboundDecoder(report)
	inbound := messaging.InboundSMS{
		ID:         "call-123:34",
		CallID:     "call-123",
		Body:       body,
		ReceivedAt: time.Date(2026, 8, 8, 5, 0, 0, 0, time.UTC),
	}

	reply, err := decoder.handle(inbound)
	if err != nil {
		t.Fatalf("handle() error = %v", err)
	}
	if !bytes.Equal(reply.Body, smscodec.BuildRPAck(0x34)) {
		t.Fatalf("reply=%x", reply.Body)
	}
	pending := report.snapshot()
	if pending.SMSLastRXState != "rp_ack_pending" || pending.SMSLastRXFrom != "10086" || pending.SMSLastRXText != "hello" {
		t.Fatalf("pending status=%+v", pending)
	}

	decoder.handleResult(inbound, reply, 200, nil)
	complete := report.snapshot()
	if !complete.SMSRXVerified || complete.SMSLastRXState != "rp_acked" || complete.SMSLastRXRPAckSIPCode != 200 {
		t.Fatalf("complete status=%+v", complete)
	}
}

func TestInboundDecoderReturnsTemporaryFailureForMalformedDeliver(t *testing.T) {
	report := newReporter(filepath.Join(t.TempDir(), "status.json"), tunnelStatus{})
	decoder := newInboundDecoder(report)
	body := inboundRPDataForTest(0x35, "+447802002606", []byte{0x00})
	inbound := messaging.InboundSMS{ID: "call-456:35", Body: body, ReceivedAt: time.Now()}

	reply, err := decoder.handle(inbound)
	if err != nil {
		t.Fatalf("handle() error = %v", err)
	}
	want := smscodec.BuildRPError(0x35, smscodec.RPCauseTemporaryFailure)
	if !bytes.Equal(reply.Body, want) {
		t.Fatalf("reply=%x want=%x", reply.Body, want)
	}
	status := report.snapshot()
	if status.SMSLastRXState != "decode_failed" || status.SMSLastRXError == "" || status.SMSRXVerified {
		t.Fatalf("status=%+v", status)
	}
}

func TestInboundDecoderClearsPreviousSuccessBeforeMalformedDelivery(t *testing.T) {
	report := newReporter(filepath.Join(t.TempDir(), "status.json"), tunnelStatus{
		SMSRXVerified: true,
		SMSLastRXID:   "old-call:10", SMSLastRXFrom: "+100", SMSLastRXText: "old",
		SMSLastRXState: "rp_acked", SMSLastRXRPAckSIPCode: 200,
	})
	decoder := newInboundDecoder(report)
	inbound := messaging.InboundSMS{
		ID: "new-call:11", Body: inboundRPDataForTest(0x11, "+447802002606", []byte{0x00}),
		ReceivedAt: time.Now(),
	}

	_, err := decoder.handle(inbound)
	if err != nil {
		t.Fatal(err)
	}
	status := report.snapshot()
	if status.SMSRXVerified || status.SMSLastRXFrom != "" || status.SMSLastRXText != "" || status.SMSLastRXRPAckSIPCode != 0 {
		t.Fatalf("stale success leaked into failed delivery: %+v", status)
	}
}

func TestInboundDecoderRecordsOutOfOrderSuccessfulAcknowledgements(t *testing.T) {
	tpduA, _ := hex.DecodeString("0005810180F600006270502143650005E8329BFD06")
	tpduB := append([]byte(nil), tpduA...)
	report := newReporter(filepath.Join(t.TempDir(), "status.json"), tunnelStatus{})
	decoder := newInboundDecoder(report)
	first := messaging.InboundSMS{ID: "call-a:41", Body: inboundRPDataForTest(0x41, "+447802002606", tpduA), ReceivedAt: time.Now()}
	second := messaging.InboundSMS{ID: "call-b:42", Body: inboundRPDataForTest(0x42, "+447802002606", tpduB), ReceivedAt: time.Now().Add(time.Second)}
	firstReply, err := decoder.handle(first)
	if err != nil {
		t.Fatal(err)
	}
	secondReply, err := decoder.handle(second)
	if err != nil {
		t.Fatal(err)
	}

	decoder.handleResult(first, firstReply, 200, nil)
	status := report.snapshot()
	if status.SMSLastRXID != second.ID || status.SMSLastRXState != "rp_ack_pending" || status.SMSRXVerified {
		t.Fatalf("older ACK corrupted current receive state: %+v", status)
	}
	if len(status.SMSReceivedMessages) != 1 || status.SMSReceivedMessages[0].ID != first.ID {
		t.Fatalf("first completed receive history=%+v", status.SMSReceivedMessages)
	}

	decoder.handleResult(second, secondReply, 202, nil)
	status = report.snapshot()
	if !status.SMSRXVerified || status.SMSLastRXID != second.ID || status.SMSLastRXRPAckSIPCode != 202 {
		t.Fatalf("second completion status=%+v", status)
	}
	if len(status.SMSReceivedMessages) != 2 || status.SMSReceivedMessages[1].ID != second.ID {
		t.Fatalf("receive history=%+v", status.SMSReceivedMessages)
	}
}

func TestInboundDecoderRejectsMSOriginatedRPData(t *testing.T) {
	report := newReporter(filepath.Join(t.TempDir(), "status.json"), tunnelStatus{})
	decoder := newInboundDecoder(report)
	body := inboundRPDataForTest(0x52, "+447802002606", []byte{0x00})
	body[0] = 0x00
	reply, err := decoder.handle(messaging.InboundSMS{ID: "wrong-direction:52", Body: body, ReceivedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reply.Body, smscodec.BuildRPError(0x52, smscodec.RPCauseTemporaryFailure)) {
		t.Fatalf("reply=%x", reply.Body)
	}
	if report.snapshot().SMSLastRXState != "decode_failed" {
		t.Fatalf("status=%+v", report.snapshot())
	}
}

func TestNewestReceivedSMSWaitsAfterLatestID(t *testing.T) {
	messages := []receivedSMSStatus{{ID: "one"}, {ID: "two"}}
	if _, ok := newestReceivedSMS(messages, "two"); ok {
		t.Fatal("latest after_id must wait for a newer delivery")
	}
	if got, ok := newestReceivedSMS(messages, "one"); !ok || got.ID != "two" {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
	if got, ok := newestReceivedSMS(messages, "unknown"); !ok || got.ID != "two" {
		t.Fatalf("unknown baseline got=%+v ok=%v", got, ok)
	}
}

func TestWaitReceiveDoesNotMissAcknowledgementAfterSubscription(t *testing.T) {
	report := newReporter(filepath.Join(t.TempDir(), "status.json"), tunnelStatus{
		SMSReceivedMessages: []receivedSMSStatus{{ID: "baseline", From: "+100", RPAckSIPCode: 200}},
	})
	server := &controlServer{report: report}
	result := make(chan *smsReceiveResult, 1)
	go func() {
		result <- server.waitReceive(context.Background(), "baseline", time.Second)
	}()

	// The atomic snapshot/subscription pair guarantees that this update either
	// appears in the snapshot or closes the channel paired with that snapshot.
	report.mutate(func(status *tunnelStatus) {
		appendReceivedSMS(status, receivedSMSStatus{
			ID: "new-message", From: "+101", Text: "arrived", ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano), RPAckSIPCode: 200,
		})
	})
	select {
	case got := <-result:
		if !got.Verified || got.MessageID != "new-message" || got.Content != "arrived" {
			t.Fatalf("wait result=%+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait_receive missed a completed receive update")
	}
}

func TestReceivedSMSHistoryRemainsBoundedAndDeduplicated(t *testing.T) {
	status := tunnelStatus{}
	for i := 0; i < maxReceivedSMSHistory+20; i++ {
		appendReceivedSMS(&status, receivedSMSStatus{
			ID: fmt.Sprintf("message-%03d", i), From: "+100", RPAckSIPCode: 200,
		})
	}
	if len(status.SMSReceivedMessages) != maxReceivedSMSHistory {
		t.Fatalf("history length=%d", len(status.SMSReceivedMessages))
	}
	if got := status.SMSReceivedMessages[0].ID; got != "message-020" {
		t.Fatalf("oldest retained message=%q", got)
	}
	appendReceivedSMS(&status, receivedSMSStatus{
		ID: "message-100", From: "+100", Text: "updated", RPAckSIPCode: 202,
	})
	if len(status.SMSReceivedMessages) != maxReceivedSMSHistory {
		t.Fatalf("duplicate grew history to %d", len(status.SMSReceivedMessages))
	}
}

func TestDecodeDeliverTPDUHandlesUCS2ConcatPart(t *testing.T) {
	tpdu, err := hex.DecodeString("4005810180F6000862705021436500080500037A02014F60")
	if err != nil {
		t.Fatal(err)
	}
	sender, text, _, concat, err := smscodec.DecodeDeliverTPDU(tpdu)
	if err != nil {
		t.Fatalf("DecodeDeliverTPDU() error = %v", err)
	}
	if sender != "10086" || text != "你" || !concat.IsConcat || concat.Ref != 0x7a || concat.Total != 2 || concat.Seq != 1 {
		t.Fatalf("sender=%q text=%q concat=%+v", sender, text, concat)
	}
}

func TestDecodeDeliverTPDUHandlesAlphanumericSender(t *testing.T) {
	tpdu, err := hex.DecodeString("0006D0C7F7FBCC2E0300006270502143650005E8329BFD06")
	if err != nil {
		t.Fatal(err)
	}
	sender, text, _, concat, err := smscodec.DecodeDeliverTPDU(tpdu)
	if err != nil {
		t.Fatalf("DecodeDeliverTPDU() error = %v", err)
	}
	if sender != "Google" || text != "hello" || concat.IsConcat {
		t.Fatalf("sender=%q text=%q concat=%+v", sender, text, concat)
	}
}

func TestDecodeDeliverTPDURejectsSubmitTPDU(t *testing.T) {
	tpdus, _, err := smscodec.BuildSubmitTPDUs("+8615556250521", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := smscodec.DecodeDeliverTPDU(tpdus[0]); err == nil {
		t.Fatal("SMS-SUBMIT must not be accepted as an inbound SMS-DELIVER")
	}
}

func inboundRPDataForTest(rpMR byte, originator string, tpdu []byte) []byte {
	body := []byte{0x01, rpMR}
	body = append(body, smscodec.EncodeAddress(originator)...)
	body = append(body, 0x00, byte(len(tpdu)))
	body = append(body, tpdu...)
	return body
}
