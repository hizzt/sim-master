//go:build jane_vowifi

package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jane-rui/vowifi-go/engine/sim"
	"github.com/jane-rui/vowifi-go/engine/swu"
	"github.com/jane-rui/vowifi-go/engine/swu/ikev2"
)

const (
	socksVersion       = 0x05
	socksAuthNone      = 0x00
	socksAuthPassword  = 0x02
	socksUDPAssociate  = 0x03
	socksAddressIPv4   = 0x01
	socksAddressDomain = 0x03
	socksAddressIPv6   = 0x04
)

type nodeProxyConfig struct {
	Address  string
	Username string
	Password string
}

type socks5UDPAssociation struct {
	mu      sync.Mutex
	control net.Conn
	packet  *net.UDPConn
	relay   *net.UDPAddr
	closed  bool
}

func dialNodeProxyUDP(ctx context.Context, cfg nodeProxyConfig) (*socks5UDPAssociation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	address := strings.TrimSpace(cfg.Address)
	if address == "" {
		return nil, errors.New("SOCKS5 proxy address is empty")
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	control, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 TCP connect %s: %w", address, err)
	}
	fail := func(cause error) (*socks5UDPAssociation, error) {
		_ = control.Close()
		return nil, cause
	}
	if deadline := proxyDeadline(ctx, 10*time.Second); !deadline.IsZero() {
		_ = control.SetDeadline(deadline)
	}
	if err := socks5Negotiate(control, cfg.Username, cfg.Password); err != nil {
		return fail(err)
	}
	relay, err := socks5Associate(control)
	if err != nil {
		return fail(err)
	}
	if relay.IP == nil || relay.IP.IsUnspecified() {
		if remote, ok := control.RemoteAddr().(*net.TCPAddr); ok {
			relay.IP = append(net.IP(nil), remote.IP...)
		}
	}
	if relay.IP == nil || relay.IP.IsUnspecified() || relay.Port <= 0 {
		return fail(errors.New("SOCKS5 UDP ASSOCIATE returned an unusable relay"))
	}
	packet, err := net.ListenUDP("udp", nil)
	if err != nil {
		return fail(fmt.Errorf("SOCKS5 UDP socket: %w", err))
	}
	_ = control.SetDeadline(time.Time{})
	return &socks5UDPAssociation{control: control, packet: packet, relay: relay}, nil
}

func socks5Negotiate(conn net.Conn, username, password string) error {
	methods := []byte{socksAuthNone}
	if username != "" || password != "" {
		methods = []byte{socksAuthPassword, socksAuthNone}
	}
	request := append([]byte{socksVersion, byte(len(methods))}, methods...)
	if _, err := conn.Write(request); err != nil {
		return fmt.Errorf("SOCKS5 method write: %w", err)
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return fmt.Errorf("SOCKS5 method read: %w", err)
	}
	if response[0] != socksVersion {
		return fmt.Errorf("SOCKS5 version mismatch: 0x%02x", response[0])
	}
	switch response[1] {
	case socksAuthNone:
		return nil
	case socksAuthPassword:
		if len(username) > 255 || len(password) > 255 {
			return errors.New("SOCKS5 credentials exceed 255 bytes")
		}
		auth := []byte{0x01, byte(len(username))}
		auth = append(auth, username...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if _, err := conn.Write(auth); err != nil {
			return fmt.Errorf("SOCKS5 auth write: %w", err)
		}
		result := make([]byte, 2)
		if _, err := io.ReadFull(conn, result); err != nil {
			return fmt.Errorf("SOCKS5 auth read: %w", err)
		}
		if result[0] != 0x01 || result[1] != 0x00 {
			return fmt.Errorf("SOCKS5 authentication rejected: 0x%02x", result[1])
		}
		return nil
	default:
		return fmt.Errorf("SOCKS5 proxy rejected authentication methods: 0x%02x", response[1])
	}
}

func socks5Associate(conn net.Conn) (*net.UDPAddr, error) {
	request := []byte{socksVersion, socksUDPAssociate, 0x00, socksAddressIPv4, 0, 0, 0, 0, 0, 0}
	if _, err := conn.Write(request); err != nil {
		return nil, fmt.Errorf("SOCKS5 UDP ASSOCIATE write: %w", err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("SOCKS5 UDP ASSOCIATE read: %w", err)
	}
	if header[0] != socksVersion || header[1] != 0x00 {
		return nil, fmt.Errorf("SOCKS5 UDP ASSOCIATE rejected: status 0x%02x", header[1])
	}
	host, err := readSOCKSAddress(conn, header[3])
	if err != nil {
		return nil, err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return nil, fmt.Errorf("SOCKS5 UDP relay port read: %w", err)
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	if port == 0 {
		return nil, errors.New("SOCKS5 UDP relay returned port 0")
	}
	address, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 UDP relay resolve: %w", err)
	}
	return address, nil
}

func readSOCKSAddress(reader io.Reader, addressType byte) (string, error) {
	switch addressType {
	case socksAddressIPv4:
		value := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", fmt.Errorf("SOCKS5 IPv4 address read: %w", err)
		}
		return net.IP(value).String(), nil
	case socksAddressIPv6:
		value := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", fmt.Errorf("SOCKS5 IPv6 address read: %w", err)
		}
		return net.IP(value).String(), nil
	case socksAddressDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", fmt.Errorf("SOCKS5 domain length read: %w", err)
		}
		value := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", fmt.Errorf("SOCKS5 domain read: %w", err)
		}
		return string(value), nil
	default:
		return "", fmt.Errorf("SOCKS5 unsupported address type: 0x%02x", addressType)
	}
}

func (c *socks5UDPAssociation) sendTo(ctx context.Context, payload []byte, target *net.UDPAddr, timeout time.Duration) error {
	packet, relay, err := c.usable()
	if err != nil {
		return err
	}
	wire, err := encodeSOCKS5UDPDatagram(payload, target)
	if err != nil {
		return err
	}
	if err := packet.SetWriteDeadline(proxyDeadline(ctx, timeout)); err != nil {
		return err
	}
	_, err = packet.WriteToUDP(wire, relay)
	return proxyNetworkError(ctx, err)
}

func (c *socks5UDPAssociation) receiveFrom(ctx context.Context, timeout time.Duration) ([]byte, *net.UDPAddr, error) {
	packet, _, err := c.usable()
	if err != nil {
		return nil, nil, err
	}
	if err := packet.SetReadDeadline(proxyDeadline(ctx, timeout)); err != nil {
		return nil, nil, err
	}
	buffer := make([]byte, 65535+262)
	n, _, err := packet.ReadFromUDP(buffer)
	if err != nil {
		return nil, nil, proxyNetworkError(ctx, err)
	}
	payload, source, err := decodeSOCKS5UDPDatagram(buffer[:n])
	return payload, source, err
}

func (c *socks5UDPAssociation) usable() (*net.UDPConn, *net.UDPAddr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.packet == nil || c.relay == nil {
		return nil, nil, net.ErrClosed
	}
	return c.packet, cloneUDPAddr(c.relay), nil
}

func (c *socks5UDPAssociation) localAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.packet == nil {
		return nil
	}
	return c.packet.LocalAddr()
}

func (c *socks5UDPAssociation) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	packet := c.packet
	control := c.control
	c.packet = nil
	c.control = nil
	c.mu.Unlock()
	var result error
	if packet != nil {
		result = errors.Join(result, packet.Close())
	}
	if control != nil {
		result = errors.Join(result, control.Close())
	}
	return result
}

func encodeSOCKS5UDPDatagram(payload []byte, target *net.UDPAddr) ([]byte, error) {
	if target == nil || target.IP == nil || target.Port <= 0 || target.Port > 65535 {
		return nil, errors.New("SOCKS5 UDP target is invalid")
	}
	wire := []byte{0, 0, 0}
	if ipv4 := target.IP.To4(); ipv4 != nil {
		wire = append(wire, socksAddressIPv4)
		wire = append(wire, ipv4...)
	} else if ipv6 := target.IP.To16(); ipv6 != nil {
		wire = append(wire, socksAddressIPv6)
		wire = append(wire, ipv6...)
	} else {
		return nil, errors.New("SOCKS5 UDP target IP is invalid")
	}
	wire = append(wire, byte(target.Port>>8), byte(target.Port))
	wire = append(wire, payload...)
	return wire, nil
}

func decodeSOCKS5UDPDatagram(wire []byte) ([]byte, *net.UDPAddr, error) {
	if len(wire) < 4 || wire[0] != 0 || wire[1] != 0 {
		return nil, nil, errors.New("invalid SOCKS5 UDP datagram header")
	}
	if wire[2] != 0 {
		return nil, nil, errors.New("fragmented SOCKS5 UDP datagrams are unsupported")
	}
	offset := 4
	var host string
	switch wire[3] {
	case socksAddressIPv4:
		if len(wire) < offset+net.IPv4len+2 {
			return nil, nil, io.ErrUnexpectedEOF
		}
		host = net.IP(wire[offset : offset+net.IPv4len]).String()
		offset += net.IPv4len
	case socksAddressIPv6:
		if len(wire) < offset+net.IPv6len+2 {
			return nil, nil, io.ErrUnexpectedEOF
		}
		host = net.IP(wire[offset : offset+net.IPv6len]).String()
		offset += net.IPv6len
	case socksAddressDomain:
		if len(wire) < offset+1 {
			return nil, nil, io.ErrUnexpectedEOF
		}
		length := int(wire[offset])
		offset++
		if len(wire) < offset+length+2 {
			return nil, nil, io.ErrUnexpectedEOF
		}
		host = string(wire[offset : offset+length])
		offset += length
	default:
		return nil, nil, fmt.Errorf("unsupported SOCKS5 UDP address type: 0x%02x", wire[3])
	}
	port := int(binary.BigEndian.Uint16(wire[offset : offset+2]))
	offset += 2
	source, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, nil, err
	}
	return append([]byte(nil), wire[offset:]...), source, nil
}

func proxyDeadline(ctx context.Context, timeout time.Duration) time.Time {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
			deadline = ctxDeadline
		}
	}
	return deadline
}

func proxyNetworkError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func cloneUDPAddr(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), address.IP...), Port: address.Port, Zone: address.Zone}
}

type nodeProxyIKETransport struct {
	mu          sync.Mutex
	proxy       nodeProxyConfig
	remote      *net.UDPAddr
	timeout     time.Duration
	association *socks5UDPAssociation
}

var _ ikev2.InitTransport = (*nodeProxyIKETransport)(nil)

func (t *nodeProxyIKETransport) ExchangeIKE(ctx context.Context, request []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	association, err := t.getConn(ctx)
	if err != nil {
		return nil, err
	}
	remote, useNonESPMarker := ikeExchangeTarget(t.remote, request)
	wire := request
	if useNonESPMarker {
		wire = append([]byte{0, 0, 0, 0}, request...)
	}
	if err := association.sendTo(ctx, wire, remote, t.timeout); err != nil {
		return nil, err
	}
	for {
		response, source, err := association.receiveFrom(ctx, t.timeout)
		if err != nil {
			return nil, err
		}
		if !sameUDPAddress(source, remote) {
			continue
		}
		if len(response) >= 4 && response[0] == 0 && response[1] == 0 && response[2] == 0 && response[3] == 0 {
			response = response[4:]
		}
		return response, nil
	}
}

func ikeExchangeTarget(configured *net.UDPAddr, request []byte) (*net.UDPAddr, bool) {
	remote := cloneUDPAddr(configured)
	if remote == nil {
		return nil, false
	}
	// EE's ePDG follows the conventional IKEv2 NAT-T transition: the first
	// IKE_SA_INIT is sent to UDP/500 without a marker; IKE_AUTH and later
	// control exchanges use UDP/4500 with the non-ESP marker.
	if len(request) >= 28 && request[18] == byte(ikev2.ExchangeIKE_SA_INIT) && binary.BigEndian.Uint32(request[20:24]) == 0 {
		remote.Port = 500
		return remote, false
	}
	remote.Port = 4500
	return remote, true
}

func (t *nodeProxyIKETransport) getConn(ctx context.Context) (*socks5UDPAssociation, error) {
	if t.association != nil {
		return t.association, nil
	}
	association, err := dialNodeProxyUDP(ctx, t.proxy)
	if err != nil {
		return nil, err
	}
	t.association = association
	return association, nil
}

type nodeProxyESPTransport struct {
	mu          sync.Mutex
	proxy       nodeProxyConfig
	remote      *net.UDPAddr
	timeout     time.Duration
	association *socks5UDPAssociation
	closed      bool
}

var _ swu.ESPPacketReadWriteTransport = (*nodeProxyESPTransport)(nil)
var _ swu.ESPPacketTransportCloser = (*nodeProxyESPTransport)(nil)

func (t *nodeProxyESPTransport) SendESPPacket(ctx context.Context, packet []byte) error {
	if len(packet) < 8 || (len(packet) >= 4 && binary.BigEndian.Uint32(packet[:4]) == 0) {
		return errors.New("invalid ESP packet")
	}
	return t.write(ctx, packet)
}

func (t *nodeProxyESPTransport) SendNATTKeepalive(ctx context.Context) error {
	return t.write(ctx, []byte{0xff})
}

func (t *nodeProxyESPTransport) ReadESPPacket(ctx context.Context) ([]byte, error) {
	for {
		association, err := t.getConn(ctx)
		if err != nil {
			return nil, err
		}
		packet, source, err := association.receiveFrom(ctx, t.timeout)
		if err != nil {
			return nil, err
		}
		if !sameUDPAddress(source, t.remote) || (len(packet) == 1 && packet[0] == 0xff) {
			continue
		}
		if len(packet) >= 4 && binary.BigEndian.Uint32(packet[:4]) == 0 {
			continue
		}
		if len(packet) < 8 {
			return nil, errors.New("received ESP packet is too short")
		}
		return packet, nil
	}
}

func (t *nodeProxyESPTransport) Close(context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	association := t.association
	t.association = nil
	t.mu.Unlock()
	return association.Close()
}

func (t *nodeProxyESPTransport) LocalNetworkAddr() net.Addr {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.association == nil {
		return nil
	}
	return t.association.localAddr()
}

func (t *nodeProxyESPTransport) write(ctx context.Context, packet []byte) error {
	association, err := t.getConn(ctx)
	if err != nil {
		return err
	}
	return association.sendTo(ctx, packet, t.remote, t.timeout)
}

func (t *nodeProxyESPTransport) getConn(ctx context.Context) (*socks5UDPAssociation, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, net.ErrClosed
	}
	if t.association != nil {
		return t.association, nil
	}
	association, err := dialNodeProxyUDP(ctx, t.proxy)
	if err != nil {
		return nil, err
	}
	t.association = association
	return association, nil
}

func sameUDPAddress(left, right *net.UDPAddr) bool {
	if left == nil || right == nil {
		return false
	}
	return left.Port == right.Port && left.IP.Equal(right.IP)
}

func nodeProxyTunnelManager(simProvider sim.AKAProvider, proxy nodeProxyConfig, localIP, eapIdentity string, pcscf []string, report *reporter) (*observedTunnelManager, error) {
	if simProvider == nil {
		return nil, errors.New("SIM AKA provider is nil")
	}
	ikeConfig := swu.IKEPacketTunnelManagerConfig{
		SIM:         simProvider,
		EAPIdentity: strings.TrimSpace(eapIdentity),
		SA:          carrierCompatibleIKEProposal(),
		ChildSA:     carrierCompatibleESPProposal(),
	}
	if parsedLocalIP := net.ParseIP(strings.TrimSpace(localIP)); parsedLocalIP != nil {
		ikeConfig.LocalIP = parsedLocalIP
		ikeConfig.LocalPort = 500
	}
	if strings.TrimSpace(proxy.Address) != "" {
		ikeConfig.RemotePort = 500
		ikeConfig.IKETransportFactory = func(_ swu.TunnelConfig, cfg swu.IKETransportConfig) (ikev2.InitTransport, error) {
			remote, err := net.ResolveUDPAddr("udp", cfg.RemoteAddr)
			if err != nil {
				return nil, err
			}
			return &nodeProxyIKETransport{
				proxy: proxy, remote: remote, timeout: cfg.Timeout,
			}, nil
		}
		ikeConfig.ESPTransportFactory = func(_ swu.TunnelConfig, cfg swu.ESPTransportConfig) (swu.ESPPacketTransport, error) {
			remote, err := net.ResolveUDPAddr("udp", cfg.RemoteAddr)
			if err != nil {
				return nil, err
			}
			remote.Port = 4500
			return &nodeProxyESPTransport{proxy: proxy, remote: remote, timeout: cfg.Timeout}, nil
		}
	}
	routes := make([]swu.TUNRoute, 0, len(pcscf))
	for _, candidate := range pcscf {
		host := pcscfHost(candidate)
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		prefix := "/128"
		if ip.To4() != nil {
			prefix = "/32"
		}
		routes = append(routes, swu.TUNRoute{Destination: ip.String() + prefix})
	}
	manager := swu.NewTUNIKETunnelManager(ikeConfig, swu.TUNTunnelManagerConfig{
		TUN:               swu.TUNDeviceConfig{Name: "simvowifi0"},
		MTU:               1400,
		DefaultRoutes:     len(routes) == 0,
		ProtectEPDGRoutes: len(routes) == 0,
		Routes:            routes,
	})
	return &observedTunnelManager{base: manager, report: report, pcscf: append([]string(nil), pcscf...)}, nil
}

func carrierCompatibleIKEProposal() ikev2.SecurityAssociation {
	return ikev2.SecurityAssociation{Proposals: []ikev2.Proposal{{
		Number:     1,
		ProtocolID: ikev2.ProtocolIKE,
		Transforms: []ikev2.Transform{
			{Type: ikev2.TransformENCR, ID: ikev2.ENCR_AES_CBC, Attributes: []ikev2.TransformAttribute{ikev2.KeyLengthAttribute(128)}},
			{Type: ikev2.TransformPRF, ID: ikev2.PRF_HMAC_SHA1},
			{Type: ikev2.TransformINTEG, ID: ikev2.INTEG_HMAC_SHA1_96},
			{Type: ikev2.TransformDHRGroup, ID: ikev2.DHGroup2048BitMODP},
		},
	}}}
}

func carrierCompatibleESPProposal() ikev2.SecurityAssociation {
	return ikev2.SecurityAssociation{Proposals: []ikev2.Proposal{{
		Number:     1,
		ProtocolID: ikev2.ProtocolESP,
		Transforms: []ikev2.Transform{
			{Type: ikev2.TransformENCR, ID: ikev2.ENCR_AES_CBC, Attributes: []ikev2.TransformAttribute{ikev2.KeyLengthAttribute(128)}},
			{Type: ikev2.TransformINTEG, ID: ikev2.INTEG_HMAC_SHA1_96},
			{Type: ikev2.TransformESN, ID: ikev2.ESNNo},
		},
	}}}
}

type observedTunnelManager struct {
	mu      sync.Mutex
	base    swu.TunnelManager
	report  *reporter
	pcscf   []string
	session swu.TunnelSession
}

func (m *observedTunnelManager) EstablishTunnel(ctx context.Context, cfg swu.TunnelConfig) (swu.TunnelSession, error) {
	if m.report != nil {
		m.report.update("authenticating", func(status *tunnelStatus) {
			status.IMSState = "not_started"
		})
	}
	session, err := m.base.EstablishTunnel(ctx, cfg)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.session = session
	m.mu.Unlock()
	if m.report != nil {
		result := session.Result()
		m.report.recordTunnel(result, m.pcscf)
	}
	return session, nil
}

func (m *observedTunnelManager) closeCaptured(ctx context.Context) error {
	m.mu.Lock()
	session := m.session
	m.session = nil
	m.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close(ctx)
}
