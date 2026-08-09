package imscore

import (
	"net"
	"testing"

	"github.com/emiago/sipgo/sip"
)

func TestEffectiveTransportAddrPrefersExplicitOverride(t *testing.T) {
	local := net.ParseIP("2a03:dd00:1106:5794:20a6:6ac0:4821:850e")
	cfg := Config{
		LocalIP:            local,
		PCSCFAddr:          "[2a03:dd00:1106:5794:20a6:6ac0:4821:850e]:5060",
		TransportPCSCFAddr: "[2a03:dd00:1f81:3010::4]:5060",
		RegistrarCandidates: []string{
			"[2a03:dd00:1106:5794:20a6:6ac0:4821:850e]:5060",
			"[2a03:dd00:1f81:3010::4]:5060",
		},
	}
	if got := effectiveTransportAddr(cfg); got != "[2a03:dd00:1f81:3010::4]:5060" {
		t.Fatalf("transport=%q", got)
	}
	ip := effectiveIPSecRemoteIP(cfg)
	if ip == nil || ip.String() != "2a03:dd00:1f81:3010::4" {
		t.Fatalf("ipsec remote=%v", ip)
	}
}

func TestEffectiveIPSecRemoteIPFromRegistrarWhenNoOverride(t *testing.T) {
	cfg := Config{PCSCFAddr: "[2a03:dd00:1f80:4860::4]:5060"}
	ip := effectiveIPSecRemoteIP(cfg)
	if ip == nil || ip.String() != "2a03:dd00:1f80:4860::4" {
		t.Fatalf("ipsec remote=%v", ip)
	}
}

func TestEffectiveRouteAddrUsesGatewayForLocalRegistrar(t *testing.T) {
	local := net.ParseIP("2a03:dd00:140b:cd45:c849:9ff6:a0c0:6646")
	cfg := Config{
		LocalIP:            local,
		PCSCFAddr:          "[2a03:dd00:140b:cd45:c849:9ff6:a0c0:6646]:5060",
		TransportPCSCFAddr: "[2a03:dd00:1f80:60::4]:5060",
	}
	if got := effectiveRouteAddr(cfg); got != "[2a03:dd00:1f80:60::4]:5060" {
		t.Fatalf("route=%q", got)
	}
}

func TestEffectiveIPSecGatewayUsesIKECandidateForLocalRegistrar(t *testing.T) {
	local := net.ParseIP("2a03:dd00:140b:cd45:c849:9ff6:a0c0:6646")
	cfg := Config{
		LocalIP:   local,
		PCSCFAddr: "[2a03:dd00:140b:cd45:c849:9ff6:a0c0:6646]:5060",
		RegistrarCandidates: []string{
			"[2a03:dd00:140b:cd45:c849:9ff6:a0c0:6646]:5060",
			"[2a03:dd00:1f80:60::4]:5060",
		},
	}
	if got := effectiveTransportAddr(cfg); got != "[2a03:dd00:140b:cd45:c849:9ff6:a0c0:6646]:5060" {
		t.Fatalf("transport=%q", got)
	}
	if got := effectiveIPSecGatewayAddr(cfg); got != "[2a03:dd00:1f80:60::4]:5060" {
		t.Fatalf("ipsec gateway=%q", got)
	}
	ip := effectiveIPSecRemoteIP(cfg)
	if ip == nil || ip.String() != "2a03:dd00:1f80:60::4" {
		t.Fatalf("ipsec remote=%v", ip)
	}
	_ = net.ParseIP
}

func TestFinalizeRegisterSuccessPreservesTransportPCSCF(t *testing.T) {
	cfg := Config{
		PCSCFAddr:          "[2a03:dd00:110e:550::1]:5060",
		TransportPCSCFAddr: "[2a03:dd00:1f80:4060::4]:5060",
	}
	response := sip.NewResponse(200, "OK")
	response.AppendHeader(sip.NewHeader("Service-Route", "<sip:[2a03:dd00:1f80:4060::4]:5060;lr>"))
	response.AppendHeader(sip.NewHeader("P-Associated-URI", "<sip:network-default@ims.example>, <tel:+18005550123>"))
	result, err := finalizeRegisterSuccess(cfg, registerState{}, response)
	if err != nil {
		t.Fatalf("finalizeRegisterSuccess: %v", err)
	}
	if result.pcscfAddr != cfg.PCSCFAddr {
		t.Fatalf("registrar = %q", result.pcscfAddr)
	}
	if result.transportPCSCFAddr != cfg.TransportPCSCFAddr {
		t.Fatalf("transport P-CSCF = %q", result.transportPCSCFAddr)
	}
	if len(result.serviceRoutes) != 1 || result.serviceRoutes[0] != "<sip:[2a03:dd00:1f80:4060::4]:5060;lr>" {
		t.Fatalf("service routes = %v", result.serviceRoutes)
	}
	if result.preferredIdentity != "sip:network-default@ims.example" {
		t.Fatalf("preferred identity = %q", result.preferredIdentity)
	}
	if result.phoneNumber != "+18005550123" {
		t.Fatalf("phone number = %q", result.phoneNumber)
	}
}

func TestRegisterSessionHandsPlainConnectionToResult(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	session := &registerSession{conn: newConnRegisterTransport(clientConn, "trace", "device")}
	result, err := session.finalizeRegisterSuccess(Config{}, registerState{}, sip.NewResponse(200, "OK"))
	if err != nil {
		t.Fatalf("finalizeRegisterSuccess: %v", err)
	}
	if result.registerConn == nil {
		t.Fatal("plain REGISTER connection was not retained")
	}
	if session.conn != nil {
		t.Fatal("register session still owns the retained connection")
	}
	_ = result.registerConn.Close()
}
