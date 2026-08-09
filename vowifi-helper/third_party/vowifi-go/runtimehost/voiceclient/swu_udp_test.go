package voiceclient

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type linkedPacketDataplane struct {
	rx   chan []byte
	mu   sync.RWMutex
	peer *linkedPacketDataplane
}

func newLinkedPacketDataplanes() (*linkedPacketDataplane, *linkedPacketDataplane) {
	a := &linkedPacketDataplane{rx: make(chan []byte, 256)}
	b := &linkedPacketDataplane{rx: make(chan []byte, 256)}
	a.peer = b
	b.peer = a
	return a, b
}

func (d *linkedPacketDataplane) SendInnerPacket(packet []byte) error {
	d.mu.RLock()
	peer := d.peer
	d.mu.RUnlock()
	peer.rx <- append([]byte(nil), packet...)
	return nil
}

func (d *linkedPacketDataplane) InnerPackets() <-chan []byte { return d.rx }

func TestSWUNetstackSharesTCPAndUDPOnOneDataplane(t *testing.T) {
	dpA, dpB := newLinkedPacketDataplanes()
	ipA := net.ParseIP("192.0.2.10")
	ipB := net.ParseIP("192.0.2.20")
	stackA, err := NewSWUTCPDialer(ipA, dpA)
	if err != nil {
		t.Fatal(err)
	}
	defer stackA.Close()
	stackB, err := NewSWUTCPDialer(ipB, dpB)
	if err != nil {
		t.Fatal(err)
	}
	defer stackB.Close()

	udpA, ok := stackA.(SWUUDPDialer)
	if !ok {
		t.Fatal("SWu netstack does not implement SWUUDPDialer")
	}
	udpB, ok := stackB.(SWUUDPDialer)
	if !ok {
		t.Fatal("SWu netstack does not implement SWUUDPDialer")
	}

	udpListener, err := udpB.ListenPacketUDP(context.Background(), ipB, 41000)
	if err != nil {
		t.Fatal(err)
	}
	defer udpListener.Close()
	udpConn, err := udpA.DialContextUDP(context.Background(), ipA, 41001, ipB, 41000)
	if err != nil {
		t.Fatal(err)
	}
	defer udpConn.Close()
	_ = udpListener.SetReadDeadline(time.Now().Add(2 * time.Second))
	_ = udpConn.SetReadDeadline(time.Now().Add(2 * time.Second))

	rtp := []byte{0x80, 0x00, 0x12, 0x34, 0xde, 0xad, 0xbe, 0xef}
	if _, err := udpConn.Write(rtp); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, from, err := udpListener.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], rtp) {
		t.Fatalf("UDP payload = %x, want %x", buf[:n], rtp)
	}
	if got := from.(*net.UDPAddr).Port; got != 41001 {
		t.Fatalf("UDP source port = %d, want 41001", got)
	}
	if _, err := udpListener.WriteTo([]byte("rtcp"), from); err != nil {
		t.Fatal(err)
	}
	n, err = udpConn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "rtcp" {
		t.Fatalf("UDP reply = %q, want rtcp", got)
	}

	tcpListener, err := stackB.ListenContextTCP(context.Background(), ipB, 42000)
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := tcpListener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	tcpCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tcpConn, err := stackA.DialContextTCP(tcpCtx, ipA, 42001, ipB, 42000)
	if err != nil {
		t.Fatal(err)
	}
	defer tcpConn.Close()
	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
		defer serverConn.Close()
	case err := <-acceptErr:
		t.Fatal(err)
	case <-tcpCtx.Done():
		t.Fatal(tcpCtx.Err())
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := tcpConn.Write([]byte("sip")); err != nil {
		t.Fatal(err)
	}
	n, err = serverConn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "sip" {
		t.Fatalf("TCP payload = %q, want sip", got)
	}
}

func TestSWUNetstackUDPRejectsWrongLocalIPAndCanceledContext(t *testing.T) {
	dp, _ := newLinkedPacketDataplanes()
	stack, err := NewSWUTCPDialer(net.ParseIP("192.0.2.30"), dp)
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	udp := stack.(SWUUDPDialer)
	listener, err := udp.ListenPacketUDP(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if addr, ok := listener.LocalAddr().(*net.UDPAddr); !ok || addr.Port == 0 {
		t.Fatalf("ephemeral UDP address = %v, want non-zero port", listener.LocalAddr())
	}
	_ = listener.Close()
	if _, err := udp.ListenPacketUDP(context.Background(), net.ParseIP("192.0.2.31"), 40000); err == nil {
		t.Fatal("wrong local IP unexpectedly accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := udp.ListenPacketUDP(ctx, nil, 0); err == nil {
		t.Fatal("canceled context unexpectedly accepted")
	}
}

func TestSWUNetstackIPv6UDP(t *testing.T) {
	dpA, dpB := newLinkedPacketDataplanes()
	ipA := net.ParseIP("2001:db8::10")
	ipB := net.ParseIP("2001:db8::20")
	stackA, err := NewSWUTCPDialer(ipA, dpA)
	if err != nil {
		t.Fatal(err)
	}
	defer stackA.Close()
	stackB, err := NewSWUTCPDialer(ipB, dpB)
	if err != nil {
		t.Fatal(err)
	}
	defer stackB.Close()

	listener, err := stackB.(SWUUDPDialer).ListenPacketUDP(context.Background(), ipB, 43000)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := stackA.(SWUUDPDialer).DialContextUDP(context.Background(), ipA, 43001, ipB, 43000)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = listener.SetReadDeadline(time.Now().Add(2 * time.Second))

	want := []byte("ipv6-rtp")
	if _, err := conn.Write(want); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, from, err := listener.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("IPv6 UDP payload = %x, want %x", buf[:n], want)
	}
	if got := from.(*net.UDPAddr); !got.IP.Equal(ipA) || got.Port != 43001 {
		t.Fatalf("IPv6 UDP source = %s, want [%s]:43001", got, ipA)
	}
}
