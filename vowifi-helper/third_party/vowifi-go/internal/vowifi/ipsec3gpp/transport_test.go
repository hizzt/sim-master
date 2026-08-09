package ipsec3gpp

import (
	"bytes"
	"net"
	"sync"
	"testing"
)

type testInnerDataplane struct {
	rx   chan []byte
	mu   sync.Mutex
	sent [][]byte
}

func newTestInnerDataplane() *testInnerDataplane {
	return &testInnerDataplane{rx: make(chan []byte, 4)}
}

func (d *testInnerDataplane) SendInnerPacket(packet []byte) error {
	d.mu.Lock()
	d.sent = append(d.sent, append([]byte(nil), packet...))
	d.mu.Unlock()
	return nil
}

func (d *testInnerDataplane) InnerPackets() <-chan []byte { return d.rx }

func TestNewPolicyAndTransport(t *testing.T) {
	ck := bytes.Repeat([]byte{0x01}, 16)
	ik := bytes.Repeat([]byte{0x02}, 16)
	policy, err := NewPolicy(PolicyInput{
		LocalIP:  net.ParseIP("10.0.0.2"),
		RemoteIP: net.ParseIP("10.0.0.1"),
		CK:       ck,
		IK:       ik,
		AuthAlg:  "hmac-sha-1-96",
		EncAlg:   "aes-cbc",
		Mech: SecurityMechanism{
			Alg:   "hmac-sha-1-96",
			EAlg:  "aes-cbc",
			Prot:  "esp",
			Mode:  "trans",
			SPIc:  0x11111111,
			SPIs:  0x22222222,
			PortC: 6054,
			PortS: 6060,
		},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	if policy.FlowC.OutboundSPI != 0x11111111 || policy.FlowS.OutboundSPI != 0x22222222 {
		t.Fatalf("unexpected flow SPIs: %+v %+v", policy.FlowC, policy.FlowS)
	}
	if _, err := NewTransport(policy); err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
}

func TestNewPolicyMapsSecurityClientAndServerDirections(t *testing.T) {
	policy, err := NewPolicy(PolicyInput{
		LocalIP:  net.ParseIP("2001:db8::2"),
		RemoteIP: net.ParseIP("2001:db8::1"),
		CK:       bytes.Repeat([]byte{0x01}, 16),
		IK:       bytes.Repeat([]byte{0x02}, 16),
		ClientMech: SecurityMechanism{
			Alg: "hmac-sha-1-96", EAlg: "aes-cbc",
			SPIc: 0x11111111, SPIs: 0x22222222,
			PortC: 15064, PortS: 15063,
		},
		ServerMech: SecurityMechanism{
			Alg: "hmac-sha-1-96", EAlg: "aes-cbc",
			SPIc: 0x33333333, SPIs: 0x44444444,
			PortC: 25064, PortS: 25063,
		},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	if policy.LocalPortC != 15064 || policy.LocalPortS != 15063 ||
		policy.RemotePortC != 25064 || policy.RemotePortS != 25063 {
		t.Fatalf("unexpected policy ports: %+v", policy)
	}
	if policy.FlowC.OutboundSPI != 0x44444444 || policy.FlowC.InboundSPI != 0x11111111 ||
		policy.FlowC.LocalPort != 15064 || policy.FlowC.RemotePort != 25063 {
		t.Fatalf("unexpected protected client flow: %+v", policy.FlowC)
	}
	if policy.FlowS.OutboundSPI != 0x33333333 || policy.FlowS.InboundSPI != 0x22222222 ||
		policy.FlowS.LocalPort != 15063 || policy.FlowS.RemotePort != 25064 {
		t.Fatalf("unexpected protected server flow: %+v", policy.FlowS)
	}
}

func TestTransportOutboundInboundIPv4(t *testing.T) {
	ck := bytes.Repeat([]byte{0x01}, 16)
	ik := bytes.Repeat([]byte{0x02}, 16)
	policy, err := NewPolicy(PolicyInput{
		LocalIP:  net.ParseIP("10.0.0.2"),
		RemoteIP: net.ParseIP("10.0.0.1"),
		CK:       ck,
		IK:       ik,
		AuthAlg:  "hmac-sha-1-96",
		EncAlg:   "aes-cbc",
		Mech: SecurityMechanism{
			Alg:   "hmac-sha-1-96",
			EAlg:  "aes-cbc",
			Prot:  "esp",
			Mode:  "trans",
			SPIc:  0x11111111,
			SPIs:  0x22222222,
			PortC: 6054,
			PortS: 6060,
		},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	transport, err := NewTransport(policy)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}

	sip := []byte("REGISTER sip:ims.example.org SIP/2.0\r\n\r\n")
	plainPacket, err := buildOutboundTCPPacket(policy, sip)
	if err != nil {
		t.Fatalf("buildOutboundTCPPacket: %v", err)
	}
	encrypted, err := transport.TransformOutbound(plainPacket)
	if err != nil {
		t.Fatalf("TransformOutbound: %v", err)
	}
	parsed, err := parseIPPacket(encrypted)
	if err != nil {
		t.Fatalf("parseIPPacket encrypted: %v", err)
	}
	if parsed.nextHeader != ipProtoESP {
		t.Fatalf("expected ESP protocol, got %d", parsed.nextHeader)
	}

	// Simulate a server-originated ESP packet (SPIs) back to the UE.
	serverFlow := transport.outbound[1]
	tcpPayload := buildMinimalTCPSegment(policy.FlowS.RemotePort, policy.FlowS.LocalPort, sip)
	esp, err := encapsulateTransport(tcpPayload, serverFlow.sa, ipProtoTCP)
	if err != nil {
		t.Fatalf("encapsulateTransport: %v", err)
	}
	inboundIP := buildIPv4Packet(policy.RemoteIP, policy.LocalIP, ipProtoESP, esp)
	decrypted, err := transport.TransformInbound(inboundIP)
	if err != nil {
		t.Fatalf("TransformInbound: %v", err)
	}
	gotParsed, err := parseIPPacket(decrypted)
	if err != nil {
		t.Fatalf("parseIPPacket decrypted: %v", err)
	}
	if !bytes.Contains(gotParsed.transportPayload, sip) {
		t.Fatalf("missing SIP payload in %x", gotParsed.transportPayload)
	}
}

func TestSecuredDataplaneTransformsIPv6AtPacketLayer(t *testing.T) {
	policy, err := NewPolicy(PolicyInput{
		LocalIP:  net.ParseIP("2001:db8::2"),
		RemoteIP: net.ParseIP("2001:db8::1"),
		CK:       bytes.Repeat([]byte{0x11}, 16),
		IK:       bytes.Repeat([]byte{0x22}, 16),
		ClientMech: SecurityMechanism{
			Alg: "hmac-sha-1-96", EAlg: "aes-cbc",
			SPIc: 0x11111111, SPIs: 0x22222222,
			PortC: 15064, PortS: 15063,
		},
		ServerMech: SecurityMechanism{
			Alg: "hmac-sha-1-96", EAlg: "aes-cbc",
			SPIc: 0x33333333, SPIs: 0x44444444,
			PortC: 25064, PortS: 25063,
		},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	transport, err := NewTransport(policy)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	inner := newTestInnerDataplane()
	dp, err := NewSecuredDataplane(inner)
	if err != nil {
		t.Fatalf("NewSecuredDataplane: %v", err)
	}
	defer dp.Close()
	if err := dp.Install(transport); err != nil {
		t.Fatalf("Install: %v", err)
	}

	plainOutbound := buildIPv6Packet(
		policy.LocalIP,
		policy.RemoteIP,
		ipProtoTCP,
		buildMinimalTCPSegment(policy.LocalPortC, policy.RemotePortS, []byte("REGISTER")),
	)
	if err := dp.SendInnerPacket(plainOutbound); err != nil {
		t.Fatalf("SendInnerPacket: %v", err)
	}
	inner.mu.Lock()
	if len(inner.sent) != 1 {
		inner.mu.Unlock()
		t.Fatalf("sent packets = %d, want 1", len(inner.sent))
	}
	encryptedOutbound := append([]byte(nil), inner.sent[0]...)
	inner.mu.Unlock()
	parsedOutbound, err := parseIPPacket(encryptedOutbound)
	if err != nil {
		t.Fatalf("parse outbound ESP: %v", err)
	}
	if parsedOutbound.nextHeader != ipProtoESP {
		t.Fatalf("outbound protocol = %d, want ESP", parsedOutbound.nextHeader)
	}
	spi, _, err := parseESPSPISeq(parsedOutbound.transportPayload)
	if err != nil {
		t.Fatalf("parse outbound SPI: %v", err)
	}
	if spi != policy.FlowC.OutboundSPI {
		t.Fatalf("outbound SPI = %#x, want %#x", spi, policy.FlowC.OutboundSPI)
	}

	// Build the P-CSCF response with the UE's advertised spi-c and feed it
	// into the SWu side of the wrapper. The TCP netstack must receive plain
	// IPv6+TCP, never ESP bytes inside a TCP stream.
	inboundFlow := policy.FlowC
	inboundFlow.OutboundSPI = policy.FlowC.InboundSPI
	inboundSA, err := newSAForFlow(inboundFlow)
	if err != nil {
		t.Fatalf("new inbound SA: %v", err)
	}
	inboundTCP := buildMinimalTCPSegment(policy.RemotePortS, policy.LocalPortC, []byte("SIP/2.0 200 OK"))
	inboundESP, err := encapsulateTransport(inboundTCP, inboundSA, ipProtoTCP)
	if err != nil {
		t.Fatalf("encapsulate inbound: %v", err)
	}
	inner.rx <- buildIPv6Packet(policy.RemoteIP, policy.LocalIP, ipProtoESP, inboundESP)
	plainInbound := <-dp.InnerPackets()
	parsedInbound, err := parseIPPacket(plainInbound)
	if err != nil {
		t.Fatalf("parse inbound plain packet: %v", err)
	}
	if parsedInbound.nextHeader != ipProtoTCP {
		t.Fatalf("inbound protocol = %d, want TCP", parsedInbound.nextHeader)
	}
	if !bytes.Contains(parsedInbound.transportPayload, []byte("SIP/2.0 200 OK")) {
		t.Fatalf("inbound SIP payload missing: %x", parsedInbound.transportPayload)
	}
}
