package voiceclient

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1239t/vowifi-go/runtimehost/messaging"
	"github.com/emiago/sipgo/sip"
)

type inboundTCPTestDialer struct {
	dials atomic.Int32
}

func (d *inboundTCPTestDialer) DialContextTCP(ctx context.Context, _ net.IP, _ int, remoteIP net.IP, remotePort int) (net.Conn, error) {
	d.dials.Add(1)
	return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(remoteIP.String(), strconv.Itoa(remotePort)))
}

func (*inboundTCPTestDialer) ListenContextTCP(context.Context, net.IP, int) (net.Listener, error) {
	return nil, errors.New("not implemented")
}

func (*inboundTCPTestDialer) Close() error { return nil }

func TestSkipRegisterAdoptsOwnedTCPFlowForInboundMessage(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	dialer := &inboundTCPTestDialer{}
	inbound := make(chan messaging.InboundSMS, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, Config{
		DeviceID:   "tcp-inbound-test",
		TraceID:    "tcp-inbound-test",
		LocalIP:    net.ParseIP("127.0.0.1"),
		LocalPort:  15060,
		Dataplane:  &ownershipTestDataplane{rx: make(chan []byte)},
		SWUDialer:  dialer,
		PCSCFAddr:  listener.Addr().String(),
		Transport:  "tcp",
		PrivateID:  "subscriber@ims.example",
		PublicURI:  "sip:subscriber@ims.example",
		HomeDomain: "ims.example",
		AKA:        ownershipTestAKA{},

		SkipRegister:   true,
		CloseSWUDialer: true,
		InboundSMSHandler: func(message messaging.InboundSMS) (messaging.InboundSMSReply, error) {
			inbound <- message
			return messaging.InboundSMSReply{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close(context.Background())
	if dialer.dials.Load() != 1 {
		t.Fatalf("registered flow dial count = %d, want 1", dialer.dials.Load())
	}

	serverConn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()
	_ = serverConn.SetDeadline(time.Now().Add(3 * time.Second))

	req := newInboundSMSRequestForTest(smsContentType, []byte{0x01, 0x2a, 0x00, 0x00})
	req.SetTransport("TCP")
	req.SetSource(listener.Addr().String())
	if _, err := serverConn.Write([]byte(req.String())); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-inbound:
		if got.CallID != "inbound-call" || len(got.Body) < 2 || got.Body[1] != 0x2a {
			t.Fatalf("inbound message = %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("inbound MESSAGE was not read from the adopted REGISTER flow")
	}

	buf := make([]byte, 16*1024)
	n, err := serverConn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	message, err := sip.ParseMessage(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	response, ok := message.(*sip.Response)
	if !ok || response.StatusCode != sip.StatusOK {
		t.Fatalf("SIP response = %T %+v", message, message)
	}
}

func TestProtectedPortSListenerServesInboundMessage(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	inbound := make(chan messaging.InboundSMS, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, Config{
		DeviceID:           "port-s-inbound-test",
		TraceID:            "port-s-inbound-test",
		LocalIP:            net.ParseIP("127.0.0.1"),
		LocalPort:          15060,
		Dataplane:          &ownershipTestDataplane{rx: make(chan []byte)},
		InboundTCPListener: listener,
		PCSCFAddr:          "127.0.0.1:5060",
		Transport:          "tcp",
		PrivateID:          "subscriber@ims.example",
		PublicURI:          "sip:subscriber@ims.example",
		HomeDomain:         "ims.example",
		AKA:                ownershipTestAKA{},
		SkipRegister:       true,
		InboundSMSHandler: func(message messaging.InboundSMS) (messaging.InboundSMSReply, error) {
			inbound <- message
			return messaging.InboundSMSReply{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close(context.Background())

	conn, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	req := newInboundSMSRequestForTest(smsContentType, []byte{0x01, 0x2b, 0x00, 0x00})
	req.SetTransport("TCP")
	req.SetSource(listener.Addr().String())
	if _, err := conn.Write([]byte(req.String())); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-inbound:
		if got.CallID != "inbound-call" || len(got.Body) < 2 || got.Body[1] != 0x2b {
			t.Fatalf("inbound message = %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("inbound MESSAGE was not served from the protected port-s listener")
	}

	buf := make([]byte, 16*1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	message, err := sip.ParseMessage(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	response, ok := message.(*sip.Response)
	if !ok || response.StatusCode != sip.StatusOK {
		t.Fatalf("SIP response = %T %+v", message, message)
	}
}
