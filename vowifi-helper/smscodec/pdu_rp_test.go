package smscodec

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/warthog618/sms/encoding/tpdu"
)

func TestClassifyRPDU(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		kind RPDUKind
	}{
		{name: "rp-data-ms", in: []byte{0x00, 0x01}, kind: RPDUKindData},
		{name: "rp-data-net", in: []byte{0x01, 0x01}, kind: RPDUKindData},
		{name: "rp-ack-ms", in: []byte{0x02, 0x01}, kind: RPDUKindAck},
		{name: "rp-ack-net", in: []byte{0x03, 0x01}, kind: RPDUKindAck},
		{name: "rp-error-ms", in: []byte{0x04, 0x0A, 0x01, 0x29, 0x00}, kind: RPDUKindError},
		{name: "rp-error-net", in: []byte{0x05, 0x0A, 0x01, 0x29, 0x00}, kind: RPDUKindError},
		{name: "unknown", in: []byte{0x7F, 0x01}, kind: RPDUKindUnknown},
		{name: "empty", in: []byte{}, kind: RPDUKindUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyRPDU(tc.in)
			if got.Kind != tc.kind {
				t.Fatalf("kind mismatch: got=%s want=%s", got.Kind, tc.kind)
			}
		})
	}
}

func TestParseRPErrorCause_VariableLengthIE(t *testing.T) {
	// cause IE length = 3: cause + 2 bytes diagnostics
	body := []byte{0x04, 0x22, 0x03, 0xA9, 0x12, 0x34, 0x00}
	cause, err := ParseRPErrorCause(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 0xA9 & 0x7F = 0x29
	if cause != 0x29 {
		t.Fatalf("cause mismatch: got=%d want=%d", cause, 0x29)
	}
}

func TestParseRPErrorCause_Invalid(t *testing.T) {
	if _, err := ParseRPErrorCause([]byte{0x04, 0x01, 0x00}); err == nil {
		t.Fatalf("expected error for empty cause IE")
	}
	if _, err := ParseRPErrorCause([]byte{0x02, 0x01, 0x01, 0x29}); err == nil {
		t.Fatalf("expected error for non RP-ERROR")
	}
}

func TestParseRPDataNetworkToMSUsesOriginatorThenDestination(t *testing.T) {
	tpdu, err := hex.DecodeString("0005810180F600006270502143650005E8329BFD06")
	if err != nil {
		t.Fatal(err)
	}
	originator := EncodeAddress("+447802002606")
	body := []byte{0x01, 0x34}
	body = append(body, originator...)
	body = append(body, 0x00, byte(len(tpdu)))
	body = append(body, tpdu...)

	rpMR, oa, da, gotTPDU, err := ParseRPDataWithAddresses(body)
	if err != nil {
		t.Fatalf("ParseRPDataWithAddresses() error = %v", err)
	}
	if rpMR != 0x34 || oa != "+447802002606" || da != "" || !bytes.Equal(gotTPDU, tpdu) {
		t.Fatalf("rpMR=%02x oa=%q da=%q tpdu=%x", rpMR, oa, da, gotTPDU)
	}
	parsedMR, parsedTPDU, err := ParseRPData(body)
	if err != nil || parsedMR != rpMR || !bytes.Equal(parsedTPDU, tpdu) {
		t.Fatalf("ParseRPData()=(%02x,%x,%v)", parsedMR, parsedTPDU, err)
	}
}

func TestParseRPDataRejectsTrailingBytes(t *testing.T) {
	if _, _, _, _, err := ParseRPDataWithAddresses([]byte{0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0xff}); err == nil {
		t.Fatal("expected trailing-byte error")
	}
}

func TestSubmitRPDataKeepsRecipientInTPDUAndSMSCInRPDestination(t *testing.T) {
	tpdus, _, err := BuildSubmitTPDUs("+8615556250521", "test message")
	if err != nil {
		t.Fatalf("BuildSubmitTPDUs: %v", err)
	}
	body := BuildRPData(0x01, tpdus[0], "+447802002606")
	rpMR, originator, destination, encodedTPDU, err := ParseRPDataWithAddresses(body)
	if err != nil {
		t.Fatalf("ParseRPDataWithAddresses: %v", err)
	}
	if rpMR != 0x01 || originator != "" || destination != "+447802002606" {
		t.Fatalf("RP-DATA rpMR=%02x originator=%q destination=%q", rpMR, originator, destination)
	}
	pdu := &tpdu.TPDU{Direction: tpdu.MO}
	if err := pdu.UnmarshalBinary(encodedTPDU); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if got := pdu.DA.Number(); got != "+8615556250521" {
		t.Fatalf("TP-Destination-Address = %q", got)
	}
}
