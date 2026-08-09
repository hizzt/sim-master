package imscore

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

type retainedFallbackDialer struct {
	conn           net.Conn
	udpConn        net.Conn
	packetConn     net.PacketConn
	dialCount      int
	udpDialCount   int
	udpListenCount int
	closeCount     int
}

func (d *retainedFallbackDialer) DialContextTCP(context.Context, net.IP, int, net.IP, int) (net.Conn, error) {
	d.dialCount++
	if d.conn == nil {
		return nil, errors.New("fallback connection unavailable")
	}
	return d.conn, nil
}

func (d *retainedFallbackDialer) ListenContextTCP(context.Context, net.IP, int) (net.Listener, error) {
	return nil, errors.New("not implemented")
}

func (d *retainedFallbackDialer) DialContextUDP(context.Context, net.IP, int, net.IP, int) (net.Conn, error) {
	d.udpDialCount++
	if d.udpConn == nil {
		return nil, errors.New("fallback UDP connection unavailable")
	}
	return d.udpConn, nil
}

func (d *retainedFallbackDialer) ListenPacketUDP(context.Context, net.IP, int) (net.PacketConn, error) {
	d.udpListenCount++
	if d.packetConn == nil {
		return nil, errors.New("fallback UDP listener unavailable")
	}
	return d.packetConn, nil
}

func (d *retainedFallbackDialer) Close() error {
	d.closeCount++
	return nil
}

func TestRetainedConnDialerClaimsRegisterConnectionOnce(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	dialer := newRetainedConnDialer(clientConn)

	claimed, err := dialer.DialContextTCP(context.Background(), nil, 0, nil, 0)
	if err != nil {
		t.Fatalf("first DialContextTCP: %v", err)
	}
	if claimed != clientConn {
		t.Fatal("dialer returned a different connection")
	}
	if _, err := dialer.DialContextTCP(context.Background(), nil, 0, nil, 0); err == nil {
		t.Fatal("second DialContextTCP unexpectedly succeeded")
	}
	_ = claimed.Close()
}

func TestRetainedConnDialerFallsBackAfterRegisterConnection(t *testing.T) {
	registerClient, registerServer := net.Pipe()
	defer registerServer.Close()
	fallbackClient, fallbackServer := net.Pipe()
	defer fallbackServer.Close()
	fallback := &retainedFallbackDialer{conn: fallbackClient}
	dialer := newRetainedConnDialer(registerClient, fallback)

	claimed, err := dialer.DialContextTCP(context.Background(), nil, 0, nil, 0)
	if err != nil {
		t.Fatalf("retained DialContextTCP: %v", err)
	}
	if claimed != registerClient {
		t.Fatal("first dial did not return retained REGISTER connection")
	}

	fresh, err := dialer.DialContextTCP(context.Background(), nil, 0, nil, 0)
	if err != nil {
		t.Fatalf("fallback DialContextTCP: %v", err)
	}
	if fresh != fallbackClient {
		t.Fatal("second dial did not return fallback connection")
	}
	if fallback.dialCount != 1 {
		t.Fatalf("fallback dial count = %d, want 1", fallback.dialCount)
	}

	_ = claimed.Close()
	_ = fresh.Close()
}

func TestRetainedConnDialerCloseDoesNotOwnSharedFallback(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	fallback := &retainedFallbackDialer{}
	dialer := newRetainedConnDialer(clientConn, fallback)

	if err := dialer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fallback.closeCount != 0 {
		t.Fatalf("shared fallback close count = %d, want 0", fallback.closeCount)
	}
	if _, err := dialer.DialContextTCP(context.Background(), nil, 0, nil, 0); err == nil {
		t.Fatal("DialContextTCP unexpectedly succeeded after Close")
	}
}

func TestRetainedConnDialerDelegatesUDPToSharedFallback(t *testing.T) {
	registerClient, registerServer := net.Pipe()
	defer registerClient.Close()
	defer registerServer.Close()
	udpClient, udpServer := net.Pipe()
	defer udpServer.Close()
	packetConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()
	fallback := &retainedFallbackDialer{udpConn: udpClient, packetConn: packetConn}
	dialer := newRetainedConnDialer(registerClient, fallback)
	udp, ok := dialer.(voiceclient.SWUUDPDialer)
	if !ok {
		t.Fatal("retained dialer does not expose shared SWu UDP")
	}

	connected, err := udp.DialContextUDP(context.Background(), net.ParseIP("192.0.2.1"), 40000, net.ParseIP("192.0.2.2"), 40002)
	if err != nil {
		t.Fatal(err)
	}
	if connected != udpClient || fallback.udpDialCount != 1 {
		t.Fatal("connected UDP was not delegated to the shared fallback")
	}
	listener, err := udp.ListenPacketUDP(context.Background(), net.ParseIP("192.0.2.1"), 40004)
	if err != nil {
		t.Fatal(err)
	}
	if listener != packetConn || fallback.udpListenCount != 1 {
		t.Fatal("UDP listener was not delegated to the shared fallback")
	}
	_ = connected.Close()
}
