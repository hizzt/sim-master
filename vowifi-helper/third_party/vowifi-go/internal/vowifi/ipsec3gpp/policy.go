package ipsec3gpp

import (
	"errors"
	"fmt"
	"net"
)

// Flow describes one direction of a 3GPP ipsec-3gpp security association.
type Flow struct {
	OutboundSPI uint32
	InboundSPI  uint32
	LocalPort   int
	RemotePort  int
	AuthAlg     string
	EncAlg      string
	CK          []byte
	IK          []byte
}

// Policy captures the negotiated ipsec-3gpp parameters for SIP-over-TCP ESP.
type Policy struct {
	LocalIP     []byte
	RemoteIP    []byte
	LocalPortC  int
	LocalPortS  int
	RemotePortC int
	RemotePortS int
	FlowC       Flow
	FlowS       Flow
}

// ReplayStats tracks anti-replay decisions.
type ReplayStats struct {
	Accepted  uint64
	Duplicate uint64
	TooOld    uint64
}

// TransportStats aggregates userspace ESP transform counters.
type TransportStats struct {
	OutboundPackets    uint64
	InboundPackets     uint64
	PassthroughPackets uint64
	TransformErrors    uint64
	Replay             ReplayStats
}

// PolicyInput is the minimum set of inputs required to build a Policy.
type PolicyInput struct {
	LocalIP  net.IP
	RemoteIP net.IP
	// ClientMech is the UE Security-Client offer. Its ports are local and
	// its SPIs protect packets received by the UE.
	ClientMech SecurityMechanism
	// ServerMech is the P-CSCF Security-Server offer. Its ports are remote
	// and its SPIs protect packets received by the P-CSCF.
	ServerMech SecurityMechanism
	// Mech is retained for callers that only have one symmetric mechanism.
	// New IMS sec-agree callers should provide ClientMech and ServerMech.
	Mech    SecurityMechanism
	CK      []byte
	IK      []byte
	AuthAlg string
	EncAlg  string
}

// NewPolicy builds a Policy from negotiated Security-Server parameters and AKA keys.
func NewPolicy(in PolicyInput) (Policy, error) {
	localIP, err := normalizeIP(in.LocalIP)
	if err != nil {
		return Policy{}, fmt.Errorf("ipsec3gpp: local IP %w", err)
	}
	remoteIP, err := normalizeIP(in.RemoteIP)
	if err != nil {
		return Policy{}, fmt.Errorf("ipsec3gpp: remote IP %w", err)
	}
	if len(in.CK) == 0 || len(in.IK) == 0 {
		return Policy{}, errors.New("ipsec3gpp: CK and IK are required")
	}

	clientMech, serverMech, explicitPair := resolvePolicyMechanisms(in)
	authAlg := canonicalAuthAlg(coalesce(in.AuthAlg, serverMech.Alg, clientMech.Alg))
	encAlg := canonicalEncAlg(coalesce(in.EncAlg, serverMech.EAlg, clientMech.EAlg))
	if authAlg == "" || encAlg == "" {
		return Policy{}, errors.New("ipsec3gpp: authentication and encryption algorithms are required")
	}
	if clientMech.SPIc == 0 || clientMech.SPIs == 0 || serverMech.SPIc == 0 || serverMech.SPIs == 0 {
		return Policy{}, errors.New("ipsec3gpp: client and server spi-c/spi-s are required")
	}

	ports := fillPolicyPorts(clientMech, serverMech, explicitPair)
	ck := append([]byte(nil), in.CK...)
	ik := append([]byte(nil), in.IK...)
	outboundC, inboundC := serverMech.SPIs, clientMech.SPIc
	outboundS, inboundS := serverMech.SPIc, clientMech.SPIs
	if !explicitPair {
		// Legacy symmetric callers historically treated spi-c as flow-c's
		// outbound SPI and spi-s as its inbound SPI.
		outboundC, inboundC = clientMech.SPIc, clientMech.SPIs
		outboundS, inboundS = clientMech.SPIs, clientMech.SPIc
	}

	// 3GPP TS 33.203 primary protected client flow:
	// UE port-c -> P-CSCF port-s uses the P-CSCF spi-s; the reverse flow
	// terminates on the UE spi-c advertised in Security-Client.
	flowC := Flow{
		OutboundSPI: outboundC,
		InboundSPI:  inboundC,
		LocalPort:   ports.localC,
		RemotePort:  ports.remoteS,
		AuthAlg:     authAlg,
		EncAlg:      encAlg,
		CK:          ck,
		IK:          ik,
	}
	// Server-side flow for requests initiated by the P-CSCF:
	// UE port-s -> P-CSCF port-c uses the P-CSCF spi-c; the reverse flow
	// terminates on the UE spi-s.
	flowS := Flow{
		OutboundSPI: outboundS,
		InboundSPI:  inboundS,
		LocalPort:   ports.localS,
		RemotePort:  ports.remoteC,
		AuthAlg:     authAlg,
		EncAlg:      encAlg,
		CK:          ck,
		IK:          ik,
	}

	return Policy{
		LocalIP:     localIP,
		RemoteIP:    remoteIP,
		LocalPortC:  ports.localC,
		LocalPortS:  ports.localS,
		RemotePortC: ports.remoteC,
		RemotePortS: ports.remoteS,
		FlowC:       flowC,
		FlowS:       flowS,
	}, nil
}

type portPair struct {
	localC, localS, remoteC, remoteS int
}

func resolvePolicyMechanisms(in PolicyInput) (client, server SecurityMechanism, explicitPair bool) {
	client = in.ClientMech
	server = in.ServerMech
	explicitPair = client.SPIc != 0 || client.SPIs != 0 || server.SPIc != 0 || server.SPIs != 0
	if client.SPIc == 0 && client.SPIs == 0 {
		client = in.Mech
	}
	if server.SPIc == 0 && server.SPIs == 0 {
		server = in.Mech
	}
	return client, server, explicitPair
}

func fillPolicyPorts(client, server SecurityMechanism, explicitPair bool) portPair {
	localC, localS := client.PortC, client.PortS
	remoteC, remoteS := server.PortC, server.PortS
	if localC == 0 {
		localC = 5060
	}
	if localS == 0 {
		localS = localC
	}
	// Preserve the historical one-mechanism fallback for non-sec-agree
	// callers while using the actual P-CSCF ports when both offers exist.
	if !explicitPair {
		remoteC, remoteS = 5060, 5060
	}
	if remoteC == 0 {
		remoteC = 5060
	}
	if remoteS == 0 {
		remoteS = remoteC
	}
	return portPair{
		localC:  localC,
		localS:  localS,
		remoteC: remoteC,
		remoteS: remoteS,
	}
}

func normalizeIP(ip net.IP) ([]byte, error) {
	if ip == nil {
		return nil, errors.New("must not be nil")
	}
	if v4 := ip.To4(); v4 != nil {
		return append([]byte(nil), v4...), nil
	}
	if v6 := ip.To16(); v6 != nil && ip.To4() == nil {
		return append([]byte(nil), v6...), nil
	}
	return nil, fmt.Errorf("invalid address %q", ip.String())
}

func normalizeIPPair(a, b []byte) (local, remote []byte, err error) {
	if len(a) == 0 || len(b) == 0 {
		return nil, nil, errors.New("ipsec3gpp: local/remote IP must not be nil")
	}
	if (len(a) == 4) != (len(b) == 4) {
		return nil, nil, errors.New("ipsec3gpp: local/remote IP family mismatch")
	}
	return append([]byte(nil), a...), append([]byte(nil), b...), nil
}

func coalesce(values ...string) string {
	for _, v := range values {
		if s := trimToken(v); s != "" {
			return s
		}
	}
	return ""
}

func ipEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
