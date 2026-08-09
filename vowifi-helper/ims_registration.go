//go:build jane_vowifi

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jane-rui/vowifi-go/runtimehost"
	"github.com/jane-rui/vowifi-go/runtimehost/messaging"
	"github.com/jane-rui/vowifi-go/runtimehost/voiceclient"
)

type trackingSecurityInstaller struct {
	mu     sync.Mutex
	inner  *voiceclient.LinuxIMSSecurityXFRMInstaller
	report *reporter
}

var _ voiceclient.SecurityPlanInstaller = (*trackingSecurityInstaller)(nil)
var _ voiceclient.SecurityPlanRequestInstaller = (*trackingSecurityInstaller)(nil)

func newTrackingSecurityInstaller(report *reporter) *trackingSecurityInstaller {
	return &trackingSecurityInstaller{
		inner:  &voiceclient.LinuxIMSSecurityXFRMInstaller{},
		report: report,
	}
}

func (i *trackingSecurityInstaller) InstallSecurityPlan(context.Context, voiceclient.IMSSecurityAssociationPlan) error {
	return errors.New("IMS Security-Agree did not include the AKA keys required by the XFRM installer")
}

func (i *trackingSecurityInstaller) InstallSecurityPlanRequest(ctx context.Context, req voiceclient.IMSSecurityAssociationInstallRequest) error {
	if i.report != nil {
		i.report.mutate(func(status *tunnelStatus) {
			status.PCSCFProbeState = "responded"
			status.PCSCFReachable = true
			status.PCSCFSIPCode = 401
			status.PCSCFProbeReply = time.Now().UTC().Format(time.RFC3339Nano)
			status.IMSAuthenticated = true
			status.IMSState = "security_agreement"
			status.IMSSecurityMode = "ipsec3gpp"
			status.PCSCFProbeError = ""
			if strings.TrimSpace(req.RemoteEndpoint.Address) != "" {
				status.PCSCFAddress = strings.TrimSpace(req.RemoteEndpoint.Address)
			}
		})
	}
	i.mu.Lock()
	err := i.inner.InstallSecurityPlanRequest(ctx, req)
	i.mu.Unlock()
	if i.report != nil {
		i.report.mutate(func(status *tunnelStatus) {
			if err != nil {
				status.IMSState = "security_install_failed"
				status.IMSErrorClass = "ims_ipsec"
				status.Error = err.Error()
				return
			}
			status.IMSIPSecEstablished = true
			status.IMSState = "registering_protected"
			status.Error = ""
		})
	}
	return err
}

func (i *trackingSecurityInstaller) cleanup(ctx context.Context) error {
	if i == nil || i.inner == nil {
		return nil
	}
	i.mu.Lock()
	err := i.inner.Cleanup(ctx)
	i.mu.Unlock()
	return err
}

func (i *trackingSecurityInstaller) installed() bool {
	if i == nil || i.inner == nil {
		return false
	}
	i.mu.Lock()
	count := i.inner.StateCount()
	i.mu.Unlock()
	return count > 0
}

type failoverIMSRegistrar struct {
	candidates []string
	userAgent  string
	smsc       string
	report     *reporter
	security   *trackingSecurityInstaller
}

var _ runtimehost.IMSRegistrar = (*failoverIMSRegistrar)(nil)

func (r *failoverIMSRegistrar) RegisterIMS(ctx context.Context, cfg runtimehost.IMSRegistrationConfig) (runtimehost.IMSRegistrationResult, error) {
	if len(r.candidates) == 0 {
		return runtimehost.IMSRegistrationResult{}, errors.New("no IKE-assigned P-CSCF candidate is available")
	}
	var lastResult runtimehost.IMSRegistrationResult
	var failures []error
	for index, candidate := range r.candidates {
		target, network, err := pcscfSIPTarget(candidate)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if index > 0 {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = r.security.cleanup(cleanupCtx)
			cancel()
		}
		if r.report != nil {
			r.report.mutate(func(status *tunnelStatus) {
				status.PCSCFAddress = target
				status.PCSCFProbeState = "sent"
				status.PCSCFProbeSent = time.Now().UTC().Format(time.RFC3339Nano)
				status.PCSCFProbeError = ""
				status.IMSState = "registering"
				status.IMSTransport = "swu"
				status.Error = ""
			})
		}
		wire := runtimehost.WireIMSRegistrar{
			ServerAddr:            target,
			Network:               network,
			Timeout:               12 * time.Second,
			Expires:               3600,
			DisableKeepalive:      true,
			UserAgent:             firstNonEmptyString(r.userAgent, "SimAdmin VoWiFi"),
			RetransmitInterval:    500 * time.Millisecond,
			MaxRetransmitInterval: 2 * time.Second,
			MaxRetransmits:        4,
			SecurityPlanInstaller: r.security,
		}
		wire.SMSFactory = func(_ runtimehost.IMSRegistrationConfig, profile voiceclient.IMSProfile, binding voiceclient.RegistrationBinding, transport voiceclient.SIPRequestTransport) messaging.SMSTransport {
			return &messaging.IMSSMSTransport{
				Transport: transport, Profile: profile, Registration: binding,
				Domain: profile.Domain, UserAgent: wire.UserAgent, SMSC: strings.TrimSpace(r.smsc),
			}
		}
		result, err := wire.RegisterIMS(ctx, cfg)
		lastResult = result
		if err == nil && result.Registered {
			if r.report != nil {
				r.report.mutate(func(status *tunnelStatus) {
					status.Stage = "established"
					status.PCSCFProbeState = "responded"
					status.PCSCFReachable = true
					status.PCSCFSIPCode = result.StatusCode
					status.PCSCFProbeReply = time.Now().UTC().Format(time.RFC3339Nano)
					status.IMSState = "registered"
					status.IMSRegistered = true
					status.IMSAuthenticated = true
					status.IMSTransport = "swu"
					status.IMSSecurityMode = "ipsec3gpp"
					status.IMSIPSecEstablished = r.security.installed()
					status.SMSOverIMSReady = true
					status.PCSCFProbeError = ""
					status.IMSErrorClass = ""
					status.Error = ""
				})
			}
			return result, nil
		}
		failure := err
		if failure == nil {
			failure = fmt.Errorf("IMS REGISTER rejected: SIP %d %s", result.StatusCode, result.Reason)
		}
		failures = append(failures, fmt.Errorf("P-CSCF %s: %w", target, failure))
		if r.report != nil {
			r.report.mutate(func(status *tunnelStatus) {
				status.IMSState = "register_failed"
				status.IMSErrorClass = "ims"
				status.PCSCFProbeError = failure.Error()
				status.Error = failure.Error()
				if result.StatusCode > 0 {
					status.PCSCFReachable = true
					status.PCSCFSIPCode = result.StatusCode
					status.PCSCFProbeState = "responded"
					status.PCSCFProbeReply = time.Now().UTC().Format(time.RFC3339Nano)
				} else {
					status.PCSCFProbeState = "failed"
				}
			})
		}
		if ctx != nil && ctx.Err() != nil {
			break
		}
	}
	return lastResult, errors.Join(failures...)
}

func pcscfSIPTarget(candidate string) (string, string, error) {
	host := pcscfHost(candidate)
	if host == "" {
		return "", "", errors.New("P-CSCF candidate is empty")
	}
	port := 5060
	trimmed := strings.TrimSpace(candidate)
	if strings.HasPrefix(strings.ToLower(trimmed), "sip:") {
		trimmed = trimmed[4:]
	} else if strings.HasPrefix(strings.ToLower(trimmed), "sips:") {
		trimmed = trimmed[5:]
	}
	if parsedHost, parsedPort, err := net.SplitHostPort(trimmed); err == nil {
		host = strings.Trim(parsedHost, "[]")
		if value, parseErr := strconv.Atoi(parsedPort); parseErr == nil && value > 0 && value <= 65535 {
			port = value
		}
	}
	ip := net.ParseIP(host)
	network := "udp"
	if ip != nil && ip.To4() == nil {
		network = "udp6"
	} else if ip != nil {
		network = "udp4"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), network, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
