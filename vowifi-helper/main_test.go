package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/techblack/sim-master/vowifi-helper/smscodec"
	"github.com/1239t/vowifi-go/runtimehost"
	"github.com/1239t/vowifi-go/runtimehost/eventhost"
)

func TestSIPCodeFromRuntimeError(t *testing.T) {
	if code := sipCodeFromError("register rejected with SIP 403 Forbidden"); code != 403 {
		t.Fatalf("code = %d", code)
	}
	if code := sipCodeFromError("voiceclient: submit part 0: unexpected response 401 Unauthorized"); code != 401 {
		t.Fatalf("unexpected response code = %d", code)
	}
	if code := sipCodeFromError("network timed out"); code != 0 {
		t.Fatalf("unexpected code = %d", code)
	}
	if code := sipCodeFromError("read tcp [2a03:dd00:1401:6c27:f49f:8fc8:271:6f81]:15133: i/o timeout"); code != 0 {
		t.Fatalf("IPv6 segment was misclassified as SIP code: %d", code)
	}
}

func TestSMSRequestStatusReportMatchesAcceptedARequestByDefault(t *testing.T) {
	if smsRequestStatusReport(nil) {
		t.Fatal("nil request enabled TP-SRR, unlike the captured A request")
	}
	disabled := false
	if smsRequestStatusReport(&disabled) {
		t.Fatal("explicit false unexpectedly enables TP-SRR")
	}
	enabled := true
	if !smsRequestStatusReport(&enabled) {
		t.Fatal("explicit true did not enable TP-SRR")
	}
}

func TestDefaultARequestMatchesCapturedRPDATAShape(t *testing.T) {
	tpdus, _, err := smscodec.BuildSubmitTPDUsWithOptions(
		"+447724304579",
		"A",
		smscodec.SubmitOptions{RequestStatusReport: smsRequestStatusReport(nil)},
	)
	if err != nil {
		t.Fatalf("build A TPDU: %v", err)
	}
	if len(tpdus) != 1 {
		t.Fatalf("A TPDU parts = %d, want 1", len(tpdus))
	}
	if firstOctet := tpdus[0][0]; firstOctet != 0x01 {
		t.Fatalf("A SMS-SUBMIT first octet = 0x%02x, want captured 0x01", firstOctet)
	}
	rpData := smscodec.BuildRPData(0x01, tpdus[0], "+447802002606")
	if len(rpData) != 26 || rpData[len(rpData)-1] != 'A' {
		t.Fatalf("A RP-DATA shape = len %d tail 0x%02x, want len 26 tail 0x41", len(rpData), rpData[len(rpData)-1])
	}
}

func TestResetStoppedStatusClearsSessionEvidence(t *testing.T) {
	status := tunnelStatus{
		Running:             true,
		Established:         true,
		TunnelIPv6:          "2001:db8::1",
		PCSCFv6:             []string{"2001:db8::2"},
		PCSCFReachable:      true,
		PCSCFSIPCode:        200,
		IMSRegistered:       true,
		IMSAuthenticated:    true,
		IMSIPSecEstablished: true,
		SMSTXVerified:       true,
		SMSRXVerified:       true,
		Error:               "old failure",
	}
	resetStoppedStatus(&status)
	if status.Running || status.Established || status.PCSCFReachable || status.IMSRegistered || status.SMSTXVerified || status.SMSRXVerified {
		t.Fatalf("stale session evidence remains: %+v", status)
	}
	if status.TunnelIPv6 != "" || len(status.PCSCFv6) != 0 || status.Error != "" {
		t.Fatalf("stale session values remain: %+v", status)
	}
}

func TestDeliveryStoreRequiresAllRPAcks(t *testing.T) {
	store := newMemoryDeliveryStore()
	now := time.Now()
	if err := store.CreateSMSDelivery("message-1", "", "modem-1", "+100", "hello", 2, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSMSDeliveryPart("message-1", 0, "call-0", 10, "pending", now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSMSDeliveryPart("message-1", 1, "call-1", 11, "pending", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkSMSDeliveryPartReport("call-0", "report-0", "modem-1", 10, "acked", 200, 0, "", now); err != nil {
		t.Fatal(err)
	}
	status, err := store.GetSMSDeliveryStatus("message-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pending" || status.Acks != 1 {
		t.Fatalf("status = %s, acks = %d", status.State, status.Acks)
	}
	if _, err := store.MarkSMSDeliveryPartReport("call-1", "report-1", "modem-1", 11, "acked", 200, 0, "", now); err != nil {
		t.Fatal(err)
	}
	status, _ = store.GetSMSDeliveryStatus("message-1")
	if status.State != "acked" || status.Acks != 2 {
		t.Fatalf("status = %s, acks = %d", status.State, status.Acks)
	}
}

func TestReceiveResultIncludesRPAckEvidence(t *testing.T) {
	result := receiveResult(tunnelStatus{
		SMSRXVerified:         true,
		SMSLastRXID:           "call:01",
		SMSLastRXFrom:         "+123",
		SMSLastRXText:         "hello",
		SMSLastRXRPMR:         1,
		SMSLastRXRPAckSIPCode: 200,
	})
	if !result.Verified || result.RPAckCode != 200 {
		t.Fatalf("result = %+v", result)
	}
}

func TestRuntimePhoneNumberReachesHelperStatusWithoutPrivateIdentity(t *testing.T) {
	report := newReporter(filepath.Join(t.TempDir(), "status.json"), tunnelStatus{PhoneNumber: "+447700900000"})
	report.applyRuntimeState(runtimehost.State{}, nil)
	if got := report.snapshot().PhoneNumber; got != "+447700900000" {
		t.Fatalf("empty runtime number overwrote cached number: %q", got)
	}
	report.applyRuntimeState(runtimehost.State{PhoneNumber: "+447700900123"}, nil)
	if got := report.snapshot().PhoneNumber; got != "+447700900123" {
		t.Fatalf("phone number = %q", got)
	}

	dispatcher := runtimeEventDispatcher{report: report}
	dispatcher.Dispatch(context.Background(), eventhost.LocalNumberLearned{
		DevID:  "modem-1",
		IMSI:   "must-not-be-persisted",
		Number: "+447700900456",
		Source: "register",
	})
	snapshot := report.snapshot()
	if snapshot.PhoneNumber != "+447700900456" || snapshot.IMSIprefix != "" {
		t.Fatalf("status leaked or lost identity data: %+v", snapshot)
	}
}

func TestReceivedSMSHistorySurvivesHelperStatusRestart(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "vowifi-tunnel.json")
	previous := tunnelStatus{
		SMSReceivedMessages: []receivedSMSStatus{
			{ID: "call-a:11", From: "+100", Text: "first", ReceivedAt: "2026-08-08T10:00:00Z", RPMR: 0x11, RPAckSIPCode: 200},
			{ID: "call-b:12", From: "+101", Text: "second", ReceivedAt: "2026-08-08T10:00:01Z", RPMR: 0x12, RPAckSIPCode: 202},
			{ID: "failed", From: "+102", Text: "ignore", ReceivedAt: "2026-08-08T10:00:02Z", RPMR: 0x13, RPAckSIPCode: 500},
		},
	}
	encoded, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	restarted := tunnelStatus{Stage: "starting", Running: true}
	restoreReceivedSMSState(statusPath, &restarted)
	if len(restarted.SMSReceivedMessages) != 2 || restarted.SMSLastRXID != "call-b:12" || !restarted.SMSRXVerified {
		t.Fatalf("restored status=%+v", restarted)
	}
	report := newReporter(statusPath, restarted)
	report.update("starting", nil)

	var persisted tunnelStatus
	file, err := os.Open(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.SMSReceivedMessages) != 2 || persisted.SMSReceivedMessages[1].ID != "call-b:12" {
		t.Fatalf("persisted restart history=%+v", persisted.SMSReceivedMessages)
	}
}
