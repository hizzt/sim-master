package runtimehost

import (
	"errors"
	"testing"
)

type statsTestDataplane struct {
	rx      chan []byte
	sendErr error
}

func (d *statsTestDataplane) SendInnerPacket([]byte) error { return d.sendErr }
func (d *statsTestDataplane) InnerPackets() <-chan []byte  { return d.rx }

func TestPacketCountedDataplaneCountsSuccessfulTraffic(t *testing.T) {
	inner := &statsTestDataplane{rx: make(chan []byte)}
	dp := newPacketCountedDataplane(inner)

	if err := dp.SendInnerPacket([]byte{0x60}); err != nil {
		t.Fatalf("SendInnerPacket: %v", err)
	}

	rx := dp.InnerPackets()
	if again := dp.InnerPackets(); rx != again {
		t.Fatal("InnerPackets returned more than one proxy channel")
	}
	go func() { inner.rx <- []byte{0x60} }()
	if packet := <-rx; len(packet) != 1 || packet[0] != 0x60 {
		t.Fatalf("received packet = %x", packet)
	}

	txCount, rxCount := dp.InnerPacketCounts()
	if txCount != 1 || rxCount != 1 {
		t.Fatalf("counts = tx:%d rx:%d, want tx:1 rx:1", txCount, rxCount)
	}

	inner.sendErr = errors.New("send failed")
	if err := dp.SendInnerPacket([]byte{0x60}); err == nil {
		t.Fatal("SendInnerPacket unexpectedly succeeded")
	}
	txCount, _ = dp.InnerPacketCounts()
	if txCount != 1 {
		t.Fatalf("failed send incremented tx count to %d", txCount)
	}
}
