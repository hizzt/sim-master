package imscore

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

type imsNetworkUDPTestDialer struct {
	localIP   net.IP
	localPort int
}

func (d *imsNetworkUDPTestDialer) DialContextTCP(context.Context, net.IP, int, net.IP, int) (net.Conn, error) {
	return nil, nil
}

func (d *imsNetworkUDPTestDialer) ListenContextTCP(context.Context, net.IP, int) (net.Listener, error) {
	return nil, nil
}

func (d *imsNetworkUDPTestDialer) Close() error { return nil }

func (d *imsNetworkUDPTestDialer) DialContextUDP(context.Context, net.IP, int, net.IP, int) (net.Conn, error) {
	return nil, nil
}

func (d *imsNetworkUDPTestDialer) ListenPacketUDP(_ context.Context, localIP net.IP, localPort int) (net.PacketConn, error) {
	d.localIP = append(net.IP(nil), localIP...)
	d.localPort = localPort
	return &imsNetworkUDPTestPacketConn{addr: &net.UDPAddr{IP: localIP, Port: localPort}}, nil
}

type imsNetworkUDPTestPacketConn struct{ addr net.Addr }

func (c *imsNetworkUDPTestPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, nil, net.ErrClosed
}
func (c *imsNetworkUDPTestPacketConn) WriteTo([]byte, net.Addr) (int, error) { return 0, net.ErrClosed }
func (c *imsNetworkUDPTestPacketConn) Close() error                          { return nil }
func (c *imsNetworkUDPTestPacketConn) LocalAddr() net.Addr                   { return c.addr }
func (c *imsNetworkUDPTestPacketConn) SetDeadline(time.Time) error           { return nil }
func (c *imsNetworkUDPTestPacketConn) SetReadDeadline(time.Time) error       { return nil }
func (c *imsNetworkUDPTestPacketConn) SetWriteDeadline(time.Time) error      { return nil }

var _ voiceclient.SWUTCPDialer = (*imsNetworkUDPTestDialer)(nil)
var _ voiceclient.SWUUDPDialer = (*imsNetworkUDPTestDialer)(nil)

func TestUserspaceIMSNetworkListenPacketUsesSharedSWUNetstack(t *testing.T) {
	dialer := &imsNetworkUDPTestDialer{}
	localIP := net.ParseIP("2001:db8::10")
	network := &UserspaceIMSNetwork{localIP: localIP, swu: dialer}
	conn, err := network.ListenPacket(context.Background(), "udp6", &net.UDPAddr{IP: net.IPv6unspecified, Port: 49170})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !dialer.localIP.Equal(localIP) || dialer.localPort != 49170 {
		t.Fatalf("ListenPacketUDP called with %s:%d", dialer.localIP, dialer.localPort)
	}
}

func TestUserspaceIMSNetworkListenPacketValidatesAddressFamily(t *testing.T) {
	dialer := &imsNetworkUDPTestDialer{}
	localIP := net.ParseIP("2001:db8::10")
	network := &UserspaceIMSNetwork{localIP: localIP, swu: dialer}
	if _, err := network.ListenPacket(context.Background(), "udp4", &net.UDPAddr{Port: 40000}); err == nil {
		t.Fatal("udp4 unexpectedly accepted for IPv6 SWu address")
	}
	if _, err := network.ListenPacket(context.Background(), "tcp", &net.UDPAddr{Port: 40000}); err == nil {
		t.Fatal("non-UDP packet network unexpectedly accepted")
	}
	if _, err := network.ListenPacket(context.Background(), "udp6", &net.UDPAddr{IP: net.ParseIP("2001:db8::11"), Port: 40000}); err == nil {
		t.Fatal("non-SWu local IP unexpectedly accepted")
	}
}
