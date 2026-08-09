package runtimehost

import (
	"sync"
	"sync/atomic"
)

// packetCountedDataplane is the single runtimehost-owned view of the SWu
// inner-packet boundary.  swu-go's Session exposes the packet methods but its
// snapshot intentionally does not expose the netstack counters, so keep the
// counters at the boundary where packets enter and leave the application.
//
// InnerPackets is fan-in from the SWu session and must have one consumer.  The
// once-guard makes that ownership explicit and prevents accidentally creating
// a second reader that would steal TCP responses from the IMS netstack.
type packetCountedDataplane struct {
	inner swuInnerDataplane

	tx atomic.Uint64
	rx atomic.Uint64

	rxOnce sync.Once
	rxCh   <-chan []byte
}

func newPacketCountedDataplane(inner swuInnerDataplane) *packetCountedDataplane {
	if inner == nil {
		return nil
	}
	return &packetCountedDataplane{inner: inner}
}

func (d *packetCountedDataplane) SendInnerPacket(packet []byte) error {
	if d == nil || d.inner == nil {
		return nil
	}
	if err := d.inner.SendInnerPacket(packet); err != nil {
		return err
	}
	d.tx.Add(1)
	return nil
}

func (d *packetCountedDataplane) InnerPackets() <-chan []byte {
	if d == nil || d.inner == nil {
		return nil
	}
	d.rxOnce.Do(func() {
		out := make(chan []byte)
		d.rxCh = out
		in := d.inner.InnerPackets()
		go func() {
			defer close(out)
			for packet := range in {
				if len(packet) > 0 {
					d.rx.Add(1)
				}
				out <- packet
			}
		}()
	})
	return d.rxCh
}

// InnerPacketCounts returns successful inner packets sent to and received
// from the SWu session.  It is deliberately a snapshot so callers can poll it
// without taking the runtimehost state mutex.
func (d *packetCountedDataplane) InnerPacketCounts() (tx, rx uint64) {
	if d == nil {
		return 0, 0
	}
	return d.tx.Load(), d.rx.Load()
}
