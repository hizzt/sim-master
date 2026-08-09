package imscore

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

// retainedConnDialer hands the successful plain REGISTER TCP connection to
// voiceclient exactly once. Some IMS networks bind subsequent MESSAGE
// requests to that registered flow and reject a new TCP source port.
type retainedConnDialer struct {
	mu       sync.Mutex
	conn     net.Conn
	fallback voiceclient.SWUTCPDialer
	closed   bool
}

func newRetainedConnDialer(conn net.Conn, fallback ...voiceclient.SWUTCPDialer) voiceclient.SWUTCPDialer {
	var next voiceclient.SWUTCPDialer
	if len(fallback) > 0 {
		next = fallback[0]
	}
	return &retainedConnDialer{conn: conn, fallback: next}
}

func (d *retainedConnDialer) DialContextTCP(ctx context.Context, localIP net.IP, localPort int, remoteIP net.IP, remotePort int) (net.Conn, error) {
	if d == nil {
		return nil, fmt.Errorf("imscore: retained REGISTER connection unavailable")
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, fmt.Errorf("imscore: retained REGISTER connection dialer is closed")
	}
	if d.conn != nil {
		conn := d.conn
		d.conn = nil
		d.mu.Unlock()
		return conn, nil
	}
	fallback := d.fallback
	d.mu.Unlock()
	if fallback == nil {
		return nil, fmt.Errorf("imscore: retained REGISTER connection already claimed")
	}
	return fallback.DialContextTCP(ctx, localIP, localPort, remoteIP, remotePort)
}

func (d *retainedConnDialer) ListenContextTCP(ctx context.Context, localIP net.IP, localPort int) (net.Listener, error) {
	if d == nil {
		return nil, fmt.Errorf("imscore: retained REGISTER connection unavailable")
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, fmt.Errorf("imscore: retained REGISTER connection dialer is closed")
	}
	fallback := d.fallback
	d.mu.Unlock()
	if fallback == nil {
		return nil, fmt.Errorf("imscore: retained REGISTER connection does not support listening")
	}
	return fallback.ListenContextTCP(ctx, localIP, localPort)
}

func (d *retainedConnDialer) DialContextUDP(ctx context.Context, localIP net.IP, localPort int, remoteIP net.IP, remotePort int) (net.Conn, error) {
	fallback, err := d.udpFallback()
	if err != nil {
		return nil, err
	}
	return fallback.DialContextUDP(ctx, localIP, localPort, remoteIP, remotePort)
}

func (d *retainedConnDialer) ListenPacketUDP(ctx context.Context, localIP net.IP, localPort int) (net.PacketConn, error) {
	fallback, err := d.udpFallback()
	if err != nil {
		return nil, err
	}
	return fallback.ListenPacketUDP(ctx, localIP, localPort)
}

func (d *retainedConnDialer) udpFallback() (voiceclient.SWUUDPDialer, error) {
	if d == nil {
		return nil, fmt.Errorf("imscore: retained REGISTER connection unavailable")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, fmt.Errorf("imscore: retained REGISTER connection dialer is closed")
	}
	fallback, ok := d.fallback.(voiceclient.SWUUDPDialer)
	if !ok || fallback == nil {
		return nil, fmt.Errorf("imscore: shared SWu netstack does not support UDP")
	}
	return fallback, nil
}

func (d *retainedConnDialer) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.conn == nil {
		return nil
	}
	err := d.conn.Close()
	d.conn = nil
	return err
}
