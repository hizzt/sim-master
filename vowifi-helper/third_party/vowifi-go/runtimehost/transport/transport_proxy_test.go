package transport

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestSocks5UDPTransportRoundTrip(t *testing.T) {
	udpRelay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udpRelay.Close()

	tcpListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()

	serverDone := make(chan error, 1)
	go func() {
		control, err := tcpListener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer control.Close()
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(control, greeting); err != nil {
			serverDone <- err
			return
		}
		if _, err := control.Write([]byte{5}); err != nil {
			serverDone <- err
			return
		}
		if _, err := control.Write([]byte{0}); err != nil {
			serverDone <- err
			return
		}
		associate := make([]byte, 10)
		if _, err := io.ReadFull(control, associate); err != nil {
			serverDone <- err
			return
		}
		port := udpRelay.LocalAddr().(*net.UDPAddr).Port
		response := []byte{5, 0, 0, 1, 0, 0, 0, 0, byte(port >> 8), byte(port)}
		for _, octet := range response {
			if _, err := control.Write([]byte{octet}); err != nil {
				serverDone <- err
				return
			}
		}

		packet := make([]byte, 2048)
		n, client, err := udpRelay.ReadFromUDP(packet)
		if err != nil {
			serverDone <- err
			return
		}
		_, err = udpRelay.WriteToUDP(packet[:n], client)
		serverDone <- err
	}()

	transport, err := NewSocks5UDPTransport(tcpListener.Addr().String(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	target := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 9), Port: 4500}
	payload := []byte("ike-over-socks5")
	if _, err := transport.SendTo(payload, target); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	n, source, err := transport.RecvFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("payload = %q", buffer[:n])
	}
	if !source.IP.Equal(target.IP) || source.Port != target.Port {
		t.Fatalf("source = %s, want %s", source, target)
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SOCKS5 UDP relay did not finish")
	}
}

func TestSocks5UDPTransportRejectsUnreachableProxyImmediately(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSocks5UDPTransport(address, "", ""); err == nil {
		t.Fatal("expected the SOCKS5 connection to fail during transport creation")
	}
}
