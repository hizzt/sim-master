package voiceclient

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/1239t/vowifi-go/engine/sim"
)

type ownershipTestDialer struct {
	closed int
}

type ownershipTestAKA struct{}

func (ownershipTestAKA) CalculateAKA([]byte, []byte) (sim.AKAResult, error) {
	return sim.AKAResult{}, nil
}

type ownershipTestDataplane struct {
	rx chan []byte
}

func (d *ownershipTestDataplane) SendInnerPacket([]byte) error { return nil }
func (d *ownershipTestDataplane) InnerPackets() <-chan []byte  { return d.rx }

func (d *ownershipTestDialer) DialContextTCP(context.Context, net.IP, int, net.IP, int) (net.Conn, error) {
	return nil, nil
}

func (d *ownershipTestDialer) ListenContextTCP(context.Context, net.IP, int) (net.Listener, error) {
	return nil, nil
}

func (d *ownershipTestDialer) Close() error {
	d.closed++
	return nil
}

func TestShutdownDoesNotCloseSharedSWUDialer(t *testing.T) {
	dialer := &ownershipTestDialer{}
	client := &Client{swuTCP: dialer}
	if err := client.shutdownSIPStack(); err != nil {
		t.Fatalf("shutdownSIPStack: %v", err)
	}
	if dialer.closed != 0 {
		t.Fatalf("shared dialer closed %d times", dialer.closed)
	}
}

func TestShutdownClosesOwnedSWUDialer(t *testing.T) {
	dialer := &ownershipTestDialer{}
	client := &Client{swuTCP: dialer, closeSWU: true}
	if err := client.shutdownSIPStack(); err != nil {
		t.Fatalf("shutdownSIPStack: %v", err)
	}
	if dialer.closed != 1 {
		t.Fatalf("owned dialer closed %d times", dialer.closed)
	}
}

func TestSkipRegisterClientClosesWithoutReregisterWorker(t *testing.T) {
	dialer := &ownershipTestDialer{}
	client, err := Dial(context.Background(), Config{
		LocalIP:      net.ParseIP("2001:db8::1"),
		LocalPort:    12000,
		Dataplane:    &ownershipTestDataplane{rx: make(chan []byte)},
		SWUDialer:    dialer,
		PCSCFAddr:    "[2001:db8::2]:5060",
		Transport:    "tcp",
		PrivateID:    "user@example.invalid",
		PublicURI:    "sip:user@example.invalid",
		HomeDomain:   "example.invalid",
		AKA:          ownershipTestAKA{},
		SkipRegister: true,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- client.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked waiting for a missing re-register worker")
	}
	if dialer.closed != 0 {
		t.Fatalf("shared dialer closed %d times", dialer.closed)
	}
}
