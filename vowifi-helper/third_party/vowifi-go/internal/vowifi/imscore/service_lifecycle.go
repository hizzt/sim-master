package imscore

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/1239t/swu-go/pkg/logger"

	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

// Start runs the full IMS Core lifecycle: REGISTER FSM, ipsec transport runtime,
// TCP write scheduler, and post-register messaging attach.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("imscore: service is nil")
	}
	if s.cfg.AKA == nil {
		return fmt.Errorf("imscore: Config.AKA is required")
	}
	if s.cfg.LocalIP == nil {
		return fmt.Errorf("imscore: Config.LocalIP is required")
	}

	addr := strings.TrimSpace(s.imsCfg.Registrar)
	if addr == "" {
		addr = strings.TrimSpace(s.cfg.PCSCFAddr)
	}
	logger.Info(fmt.Sprintf("[%s] IMS Core 正在启动", strings.TrimSpace(s.cfg.DeviceID)),
		logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
		logger.String("addr", addr),
		logger.String("transport", strings.TrimSpace(s.imsCfg.Transport)),
		logger.String("preset_id", strings.TrimSpace(s.imsCfg.CarrierPresetID)),
		logger.String("register_template", strings.TrimSpace(s.imsCfg.IMSRegisterTemplate.ID)),
		logger.String("register_policy", registerPolicyID(s.imsCfg.IMSRegisterTemplate)),
		logger.String("register_policy_source", strings.TrimSpace(s.imsCfg.IMSRegisterPolicySource)))

	swu, err := s.resolveSWUDialer()
	if err != nil {
		return err
	}
	s.swu = swu

	lifecycleCtx, cancel := context.WithCancel(ctx)
	s.lifecycleCtx = lifecycleCtx
	s.lifecycleCancel = cancel

	registerCtx := lifecycleCtx
	registerCancel := func() {}
	if !initialRejectFallbackEnabled(s.cfg) {
		registerCtx, registerCancel = context.WithTimeout(lifecycleCtx, registerDialTimeout)
	}
	defer registerCancel()

	reg, err := s.runRegisterFlow(registerCtx)
	if err != nil {
		logger.Warn("IMS register failed",
			logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
			logger.String("device_id", strings.TrimSpace(s.cfg.DeviceID)),
			logger.String("pcscf", s.cfg.PCSCFAddr),
			logger.Int("registrar_candidates", len(s.cfg.RegistrarCandidates)),
			logger.String("error", err.Error()))
		return fmt.Errorf("register: %w", err)
	}

	winningPCSCF := strings.TrimSpace(reg.pcscfAddr)
	if winningPCSCF == "" {
		winningPCSCF = s.cfg.PCSCFAddr
	}
	winningTransportPCSCF := strings.TrimSpace(reg.transportPCSCFAddr)
	if winningTransportPCSCF == "" {
		winningTransportPCSCF = effectiveTransportAddr(s.cfg)
	}
	s.cfg.PCSCFAddr = winningPCSCF
	s.cfg.TransportPCSCFAddr = winningTransportPCSCF
	s.imsCfg.Registrar = winningPCSCF
	s.imsCfg.PCSCF = winningPCSCF

	s.registered = true
	s.expiresSeconds = reg.expiresSeconds
	s.verifyHeader = reg.verifyHeader
	s.ipsecInstalled = reg.secureConn != nil
	s.sipSecurityMode = securityModeLabel(s.ipsecInstalled)
	// Expose the actual remote endpoint in status. The logical registrar can
	// be the tunnel's own IPv6 address when SIP is routed through a P-CSCF
	// gateway learned from IKE.
	s.pcscf = winningTransportPCSCF
	s.localAddr = s.cfg.LocalIP.String()
	s.phoneNumber = strings.TrimSpace(reg.phoneNumber)

	if reg.secureConn != nil && reg.transport != nil {
		rt, err := startTransportRuntime(lifecycleCtx, s.cfg, swu, reg.ipsecPolicy, reg.transport, reg.secureConn)
		if err != nil {
			logger.Warn("IMS transport runtime start failed",
				logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
				logger.String("error", err.Error()))
		} else {
			s.transportRuntime = rt
			s.logTCPWriterLoop(lifecycleCtx, reg.secureConn)
		}
	}

	if err := s.attachMessaging(lifecycleCtx, winningPCSCF, reg); err != nil {
		return err
	}
	s.started = true
	return nil
}

func (s *Service) resolveSWUDialer() (voiceclient.SWUTCPDialer, error) {
	if s == nil {
		return nil, fmt.Errorf("imscore: service is nil")
	}
	if us, ok := s.network.(*UserspaceIMSNetwork); ok && us != nil {
		if dialer := us.SWUDialer(); dialer != nil {
			return dialer, nil
		}
	}
	return newSWUNetstack(s.cfg.LocalIP, s.cfg.Dataplane)
}

func (s *Service) logTCPWriterLoop(ctx context.Context, conn net.Conn) {
	if s == nil || s.transportRuntime == nil || conn == nil {
		return
	}
	local := ""
	if conn.LocalAddr() != nil {
		local = conn.LocalAddr().String()
	}
	logger.Info(fmt.Sprintf("[%s] TCP 专用写通道调度器已启动", strings.TrimSpace(s.cfg.DeviceID)),
		logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
		logger.String("local", local))

	go func() {
		<-ctx.Done()
		logger.Info(fmt.Sprintf("[%s] TCP 专用写通道调度器已退出", strings.TrimSpace(s.cfg.DeviceID)),
			logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
			logger.String("local", local))
	}()
}

// attachMessaging hooks voiceclient for SMS/USSD after imscore registration.
func (s *Service) attachMessaging(ctx context.Context, winningPCSCF string, reg *registerResult) error {
	transportPCSCF := strings.TrimSpace(s.cfg.TransportPCSCFAddr)
	if transportPCSCF == "" {
		transportPCSCF = winningPCSCF
	}
	voiceCfg := voiceclient.Config{
		DeviceID: s.cfg.DeviceID,
		TraceID:  s.cfg.TraceID,
		LocalIP:  s.cfg.LocalIP,
		// Fallback port when no successful plain REGISTER connection can be
		// retained (for example, after a separate 3GPP IPsec setup).
		LocalPort:               randomEphemeralSIPPort(),
		Dataplane:               s.cfg.Dataplane,
		PCSCFAddr:               transportPCSCF,
		ServiceRoutes:           append([]string(nil), reg.serviceRoutes...),
		PreemptiveAuth:          cloneDigestAuthSession(reg.messageAuth),
		SecurityVerify:          reg.verifyHeader,
		PreferredIdentity:       reg.preferredIdentity,
		SMSPreferredIdentity:    telephonePublicIdentity(reg.phoneNumber),
		Realm:                   s.cfg.Realm,
		PrivateID:               s.cfg.PrivateID,
		EAPPrivateID:            s.cfg.EAPPrivateID,
		PublicURI:               s.cfg.PublicURI,
		HomeDomain:              s.cfg.HomeDomain,
		IMSI:                    s.cfg.IMSI,
		SMSC:                    s.cfg.SMSC,
		Transport:               "tcp",
		MCC:                     s.cfg.MCC,
		MNC:                     s.cfg.MNC,
		CellID:                  s.cfg.CellID,
		AKA:                     s.cfg.AKA,
		DeliveryStore:           s.cfg.DeliveryStore,
		InboundSMSHandler:       s.cfg.InboundSMSHandler,
		InboundSMSResultHandler: s.cfg.InboundSMSResultHandler,
		SIPInstanceURN:          s.cfg.SIPInstanceURN,
		RegisterProfile:         messagingRegisterProfile(s.cfg),
		SkipRegister:            true,
	}
	if reg.secureConn != nil {
		// Reuse the authenticated port-c connection established by the protected
		// REGISTER. The logical P-CSCF remains the Route URI; service transactions
		// must target the Security-Server port-s peer on this wrapped connection.
		voiceCfg.SWUDialer = newRetainedConnDialer(reg.secureConn, s.swu)
		// The protected connection has a translated endpoint. An empty adoption
		// lookup consumes the one-shot connection before the first real service
		// transaction, so let that transaction claim it directly. The security
		// runtime owns and closes the underlying connection.
		voiceCfg.CloseSWUDialer = false
		if s.transportRuntime != nil {
			voiceCfg.InboundTCPListener = s.transportRuntime.portSListener
		}
		voiceCfg.ContactPort = reg.ipsecPolicy.LocalPortS
		if local, ok := reg.secureConn.LocalAddr().(*net.TCPAddr); ok && local != nil && local.Port > 0 {
			voiceCfg.LocalPort = local.Port
		}
		if remote := reg.secureConn.RemoteAddr(); remote != nil {
			voiceCfg.TransportPCSCFAddr = remote.String()
		}
	} else if reg.registerConn != nil {
		// Reuse the REGISTER flow first because some IMS cores bind services to
		// it. If the P-CSCF later resets that TCP connection, allow sipgo to
		// open a fresh connection through the same shared SWu netstack.
		voiceCfg.SWUDialer = newRetainedConnDialer(reg.registerConn, s.swu)
		voiceCfg.CloseSWUDialer = true
		if local, ok := reg.registerConn.LocalAddr().(*net.TCPAddr); ok && local != nil && local.Port > 0 {
			voiceCfg.LocalPort = local.Port
		}
		reg.registerConn = nil
	} else if s.swu != nil {
		// REGISTER and SMS must share one userspace netstack: InnerPackets is
		// a single-consumer channel, so a second netstack can steal responses.
		voiceCfg.SWUDialer = s.swu
	}
	if s.cfg.RegisterExpirySeconds > 0 {
		voiceCfg.RegisterExpiry = time.Duration(s.cfg.RegisterExpirySeconds) * time.Second
	}
	inner, err := voiceclient.Dial(ctx, voiceCfg)
	if err != nil {
		return fmt.Errorf("voiceclient attach: %w", err)
	}
	s.inner = inner
	return nil
}

func telephonePublicIdentity(phoneNumber string) string {
	phoneNumber = strings.TrimSpace(phoneNumber)
	if phoneNumber == "" {
		return ""
	}
	return "tel:" + phoneNumber
}

func messagingRegisterProfile(cfg Config) voiceclient.RegisterProfile {
	profile := cfg.RegisterProfile
	variant := applyLocationHeaderMode(cfg.Template.LocationHeaderMode, initialRegisterVariantFromProfile(profile))
	profile.IncludePAccessNetworkInfo = variant.includePANI
	profile.IncludeCellularNetwork = variant.includeCellular
	return profile
}

// Stop tears down the IMS Core lifecycle.
func (s *Service) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
	return s.Close(ctx)
}
