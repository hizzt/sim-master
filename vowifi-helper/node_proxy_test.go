//go:build jane_vowifi

package main

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/jane-rui/vowifi-go/engine/swu/ikev2"
)

func TestIKEExchangeTargetMovesFrom500To4500(t *testing.T) {
	configured := &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 500}
	initial := make([]byte, 28)
	initial[18] = byte(ikev2.ExchangeIKE_SA_INIT)
	remote, marker := ikeExchangeTarget(configured, initial)
	if remote.Port != 500 || marker {
		t.Fatalf("IKE_SA_INIT target=%v marker=%t", remote, marker)
	}
	auth := make([]byte, 28)
	auth[18] = byte(ikev2.ExchangeIKE_AUTH)
	binary.BigEndian.PutUint32(auth[20:24], 1)
	remote, marker = ikeExchangeTarget(configured, auth)
	if remote.Port != 4500 || !marker {
		t.Fatalf("IKE_AUTH target=%v marker=%t", remote, marker)
	}
}

func TestSOCKS5UDPDatagramRoundTripIPv6(t *testing.T) {
	target := &net.UDPAddr{IP: net.ParseIP("2001:db8::5"), Port: 4500}
	wire, err := encodeSOCKS5UDPDatagram([]byte("ike"), target)
	if err != nil {
		t.Fatal(err)
	}
	payload, decoded, err := decodeSOCKS5UDPDatagram(wire)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "ike" || !sameUDPAddress(decoded, target) {
		t.Fatalf("payload=%q decoded=%v", payload, decoded)
	}
}

func TestCarrierCompatibleIKEProposal(t *testing.T) {
	proposal := carrierCompatibleIKEProposal().Proposals[0]
	want := map[uint8]uint16{
		ikev2.TransformENCR:     ikev2.ENCR_AES_CBC,
		ikev2.TransformPRF:      ikev2.PRF_HMAC_SHA1,
		ikev2.TransformINTEG:    ikev2.INTEG_HMAC_SHA1_96,
		ikev2.TransformDHRGroup: ikev2.DHGroup2048BitMODP,
	}
	for _, transform := range proposal.Transforms {
		if want[transform.Type] != transform.ID {
			t.Fatalf("transform type=%d id=%d", transform.Type, transform.ID)
		}
		delete(want, transform.Type)
	}
	if len(want) != 0 {
		t.Fatalf("missing transforms: %v", want)
	}
}
