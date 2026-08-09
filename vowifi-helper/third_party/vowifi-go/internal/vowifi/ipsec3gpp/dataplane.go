package ipsec3gpp

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/1239t/swu-go/pkg/logger"
)

// InnerPacketDataplane carries complete inner IP packets through the SWu
// Child SA. IMS ESP transport mode has to be applied at this layer, before
// packets enter the outer SWu ESP tunnel.
type InnerPacketDataplane interface {
	SendInnerPacket([]byte) error
	InnerPackets() <-chan []byte
}

// SecuredDataplane dynamically applies the negotiated 3GPP IMS ESP policy to
// packets exchanged between the TCP netstack and the SWu dataplane. Before a
// policy is installed it is a transparent pass-through, which permits the
// initial unprotected REGISTER/401 security agreement exchange.
type SecuredDataplane struct {
	inner InnerPacketDataplane
	rx    chan []byte
	done  chan struct{}

	mu        sync.RWMutex
	transport *Transport
	closed    bool
	closeOnce sync.Once

	inboundAccepted atomic.Uint64
	inboundRejected atomic.Uint64
	outboundTraced  atomic.Uint64
}

// NewSecuredDataplane wraps an established SWu inner-packet dataplane.
func NewSecuredDataplane(inner InnerPacketDataplane) (*SecuredDataplane, error) {
	if inner == nil {
		return nil, errors.New("ipsec3gpp: inner dataplane is required")
	}
	d := &SecuredDataplane{
		inner: inner,
		rx:    make(chan []byte, 512),
		done:  make(chan struct{}),
	}
	go d.inboundLoop()
	return d, nil
}

// Install atomically enables a negotiated IMS ESP transport policy.
func (d *SecuredDataplane) Install(transport *Transport) error {
	if d == nil || transport == nil {
		return errors.New("ipsec3gpp: transport is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("ipsec3gpp: secured dataplane is closed")
	}
	d.transport = transport
	return nil
}

// Clear disables IMS ESP transforms while keeping the underlying SWu tunnel.
func (d *SecuredDataplane) Clear() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.transport = nil
	d.mu.Unlock()
}

// Close stops the forwarding loop. Ownership of the SWu dataplane remains
// with the SWu session and is deliberately not closed here.
func (d *SecuredDataplane) Close() {
	if d == nil {
		return
	}
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.transport = nil
		d.mu.Unlock()
		close(d.done)
	})
}

func (d *SecuredDataplane) SendInnerPacket(packet []byte) error {
	if d == nil || d.inner == nil {
		return errors.New("ipsec3gpp: secured dataplane is not ready")
	}
	d.mu.RLock()
	transport := d.transport
	closed := d.closed
	d.mu.RUnlock()
	if closed {
		return errors.New("ipsec3gpp: secured dataplane is closed")
	}
	out := append([]byte(nil), packet...)
	if transport != nil {
		var err error
		out, err = transport.TransformOutbound(out)
		if err != nil {
			return err
		}
		if parsed, parseErr := parseIPPacket(packet); parseErr == nil &&
			(parsed.srcPort == transport.policy.LocalPortS || d.outboundTraced.Load() < 16) {
			count := d.outboundTraced.Add(1)
			if count <= 128 {
				logger.Info("IMS ipsec outbound packet",
					logger.Int("packet_count", int(count)),
					logger.Int("src_port", parsed.srcPort),
					logger.Int("dst_port", parsed.dstPort),
					logger.Int("plain_bytes", len(packet)),
					logger.Bool("protected", len(out) != len(packet)))
			}
		}
	}
	return d.inner.SendInnerPacket(out)
}

func (d *SecuredDataplane) InnerPackets() <-chan []byte {
	if d == nil {
		return nil
	}
	return d.rx
}

func (d *SecuredDataplane) inboundLoop() {
	source := d.inner.InnerPackets()
	defer close(d.rx)
	for {
		select {
		case <-d.done:
			return
		case packet, ok := <-source:
			if !ok {
				return
			}
			if len(packet) == 0 {
				continue
			}
			d.mu.RLock()
			transport := d.transport
			d.mu.RUnlock()
			out := append([]byte(nil), packet...)
			if transport != nil {
				var err error
				out, err = transport.TransformInbound(out)
				if err != nil {
					// Unknown or invalid ESP must not be injected as plain traffic.
					count := d.inboundRejected.Add(1)
					if count <= 32 || count%100 == 0 {
						logger.Warn("IMS ipsec inbound packet rejected",
							logger.Int("reject_count", int(count)),
							logger.Int("wire_bytes", len(packet)),
							logger.String("error", err.Error()))
					}
					continue
				}
				if parsed, parseErr := parseIPPacket(out); parseErr == nil {
					count := d.inboundAccepted.Add(1)
					if count <= 128 {
						logger.Info("IMS ipsec inbound packet",
							logger.Int("packet_count", int(count)),
							logger.Int("src_port", parsed.srcPort),
							logger.Int("dst_port", parsed.dstPort),
							logger.Int("plain_bytes", len(out)))
					}
				}
			}
			select {
			case <-d.done:
				return
			case d.rx <- out:
			}
		}
	}
}
