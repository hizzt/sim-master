package imscore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/1239t/swu-go/pkg/logger"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"

	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

func resolveStableSIPInstance(cfg Config) string {
	if urn := strings.TrimSpace(cfg.SIPInstanceURN); urn != "" {
		return urn
	}
	return voiceclient.NewSIPInstanceURN()
}

type registerPhase string

const (
	registerPhaseInitial registerPhase = "initial"
	registerPhaseAuth    registerPhase = "auth"
	registerPhaseSecure  registerPhase = "secure"
)

type registerSession struct {
	cfg           Config
	swu           voiceclient.SWUTCPDialer
	network       IMSNetwork
	transportMode string
	state         *registerState
	phase         registerPhase
	jitter        bool

	conn      *connRegisterTransport
	callID    string
	cseq      uint32
	localPort int
}

func newRegisterSession(cfg Config, swu voiceclient.SWUTCPDialer, network IMSNetwork, transportMode string, attemptIndex int) *registerSession {
	state := newRegisterState(cfg)
	if attemptIndex > 0 {
		// A protected REGISTER rejected by one P-CSCF leaves its advertised
		// port-c in TCP TIME_WAIT. Each following P-CSCF candidate must offer a
		// fresh client port or the userspace stack cannot bind the secure flow.
		state.portC = randomEphemeralSIPPort()
	}
	localPort := registerAttemptLocalPort(cfg, attemptIndex)
	return &registerSession{
		cfg:           cfg,
		swu:           swu,
		network:       network,
		transportMode: strings.TrimSpace(transportMode),
		state:         state,
		phase:         registerPhaseInitial,
		jitter:        true,
		callID:        uuid.NewString(),
		cseq:          nextRegisterTransportAttemptCSeq(0),
		localPort:     localPort,
	}
}

func newRegisterState(cfg Config) *registerState {
	return &registerState{
		spiC:        randomNonZeroUint32(),
		spiS:        randomNonZeroUint32(),
		portC:       5064,
		portS:       5063,
		sipInstance: resolveStableSIPInstance(cfg),
		contactUser: uuid.NewString(),
	}
}

func (s *registerSession) imsNetwork() IMSNetwork {
	if s == nil {
		return nil
	}
	return s.network
}

func (s *registerSession) dialRegisterConn(ctx context.Context) (*connRegisterTransport, error) {
	if s == nil {
		return nil, fmt.Errorf("imscore: register session is nil")
	}
	if s.conn != nil {
		return s.conn, nil
	}

	if s.localPort <= 0 {
		s.localPort = registerSIPLocalPort(s.cfg)
	}
	transportAddr := effectiveTransportAddr(s.cfg)
	host, portStr, err := net.SplitHostPort(transportAddr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	rip := net.ParseIP(host)
	if rip == nil {
		return nil, fmt.Errorf("invalid transport P-CSCF %q", transportAddr)
	}
	raddr := &net.TCPAddr{IP: rip, Port: port}

	var rawConn net.Conn
	dialCtx := withLocalPort(ctx, s.localPort)
	switch {
	case s.network != nil:
		rawConn, err = s.network.DialContext(dialCtx, "tcp", raddr, s.transportMode, DialOptions{})
	case s.swu != nil:
		rawConn, err = s.swu.DialContextTCP(dialCtx, s.cfg.LocalIP, s.localPort, rip, port)
	default:
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: s.cfg.LocalIP, Port: s.localPort}}
		rawConn, err = d.DialContext(ctx, "tcp", transportAddr)
	}
	if err != nil {
		return nil, fmt.Errorf("register dial %s: %w", transportAddr, err)
	}

	installSIPTrace(s.cfg.TraceID, s.cfg.DeviceID)
	s.conn = newConnRegisterTransport(rawConn, s.cfg.TraceID, s.cfg.DeviceID)
	logger.Info("IMS REGISTER transport connected",
		logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
		logger.String("device_id", strings.TrimSpace(s.cfg.DeviceID)),
		logger.String("transport_mode", s.transportMode),
		logger.String("local", connLocalAddrString(s.conn.conn)),
		logger.String("remote", connRemoteAddrString(s.conn.conn)),
		logger.Int("local_port_hint", s.localPort))
	return s.conn, nil
}

func (s *registerSession) closeConn() {
	if s == nil || s.conn == nil {
		return
	}
	_ = s.conn.Close()
	s.conn = nil
}

func (s *registerSession) cleanupFailedAttempt() {
	if s == nil {
		return
	}
	s.closeConn()
	if s.state != nil && s.state.secureConn != nil {
		_ = s.state.secureConn.Close()
		s.state.secureConn = nil
	}
	if s.state != nil && s.state.transport != nil && s.cfg.IPSecInstaller != nil {
		s.cfg.IPSecInstaller.ClearIPSec3GPP()
		s.state.transport = nil
	}
}

func (s *registerSession) logFSM(event, reason string, variantIndex, variantTotal, mechanismCount int, variant initialRegisterVariant) {
	logger.Info(fmt.Sprintf("FSM(reg): %s", event),
		logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
		logger.String("device_id", strings.TrimSpace(s.cfg.DeviceID)),
		logger.String("phase", string(s.phase)),
		logger.String("registrar", strings.TrimSpace(s.cfg.PCSCFAddr)),
		logger.String("reason", reason),
		logger.Int("variant_index", variantIndex),
		logger.Int("variant_total", variantTotal),
		logger.String("variant_name", variant.name),
		logger.String("initial_auth", variant.initialAuth),
		logger.String("authorization_identity", variant.profile.AuthorizationIdentity),
		logger.String("user_agent", variant.profile.UserAgent),
		logger.String("location_mode", variant.locationMode),
		logger.Bool("include_pani", variant.includePANI),
		logger.Bool("include_cellular", variant.includeCellular),
		logger.Bool("require_sec_agree", variant.profile.IncludeRequireSecAgree),
		logger.String("security_client_format", variant.profile.SecurityClientFormat),
		logger.Int("security_client_mechanisms", mechanismCount),
	)
}

func (s *registerSession) runInitialRegisterFlow(ctx context.Context) (*registerResult, error) {
	if s.jitter {
		if err := waitInitialRegisterJitter(ctx, s.cfg); err != nil {
			return nil, err
		}
		s.jitter = false
	}

	defer s.closeConn()

	variants := initialRegisterVariants(s.cfg)
	var lastErr error
	firstAttempt := true
	temporaryResponseRetries := 0
	for i := 0; i < len(variants); {
		variant := variants[i]
		if !firstAttempt {
			s.resetRegisterState()
		}
		firstAttempt = false
		variantCfg := applyInitialRegisterVariantConfig(s.cfg, variant)
		s.logFSM("initial_register_attempt", "", i+1, len(variants), securityClientMechanismCount(s.cfg.Template), variant)

		res, req, transport, err := s.registerInitialVariantUntilResponse(ctx, variantCfg, variant, i+1, len(variants))
		if err != nil {
			return nil, err
		}

		logger.Info("IMS REGISTER initial response",
			logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
			logger.String("pcscf", s.cfg.PCSCFAddr),
			logger.String("variant_name", variant.name),
			logger.String("initial_auth", variant.initialAuth),
			logger.String("authorization_identity", variant.profile.AuthorizationIdentity),
			logger.String("user_agent", variant.profile.UserAgent),
			logger.String("location_mode", variant.locationMode),
			logger.Bool("include_pani", variant.includePANI),
			logger.Bool("include_cellular", variant.includeCellular),
			logger.Bool("security_server", res.GetHeader("Security-Server") != nil),
			logger.Bool("authenticate_challenge", len(res.GetHeaders("WWW-Authenticate")) > 0 || len(res.GetHeaders("Proxy-Authenticate")) > 0),
			logger.Int("status", res.StatusCode),
			logger.String("reason", res.Reason))

		switch res.StatusCode {
		case sip.StatusOK:
			decision, err := decideInitialRegisterSuccessSecurity(variantCfg, res)
			if err != nil {
				return nil, err
			}
			s.logFSM("initial_register_success", decision.reason, i+1, len(variants), securityClientMechanismCount(s.cfg.Template), variant)
			if decision.requireIPSec {
				if err := installIPSecFromChallenge(variantCfg, s.state, res); err != nil {
					return nil, err
				}
				s.phase = registerPhaseSecure
				return runSecureAuthenticatedRegister(ctx, variantCfg, s.swu, s.state, req, res)
			}
			return s.finalizeRegisterSuccess(variantCfg, *s.state, res)
		case sip.StatusUnauthorized, sip.StatusProxyAuthRequired:
			s.phase = registerPhaseAuth
			result, authErr := s.runAuthRegisterPhase(ctx, variantCfg, transport, req, res)
			if authErr == nil {
				return result, nil
			}
			lastErr = authErr
			if shouldRetryInitialRegisterSameVariant(ctx, variantCfg, authErr) {
				logger.Warn("IMS REGISTER auth timeout, restarting same variant",
					logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
					logger.Int("variant_index", i+1),
					logger.Int("variant_total", len(variants)),
					logger.String("variant_name", variant.name),
					logger.String("error", authErr.Error()))
				s.phase = registerPhaseInitial
				s.closeConn()
				continue
			}
			if shouldRetryInitialRegisterVariantForError(variantCfg, authErr, i, len(variants)) {
				var attemptErr *registrarAttemptError
				errors.As(authErr, &attemptErr)
				s.logInitialRegisterFallback(attemptErr.statusCode, attemptErr.reason, i, variants)
				s.phase = registerPhaseInitial
				s.closeConn()
				i++
				continue
			}
			return nil, authErr
		default:
			lastErr = &registrarAttemptError{
				pcscf:      s.cfg.PCSCFAddr,
				statusCode: res.StatusCode,
				reason:     res.Reason,
			}
			outcome := decideRegisterFailureOutcome(s.cfg, res.StatusCode, res.Reason, i, len(variants), false)
			if outcome.retryVariant {
				s.logInitialRegisterFallback(res.StatusCode, res.Reason, i, variants)
				temporaryResponseRetries = 0
				i++
				continue
			}
			if initialRejectFallbackEnabled(variantCfg) && isTemporaryRegisterSIPResponse(res.StatusCode) {
				if temporaryResponseRetries >= maxTemporaryResponseRetries {
					if i+1 >= len(variants) {
						return nil, lastErr
					}
					logger.Warn("IMS REGISTER temporary response retry limit reached, advancing variant",
						logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
						logger.Int("variant_index", i+1),
						logger.Int("variant_total", len(variants)),
						logger.String("variant_name", variant.name),
						logger.Int("status", res.StatusCode),
						logger.String("reason", res.Reason),
						logger.Int("temporary_retries", temporaryResponseRetries))
					s.logInitialRegisterFallback(res.StatusCode, res.Reason, i, variants)
					temporaryResponseRetries = 0
					i++
					continue
				}
				temporaryResponseRetries++
				retryDelay := temporaryRegisterResponseRetryDelay(variantCfg, res)
				logger.Warn("IMS REGISTER temporary response, retrying same variant",
					logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
					logger.Int("variant_index", i+1),
					logger.Int("variant_total", len(variants)),
					logger.String("variant_name", variant.name),
					logger.Int("status", res.StatusCode),
					logger.String("reason", res.Reason),
					logger.Int("temporary_retry", temporaryResponseRetries),
					logger.Int("temporary_retry_limit", maxTemporaryResponseRetries),
					logger.String("retry_delay", retryDelay.String()))
				s.closeConn()
				s.localPort = randomEphemeralSIPPort()
				if err := waitRegisterRetry(ctx, retryDelay); err != nil {
					return nil, err
				}
				continue
			}
			return nil, lastErr
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("imscore: initial REGISTER variants exhausted")
}

func temporaryRegisterResponseRetryDelay(cfg Config, res *sip.Response) time.Duration {
	seconds := cfg.Template.RegisterPolicy.TemporaryRetrySeconds
	if res != nil {
		if header := res.GetHeader("Retry-After"); header != nil {
			value := strings.TrimSpace(header.Value())
			if end := strings.IndexAny(value, " ;("); end >= 0 {
				value = value[:end]
			}
			if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
				seconds = parsed
			}
		}
	}
	if seconds <= 0 {
		seconds = 5
	}
	delay := time.Duration(seconds) * time.Second
	if initialRejectFallbackEnabled(cfg) && delay > maxMatrixTemporaryRetryDelay {
		return maxMatrixTemporaryRetryDelay
	}
	return delay
}

func waitRegisterRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *registerSession) resetRegisterState() {
	sipInstance := ""
	hadIPSec := false
	if s.state != nil {
		sipInstance = s.state.sipInstance
		hadIPSec = s.state.transport != nil
		if s.state.secureConn != nil {
			_ = s.state.secureConn.Close()
		}
	}
	if hadIPSec && s.cfg.IPSecInstaller != nil {
		s.cfg.IPSecInstaller.ClearIPSec3GPP()
	}
	s.state = newRegisterState(s.cfg)
	if hadIPSec {
		// A rejected protected REGISTER leaves the previous port-c in TCP
		// TIME_WAIT. Advertise a fresh client port for the next agreement.
		s.state.portC = randomEphemeralSIPPort()
	}
	if strings.TrimSpace(sipInstance) != "" {
		s.state.sipInstance = sipInstance
	}
}

func (s *registerSession) registerInitialVariantUntilResponse(ctx context.Context, cfg Config, variant initialRegisterVariant, variantIndex, variantTotal int) (*sip.Response, *sip.Request, *connRegisterTransport, error) {
	timeoutRetries := 0
	for retry := 1; ; retry++ {
		transport, err := s.dialRegisterConn(ctx)
		if err == nil {
			var res *sip.Response
			var req *sip.Request
			res, req, err = s.registerOnce(ctx, cfg, transport, true, variant)
			if err == nil {
				return res, req, transport, nil
			}
		}
		if !shouldRetryInitialRegisterSameVariant(ctx, cfg, err) {
			return nil, nil, nil, err
		}
		if isTimeoutError(err) {
			timeoutRetries++
			if timeoutRetries >= maxTransportTimeoutRetries {
				logger.Warn("IMS REGISTER P-CSCF timeout retry limit reached",
					logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
					logger.String("device_id", strings.TrimSpace(s.cfg.DeviceID)),
					logger.Int("variant_index", variantIndex),
					logger.Int("variant_total", variantTotal),
					logger.String("variant_name", variant.name),
					logger.Int("timeout_retries", timeoutRetries),
					logger.String("error", err.Error()))
				return nil, nil, nil, err
			}
		}
		logger.Warn("IMS REGISTER transport retry, reconnecting same variant",
			logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
			logger.String("device_id", strings.TrimSpace(s.cfg.DeviceID)),
			logger.Int("variant_index", variantIndex),
			logger.Int("variant_total", variantTotal),
			logger.String("variant_name", variant.name),
			logger.Int("same_variant_retry", retry),
			logger.Int("timeout_retry", timeoutRetries),
			logger.Int("timeout_retry_limit", maxTransportTimeoutRetries),
			logger.String("error", err.Error()))
		s.closeConn()
		s.localPort = randomEphemeralSIPPort()
	}
}

func shouldRetryInitialRegisterSameVariant(ctx context.Context, cfg Config, err error) bool {
	if err == nil || !initialRejectFallbackEnabled(cfg) {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeoutError(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection closed") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "port is in use") ||
		strings.Contains(message, "address already in use")
}

func shouldRetryInitialRegisterVariantForError(cfg Config, err error, variantIndex, variantTotal int) bool {
	var attemptErr *registrarAttemptError
	return errors.As(err, &attemptErr) &&
		shouldRetryInitialRegisterForStatus(cfg, attemptErr.statusCode) &&
		variantIndex+1 < variantTotal
}

func (s *registerSession) logInitialRegisterFallback(status int, reason string, variantIndex int, variants []initialRegisterVariant) {
	next := variants[variantIndex+1]
	logger.Info("IMS REGISTER initial reject fallback",
		logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
		logger.Int("status", status),
		logger.String("reason", reason),
		logger.Int("variant_index", variantIndex+1),
		logger.Int("variant_total", len(variants)),
		logger.String("next_variant_name", next.name),
		logger.String("next_initial_auth", next.initialAuth),
		logger.String("next_authorization_identity", next.profile.AuthorizationIdentity),
		logger.String("next_user_agent", next.profile.UserAgent),
		logger.String("next_location_mode", next.locationMode),
		logger.Bool("next_include_pani", next.includePANI),
		logger.Bool("next_include_cellular", next.includeCellular))
}

func (s *registerSession) runAuthRegisterPhase(ctx context.Context, cfg Config, transport *connRegisterTransport, initialReq *sip.Request, challengeRes *sip.Response) (*registerResult, error) {
	var lastReq = initialReq
	var lastRes = challengeRes

	for round := 0; round < maxChallengeRounds && (lastRes.StatusCode == 401 || lastRes.StatusCode == 407); round++ {
		decision, decisionErr := decideSecAgreeAfterChallenge(cfg, lastRes)
		if decisionErr != nil {
			return nil, decisionErr
		}
		if decision.installIPSec {
			result, secureErr := runSecureAuthenticatedRegister(ctx, cfg, s.swu, s.state, lastReq, lastRes)
			if secureErr == nil {
				return result, nil
			}
			var resyncErr *akaResyncRequiredError
			if !errors.As(secureErr, &resyncErr) || resyncErr.request == nil {
				return nil, secureErr
			}

			logger.Info("IMS-AKA AUTS resynchronization sent before ipsec-3gpp setup",
				logger.String("trace_id", strings.TrimSpace(s.cfg.TraceID)),
				logger.String("device_id", strings.TrimSpace(s.cfg.DeviceID)),
				logger.Int("challenge_round", round+1))
			if err := s.decorateRegisterRequest(resyncErr.request); err != nil {
				return nil, err
			}
			res, err := s.sendRegisterRequest(ctx, transport, resyncErr.request)
			if err != nil {
				return nil, err
			}
			lastReq, lastRes = resyncErr.request, res
			continue
		}

		res, req, ck, ik, messageAuth, err := s.answerRegisterChallenge(ctx, cfg, transport, lastReq, lastRes)
		if err != nil {
			return nil, fmt.Errorf("challenge round %d: %w", round+1, err)
		}
		s.state.ck, s.state.ik = ck, ik
		if messageAuth != nil {
			s.state.messageAuth = cloneDigestAuthSession(messageAuth)
		}
		lastReq, lastRes = req, res

		if lastRes.StatusCode != sip.StatusUnauthorized && lastRes.StatusCode != sip.StatusProxyAuthRequired {
			break
		}
	}

	if lastRes.StatusCode == sip.StatusOK {
		contactSMSIP := false
		contactCount := len(lastReq.GetHeaders("Contact"))
		for _, header := range lastReq.GetHeaders("Contact") {
			if header != nil && strings.Contains(strings.ToLower(header.Value()), "+g.3gpp.smsip") {
				contactSMSIP = true
				break
			}
		}
		logger.Info("IMS authenticated REGISTER binding sent",
			logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
			logger.Int("contact_count", contactCount),
			logger.Bool("smsip_binding", contactSMSIP))
		return s.finalizeRegisterSuccess(cfg, *s.state, lastRes)
	}

	if lastRes.StatusCode != sip.StatusUnauthorized && lastRes.StatusCode != sip.StatusProxyAuthRequired {
		return nil, &registrarAttemptError{pcscf: cfg.PCSCFAddr, statusCode: lastRes.StatusCode, reason: lastRes.Reason}
	}

	decision, err := decideSecAgreeAfterChallenge(cfg, lastRes)
	if err != nil {
		return nil, err
	}
	if !decision.installIPSec {
		return nil, fmt.Errorf("sec-agree requires ipsec install: %s", decision.reason)
	}
	return runSecureAuthenticatedRegister(ctx, cfg, s.swu, s.state, lastReq, lastRes)
}

func (s *registerSession) registerOnce(ctx context.Context, cfg Config, transport *connRegisterTransport, initial bool, variant initialRegisterVariant) (*sip.Response, *sip.Request, error) {
	req, err := buildRegisterRequest(cfg, *s.state, initial, variant, s.localPort)
	if err != nil {
		return nil, nil, err
	}
	if err := s.decorateRegisterRequest(req); err != nil {
		return nil, nil, err
	}
	if err := transport.Send(ctx, req); err != nil {
		return nil, req, err
	}
	res, err := transport.ReadResponse(ctx)
	return res, req, err
}

func (s *registerSession) answerRegisterChallenge(ctx context.Context, cfg Config, transport *connRegisterTransport, prevReq *sip.Request, prevRes *sip.Response) (*sip.Response, *sip.Request, []byte, []byte, *voiceclient.DigestAuthSession, error) {
	if prevReq == nil {
		variant := initialRegisterVariantFromProfile(effectiveRegisterProfile(cfg))
		req, err := buildRegisterRequest(cfg, *s.state, false, variant, s.localPort)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		prevReq = req
	}

	chal, err := selectDigestChallenge(cfg, prevRes)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	akaResult, authHeader, err := computeAKAAuth(cfg, chal, prevReq)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	newReq := prevReq.Clone()
	newReq.RemoveHeader("Via")
	newReq.RemoveHeader("Authorization")
	newReq.AppendHeader(sip.NewHeader("Authorization", authHeader))
	if err := s.decorateRegisterRequest(newReq); err != nil {
		return nil, nil, nil, nil, nil, err
	}

	res, err := s.sendRegisterRequest(ctx, transport, newReq)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return res, newReq, akaResult.CK, akaResult.IK, newDigestAuthSession(chal, akaResult.RES, cfg.PrivateID, prevRes), nil
}

func (s *registerSession) sendRegisterRequest(ctx context.Context, transport *connRegisterTransport, req *sip.Request) (*sip.Response, error) {
	if err := transport.Send(ctx, req); err != nil {
		return nil, err
	}
	return transport.ReadResponse(ctx)
}

func (s *registerSession) finalizeRegisterSuccess(cfg Config, state registerState, res *sip.Response) (*registerResult, error) {
	result, err := finalizeRegisterSuccess(cfg, state, res)
	if err != nil || result == nil || result.secureConn != nil || s == nil || s.conn == nil {
		return result, err
	}
	result.registerConn = s.conn.ReleaseConn()
	s.conn = nil
	return result, nil
}

func (s *registerSession) decorateRegisterRequest(req *sip.Request) error {
	if req == nil {
		return fmt.Errorf("missing REGISTER request")
	}
	req.RemoveHeader("Via")
	req.RemoveHeader("Call-ID")
	req.RemoveHeader("CSeq")
	req.RemoveHeader("Max-Forwards")

	if s.localPort <= 0 {
		s.localPort = registerSIPLocalPort(s.cfg)
	}
	viaHost := formatRegisterViaHost(s.cfg.LocalIP, s.localPort)
	via := fmt.Sprintf("SIP/2.0/TCP %s;branch=%s", viaHost, sip.GenerateBranchN(16))
	req.PrependHeader(sip.NewHeader("Via", via))
	req.AppendHeader(sip.NewHeader("Call-ID", s.callID))
	req.AppendHeader(sip.NewHeader("CSeq", fmt.Sprintf("%d REGISTER", s.cseq)))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	s.cseq = nextRegisterTransportAttemptCSeq(s.cseq)
	return nil
}

func formatRegisterViaHost(ip net.IP, port int) string {
	if ip == nil {
		return fmt.Sprintf("127.0.0.1:%d", port)
	}
	if ip.To4() == nil {
		return fmt.Sprintf("[%s]:%d", ip.String(), port)
	}
	return fmt.Sprintf("%s:%d", ip.String(), port)
}
