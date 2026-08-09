package imscore

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/1239t/swu-go/pkg/logger"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"

	"github.com/1239t/vowifi-go/engine/sim"
	"github.com/1239t/vowifi-go/internal/vowifi/imsheaders"
	"github.com/1239t/vowifi-go/internal/vowifi/ipsec3gpp"
	"github.com/1239t/vowifi-go/internal/vowifi/policy"
	"github.com/1239t/vowifi-go/runtimehost/simauth"
	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

const (
	registerTransactionTimeout   = 12 * time.Second
	registerCandidateTimeout     = 15 * time.Second
	registerDialTimeout          = 90 * time.Second
	maxChallengeRounds           = 2
	maxTemporaryResponseRetries  = 3
	maxTransportTimeoutRetries   = 12
	maxMatrixTemporaryRetryDelay = 10 * time.Second
)

type registerState struct {
	spiC  uint32
	spiS  uint32
	portC int
	portS int

	ck []byte
	ik []byte

	sipInstance   string
	selectedOffer *imsheaders.SecurityOffer
	ipsecPolicy   ipsec3gpp.Policy
	transport     *ipsec3gpp.Transport
	secureConn    *ipsec3gpp.SecureChannelConn

	expiresSeconds int
	verifyHeader   string
	contactUser    string
	messageAuth    *voiceclient.DigestAuthSession
}

type registerResult struct {
	// pcscfAddr is the logical SIP registrar (which may be the UE's inner
	// address). transportPCSCFAddr is the actual remote P-CSCF/gateway used by
	// the userspace TCP dialer.
	pcscfAddr          string
	transportPCSCFAddr string
	serviceRoutes      []string
	preferredIdentity  string
	phoneNumber        string
	registerConn       net.Conn
	expiresSeconds     int
	verifyHeader       string
	secureConn         *ipsec3gpp.SecureChannelConn
	ipsecPolicy        ipsec3gpp.Policy
	transport          *ipsec3gpp.Transport
	messageAuth        *voiceclient.DigestAuthSession
}

type akaResyncRequiredError struct {
	request *sip.Request
}

func (e *akaResyncRequiredError) Error() string {
	return "IMS-AKA synchronization requires a plain AUTS REGISTER before ipsec-3gpp setup"
}

type initialRegisterVariant struct {
	name            string
	initialAuth     string
	includePANI     bool
	includeCellular bool
	locationMode    string
	profile         voiceclient.RegisterProfile
}

func initialRejectFallbackEnabled(cfg Config) bool {
	if cfg.Template.EnableInitialRejectFallback {
		return true
	}
	return strings.TrimSpace(os.Getenv("VOHIVE_IMS_INITIAL_REJECT_FALLBACK")) == "1"
}

func initialRegisterVariants(cfg Config) []initialRegisterVariant {
	profile := effectiveRegisterProfile(cfg)
	base := initialRegisterVariantFromProfile(profile)
	base.name = "configured"
	base = applyLocationHeaderMode(cfg.Template.LocationHeaderMode, base)
	if !initialRejectFallbackEnabled(cfg) {
		return []initialRegisterVariant{base}
	}

	profiles := voiceclient.RegisterVariantsForProfile(profile)
	if len(profiles) == 0 {
		profiles = []voiceclient.RegisterProfile{profile}
	}
	if strings.EqualFold(strings.TrimSpace(profile.VariantSet), "simadmin_gb_ee") {
		for i := range profiles {
			if strings.TrimSpace(profile.SecurityClientFormat) != "" {
				profiles[i].SecurityClientFormat = profile.SecurityClientFormat
			}
		}
		profiles = append(profiles, voiceclient.XiaomiMi11RegisterProfile())
		pixelWithAuth := voiceclient.Pixel7ProRegisterProfile()
		pixelWithoutAuth := pixelWithAuth
		pixelWithoutAuth.InitialAuthorization = "none"
		pixelLegacy := voiceclient.LegacyVohiveRegisterProfile()
		pixelLegacy.UserAgent = pixelWithAuth.UserAgent
		profiles = append([]voiceclient.RegisterProfile{
			pixelWithAuth,
			pixelWithoutAuth,
			pixelLegacy,
			voiceclient.LegacyVohiveRegisterProfile(),
		}, profiles...)
	}

	locationModes := []string{"both", "pani_only", "cellular_only", "none"}
	variants := make([]initialRegisterVariant, 0, len(profiles)*len(locationModes))
	for profileIndex, candidateProfile := range profiles {
		if strings.TrimSpace(candidateProfile.UserAgent) == "" {
			candidateProfile.UserAgent = profile.UserAgent
		}
		for _, locationMode := range locationModes {
			variant := applyLocationHeaderMode(locationMode, initialRegisterVariantFromProfile(candidateProfile))
			variant.name = fmt.Sprintf("profile_%02d_%s", profileIndex+1, locationMode)
			variants = append(variants, variant)
		}
	}
	return variants
}

func effectiveRegisterProfile(cfg Config) voiceclient.RegisterProfile {
	profile := cfg.RegisterProfile
	if strings.TrimSpace(profile.ContactFeatures) == "" {
		profile = voiceclient.DefaultGBEERegisterProfile()
		profile.InitialAuthorization = ""
	}
	if strings.TrimSpace(profile.UserAgent) == "" {
		profile.UserAgent = strings.TrimSpace(cfg.UserAgent)
	}
	if strings.TrimSpace(profile.UserAgent) == "" {
		profile.UserAgent = "SimAdmin VoWiFi"
	}
	return profile
}

func initialRegisterVariantFromProfile(profile voiceclient.RegisterProfile) initialRegisterVariant {
	return initialRegisterVariant{
		initialAuth:     strings.TrimSpace(profile.InitialAuthorization),
		includePANI:     profile.IncludePAccessNetworkInfo,
		includeCellular: profile.IncludeCellularNetwork,
		profile:         profile,
	}
}

func applyLocationHeaderMode(mode string, variant initialRegisterVariant) initialRegisterVariant {
	variant.locationMode = strings.ToLower(strings.TrimSpace(mode))
	switch variant.locationMode {
	case "both":
		variant.includePANI = true
		variant.includeCellular = true
	case "pani_only":
		variant.includePANI = true
		variant.includeCellular = false
	case "cellular_only":
		variant.includePANI = false
		variant.includeCellular = true
	case "none":
		variant.includePANI = false
		variant.includeCellular = false
	}
	return variant
}

func shouldRetryInitialRegisterForStatus(cfg Config, statusCode int) bool {
	if !initialRejectFallbackEnabled(cfg) {
		return false
	}
	if statusCode == sip.StatusForbidden {
		return true
	}
	for _, code := range cfg.Template.RegisterPolicy.InitialRejectFallbackStatusCodes {
		if code == statusCode {
			return true
		}
	}
	return false
}

func runSecureAuthenticatedRegister(ctx context.Context, cfg Config, swuTCP voiceclient.SWUTCPDialer, state *registerState, lastReq *sip.Request, lastRes *sip.Response) (*registerResult, error) {
	authReq, akaResult, messageAuth, err := buildAuthenticatedRegister(cfg, *state, lastReq, lastRes)
	if err != nil {
		return nil, err
	}
	if len(akaResult.CK) == 0 || len(akaResult.IK) == 0 {
		if len(akaResult.AUTS) > 0 {
			return nil, &akaResyncRequiredError{request: authReq}
		}
		return nil, fmt.Errorf("IMS-AKA did not yield CK/IK for protected REGISTER")
	}
	state.ck = append(state.ck[:0], akaResult.CK...)
	state.ik = append(state.ik[:0], akaResult.IK...)
	state.messageAuth = cloneDigestAuthSession(messageAuth)
	if err := installIPSecFromChallenge(cfg, state, lastRes); err != nil {
		return nil, fmt.Errorf("ipsec install: %w", err)
	}
	authReq.RemoveHeader("Security-Verify")
	if state.verifyHeader != "" {
		authReq.AppendHeader(sip.NewHeader("Security-Verify", state.verifyHeader))
	}
	// Keep the original capability list and preloaded P-CSCF Route stable across
	// the protected retry. Security agreement changes the transport ports, not
	// the REGISTER route set.
	selectedSecurityClient := buildFullSecurityClient(
		cfg.Template,
		state.spiC,
		state.spiS,
		state.portC,
		state.portS,
	)
	if selectedSecurityClient != "" {
		authReq.RemoveHeader("Security-Client")
		authReq.AppendHeader(sip.NewHeader("Security-Client", selectedSecurityClient))
	}
	authReq.RemoveHeader("Contact")
	authReq.AppendHeader(sip.NewHeader(
		"Contact",
		buildProfileContact(cfg, *state, state.ipsecPolicy.LocalPortS, effectiveRegisterProfile(cfg)),
	))

	logger.Info("IMS ipsec-3gpp security agreement installed",
		logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
		logger.String("device_id", strings.TrimSpace(cfg.DeviceID)),
		logger.String("security_alg", state.selectedOffer.Alg),
		logger.String("security_ealg", state.selectedOffer.EAlg),
		logger.String("security_prot", state.selectedOffer.Prot),
		logger.String("security_mode", state.selectedOffer.Mode),
		logger.String("security_verify", state.verifyHeader),
		logger.Int("ue_port_c", state.ipsecPolicy.LocalPortC),
		logger.Int("ue_port_s", state.ipsecPolicy.LocalPortS),
		logger.Int("pcscf_port_c", state.ipsecPolicy.RemotePortC),
		logger.Int("pcscf_port_s", state.ipsecPolicy.RemotePortS),
		logger.String("protected_security_client", selectedSecurityClient))

	stopPreRegisterListener := startPreRegisterPortSListener(ctx, cfg, swuTCP, *state)
	defer stopPreRegisterListener()
	return runSecurePreparedRegister(ctx, cfg, swuTCP, state, authReq)
}

// startPreRegisterPortSListener makes the UE server port reachable before the
// protected REGISTER. Mature ipsec-3gpp stacks bring up both UE ports when the
// temporary SAs are installed; some P-CSCFs validate the advertised port-s
// before accepting the security agreement.
func startPreRegisterPortSListener(ctx context.Context, cfg Config, swuTCP voiceclient.SWUTCPDialer, state registerState) func() {
	if swuTCP == nil || state.ipsecPolicy.LocalPortS <= 0 {
		return func() {}
	}
	listener, err := swuTCP.ListenContextTCP(ctx, cfg.LocalIP, state.ipsecPolicy.LocalPortS)
	if err != nil {
		logger.Warn("IMS pre-register port-s listen failed",
			logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
			logger.Int("port_s", state.ipsecPolicy.LocalPortS),
			logger.String("error", err.Error()))
		return func() {}
	}
	logger.Info("IMS pre-register port-s listener started",
		logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
		logger.Int("port_s", state.ipsecPolicy.LocalPortS))

	done := make(chan struct{})
	go func() {
		rawConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		logger.Info("IMS pre-register port-s accepted connection",
			logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
			logger.String("local", rawConn.LocalAddr().String()),
			logger.String("remote", rawConn.RemoteAddr().String()))
		select {
		case <-done:
		case <-ctx.Done():
		}
		_ = rawConn.Close()
	}()
	return func() {
		close(done)
		_ = listener.Close()
	}
}

func runSecurePreparedRegister(ctx context.Context, cfg Config, swuTCP voiceclient.SWUTCPDialer, state *registerState, authReq *sip.Request) (*registerResult, error) {
	if authReq == nil {
		return nil, fmt.Errorf("missing authenticated REGISTER request")
	}
	secureConn, err := dialSecureRegisterConn(ctx, cfg, swuTCP, *state)
	if err != nil {
		return nil, fmt.Errorf("secure channel dial: %w", err)
	}
	transport := newConnRegisterTransport(secureConn, cfg.TraceID, cfg.DeviceID)
	defer transport.Close()

	if remote := secureConn.RemoteAddr(); remote != nil {
		authReq.SetDestination(remote.String())
	}
	if err := prepareProtectedRegisterRequest(cfg, *state, authReq); err != nil {
		return nil, err
	}
	if err := logProtectedRegisterShape(cfg)(nil, authReq); err != nil {
		return nil, err
	}
	if err := transport.Send(ctx, authReq); err != nil {
		return nil, fmt.Errorf("authenticated REGISTER send: %w", err)
	}
	finalRes, err := transport.ReadResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticated REGISTER: %w", err)
	}
	if finalRes.StatusCode != sip.StatusOK {
		reasonHeader := ""
		if header := finalRes.GetHeader("Reason"); header != nil {
			reasonHeader = strings.TrimSpace(header.Value())
		}
		warningHeader := ""
		if header := finalRes.GetHeader("Warning"); header != nil {
			warningHeader = strings.TrimSpace(header.Value())
		}
		logger.Warn("IMS protected REGISTER rejected",
			logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
			logger.Int("status", finalRes.StatusCode),
			logger.String("reason", finalRes.Reason),
			logger.String("reason_header", reasonHeader),
			logger.String("warning_header", warningHeader))
		return nil, &registrarAttemptError{pcscf: cfg.PCSCFAddr, statusCode: finalRes.StatusCode, reason: finalRes.Reason}
	}

	retained := transport.ReleaseConn()
	retainedSecureConn, ok := retained.(*ipsec3gpp.SecureChannelConn)
	if !ok || retainedSecureConn == nil {
		if retained != nil {
			_ = retained.Close()
		}
		return nil, fmt.Errorf("authenticated REGISTER connection ownership was not retained")
	}
	state.secureConn = retainedSecureConn
	return finalizeRegisterSuccess(cfg, *state, finalRes)
}

// prepareProtectedRegisterRequest gives the authenticated REGISTER a fresh
// transaction branch while preserving its already-incremented CSeq and the
// Call-ID established by the initial REGISTER. The Via address must advertise
// UE port-c; Contact independently advertises UE port-s.
func prepareProtectedRegisterRequest(cfg Config, state registerState, req *sip.Request) error {
	if req == nil {
		return fmt.Errorf("missing authenticated REGISTER request")
	}
	localPort := state.ipsecPolicy.LocalPortC
	if localPort <= 0 {
		localPort = state.portC
	}
	if localPort <= 0 {
		return fmt.Errorf("authenticated REGISTER missing protected port-c")
	}
	req.RemoveHeader("Via")
	viaHost := formatRegisterViaHost(cfg.LocalIP, localPort)
	via := fmt.Sprintf("SIP/2.0/TCP %s;branch=%s", viaHost, sip.GenerateBranchN(16))
	req.PrependHeader(sip.NewHeader("Via", via))
	return nil
}

func logProtectedRegisterShape(cfg Config) sipgo.ClientRequestOption {
	return func(_ *sipgo.Client, req *sip.Request) error {
		if req == nil {
			return nil
		}
		logger.Info("IMS protected REGISTER wire shape",
			logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
			logger.String("via", joinedSIPHeaderValues(req, "Via")),
			logger.String("contact", joinedSIPHeaderValues(req, "Contact")),
			logger.String("route", joinedSIPHeaderValues(req, "Route")),
			logger.String("security_client", joinedSIPHeaderValues(req, "Security-Client")),
			logger.String("security_verify", joinedSIPHeaderValues(req, "Security-Verify")),
			logger.String("require", joinedSIPHeaderValues(req, "Require")),
			logger.String("proxy_require", joinedSIPHeaderValues(req, "Proxy-Require")),
			logger.String("supported", joinedSIPHeaderValues(req, "Supported")),
			logger.String("cseq", joinedSIPHeaderValues(req, "CSeq")),
			logger.Bool("authorization", req.GetHeader("Authorization") != nil),
			logger.Bool("proxy_authorization", req.GetHeader("Proxy-Authorization") != nil))
		return nil
	}
}

func joinedSIPHeaderValues(req *sip.Request, name string) string {
	if req == nil {
		return ""
	}
	values := make([]string, 0, len(req.GetHeaders(name)))
	for _, header := range req.GetHeaders(name) {
		if header != nil {
			values = append(values, strings.TrimSpace(header.Value()))
		}
	}
	return strings.Join(values, " || ")
}

func installIPSecFromChallenge(cfg Config, state *registerState, res *sip.Response) error {
	secServer := res.GetHeader("Security-Server")
	if secServer == nil {
		return fmt.Errorf("missing Security-Server on %d", res.StatusCode)
	}
	verify, selected, err := buildSecurityVerifyFromChallenge(cfg, res)
	if err != nil {
		return err
	}
	state.selectedOffer = selected
	state.verifyHeader = verify

	rip := effectiveIPSecRemoteIP(cfg)
	if rip == nil {
		return fmt.Errorf("invalid IPSec remote for registrar %q transport %q", cfg.PCSCFAddr, effectiveTransportAddr(cfg))
	}

	serverMech := ipsec3gpp.SecurityMechanism{
		Alg:   selected.Alg,
		EAlg:  selected.EAlg,
		Prot:  selected.Prot,
		Mode:  selected.Mode,
		SPIc:  selected.SPIC,
		SPIs:  selected.SPIS,
		PortC: selected.PortC,
		PortS: selected.PortS,
	}
	clientMech := ipsec3gpp.SecurityMechanism{
		Alg:   selected.Alg,
		EAlg:  selected.EAlg,
		Prot:  selected.Prot,
		Mode:  selected.Mode,
		SPIc:  state.spiC,
		SPIs:  state.spiS,
		PortC: state.portC,
		PortS: state.portS,
	}
	pol, err := ipsec3gpp.NewPolicy(ipsec3gpp.PolicyInput{
		LocalIP:    cfg.LocalIP,
		RemoteIP:   rip,
		ClientMech: clientMech,
		ServerMech: serverMech,
		CK:         state.ck,
		IK:         state.ik,
	})
	if err != nil {
		return err
	}
	state.portC = pol.LocalPortC
	state.portS = pol.LocalPortS
	transport, err := ipsec3gpp.NewTransport(pol)
	if err != nil {
		return err
	}
	state.ipsecPolicy = pol
	state.transport = transport
	if cfg.IPSecInstaller == nil {
		return fmt.Errorf("userspace IMS ipsec installer unavailable")
	}
	if err := cfg.IPSecInstaller.InstallIPSec3GPP(pol, transport); err != nil {
		return fmt.Errorf("install userspace IMS ESP policy: %w", err)
	}
	return nil
}

func dialSecureRegisterConn(ctx context.Context, cfg Config, swuTCP voiceclient.SWUTCPDialer, state registerState) (*ipsec3gpp.SecureChannelConn, error) {
	transportAddr := effectiveIPSecGatewayAddr(cfg)
	remoteIP, remotePortStr, err := net.SplitHostPort(transportAddr)
	if err != nil {
		return nil, err
	}
	remotePort, err := strconv.Atoi(remotePortStr)
	if err != nil {
		return nil, err
	}
	rip := net.ParseIP(remoteIP)
	if rip == nil {
		return nil, fmt.Errorf("invalid transport P-CSCF %q", transportAddr)
	}
	portS := state.ipsecPolicy.RemotePortS
	if portS <= 0 && state.selectedOffer != nil {
		portS = state.selectedOffer.PortS
	}
	if portS <= 0 {
		portS = remotePort
	}
	localPort := state.ipsecPolicy.LocalPortC
	if localPort <= 0 {
		localPort = state.portC
	}

	var rawConn net.Conn
	if swuTCP != nil {
		rawConn, err = swuTCP.DialContextTCP(ctx, cfg.LocalIP, localPort, rip, portS)
	} else {
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: cfg.LocalIP, Port: localPort}}
		rawConn, err = d.DialContext(ctx, "tcp", net.JoinHostPort(rip.String(), strconv.Itoa(portS)))
	}
	if err != nil {
		return nil, err
	}
	return ipsec3gpp.WrapSecureChannel(rawConn, state.transport, state.ipsecPolicy), nil
}

func buildAuthenticatedRegister(cfg Config, state registerState, prevReq *sip.Request, prevRes *sip.Response) (*sip.Request, sim.AKAResult, *voiceclient.DigestAuthSession, error) {
	if prevReq == nil {
		return nil, sim.AKAResult{}, nil, fmt.Errorf("missing previous REGISTER request")
	}
	chal, err := selectDigestChallenge(cfg, prevRes)
	if err != nil {
		return nil, sim.AKAResult{}, nil, err
	}
	akaResult, authHeader, err := computeAKAAuth(cfg, chal, prevReq)
	if err != nil {
		return nil, sim.AKAResult{}, nil, err
	}

	req := prevReq.Clone()
	req.RemoveHeader("Via")
	req.RemoveHeader("Authorization")
	req.RemoveHeader("Security-Verify")
	// The authenticated REGISTER is a new transaction and MUST advance CSeq.
	// These requests use generic headers, so replace the serialized value
	// explicitly instead of mutating sipgo's lazily parsed CSeq cache.
	if cseq := req.CSeq(); cseq != nil {
		next := cseq.SeqNo + 1
		req.RemoveHeader("CSeq")
		req.AppendHeader(sip.NewHeader("CSeq", fmt.Sprintf("%d REGISTER", next)))
	}
	req.AppendHeader(sip.NewHeader("Authorization", authHeader))
	if state.verifyHeader != "" {
		req.AppendHeader(sip.NewHeader("Security-Verify", state.verifyHeader))
	}
	return req, akaResult, newDigestAuthSession(chal, akaResult.RES, cfg.PrivateID, prevRes), nil
}

func buildRegisterRequest(cfg Config, state registerState, initial bool, variant initialRegisterVariant, localPorts ...int) (*sip.Request, error) {
	profile := variant.profile
	if strings.TrimSpace(profile.ContactFeatures) == "" {
		profile = effectiveRegisterProfile(cfg)
	}
	recipient := sip.Uri{}
	rawURI := "sip:" + strings.TrimSpace(cfg.HomeDomain)
	if err := sip.ParseUri(rawURI, &recipient); err != nil {
		return nil, err
	}
	req := sip.NewRequest(sip.REGISTER, recipient)
	localPort := registerSIPLocalPort(cfg)
	if len(localPorts) > 0 && localPorts[0] > 0 {
		localPort = localPorts[0]
	}
	req.AppendHeader(sip.NewHeader("From", "<"+cfg.PublicURI+">;tag="+sip.GenerateTagN(16)))
	req.AppendHeader(sip.NewHeader("To", "<"+cfg.PublicURI+">"))
	req.AppendHeader(sip.NewHeader("Contact", buildProfileContact(cfg, state, localPort, profile)))
	if initial {
		if auth := buildInitialAuthorization(cfg, variant.initialAuth); auth != "" {
			req.AppendHeader(sip.NewHeader("Authorization", auth))
		}
	}
	if profile.IncludeRoute {
		req.AppendHeader(sip.NewHeader("Route", "<sip:"+effectiveRouteAddr(cfg)+";lr>"))
	}
	expires := profileRegisterExpires(cfg, profile)
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))
	if profile.IncludeRequireSecAgree {
		req.AppendHeader(sip.NewHeader("Require", "sec-agree"))
		req.AppendHeader(sip.NewHeader("Proxy-Require", "sec-agree"))
	}
	supported := strings.TrimSpace(profile.SupportedHeader)
	if supported == "" {
		supported = "path,sec-agree,gruu"
	}
	req.AppendHeader(sip.NewHeader("Supported", supported))
	req.AppendHeader(sip.NewHeader("Allow", "INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS"))
	if profile.IncludePPreferredID {
		req.AppendHeader(sip.NewHeader("P-Preferred-Identity", "<"+cfg.PublicURI+">"))
	}
	if profile.IncludePVisitedNetworkID {
		req.AppendHeader(sip.NewHeader("P-Visited-Network-ID", "\""+cfg.HomeDomain+"\""))
	}
	if variant.includePANI {
		pani := "IEEE-802.11;i-wlan-node-id=000000000000"
		if profile.IncludePANIAuthenticated {
			pani += ";network-provided"
		}
		req.AppendHeader(sip.NewHeader("P-Access-Network-Info", pani))
	}
	if variant.includeCellular {
		req.AppendHeader(sip.NewHeader("Cellular-Network-Info", buildCellularNetworkInfo(cfg)))
	}
	if profile.IncludeAcceptContact {
		req.AppendHeader(sip.NewHeader("Accept-Contact", "*;+g.3gpp.smsip"))
		req.AppendHeader(sip.NewHeader("Accept-Contact", "*;+g.3gpp.icsi-ref=\""+policy.IMSMmtelICSIRef+"\""))
	}
	if profile.IncludeSecurityClient {
		secClient := voiceclient.BuildSecurityClientHeader(profile, state.spiC, state.spiS, state.portC, state.portS)
		if strings.EqualFold(strings.TrimSpace(profile.SecurityClientFormat), "template_preferred") {
			secClient = buildTemplateSecurityClient(cfg.Template, state.spiC, state.spiS, state.portC, state.portS)
		}
		req.AppendHeader(sip.NewHeader("Security-Client", secClient))
	}
	req.AppendHeader(sip.NewHeader("User-Agent", profile.UserAgent))
	req.SetDestination(effectiveTransportAddr(cfg))
	req.SetTransport("TCP")
	logRegisterRouting(cfg, req)
	return req, nil
}

func applyInitialRegisterVariantConfig(cfg Config, variant initialRegisterVariant) Config {
	out := cfg
	out.RegisterProfile = variant.profile
	if ua := strings.TrimSpace(variant.profile.UserAgent); ua != "" {
		out.UserAgent = ua
	}
	shape := strings.ToLower(strings.TrimSpace(variant.profile.AuthorizationIdentity))
	if shape == "eap_private_id" {
		if privateID := strings.TrimSpace(out.EAPPrivateID); privateID != "" {
			out.PrivateID = privateID
		}
		return out
	}
	if shape == "" || shape == "private_id" {
		return out
	}
	privateID, publicURI := voiceclient.BuildIMSIdentity(out.IMSI, out.Realm, out.HomeDomain, shape)
	if privateID != "" && publicURI != "" {
		out.PrivateID = privateID
		out.PublicURI = publicURI
	}
	return out
}

func profileRegisterExpires(cfg Config, profile voiceclient.RegisterProfile) int {
	if profile.RegisterExpirySeconds > 0 {
		return profile.RegisterExpirySeconds
	}
	if cfg.RegisterExpirySeconds > 0 {
		return cfg.RegisterExpirySeconds
	}
	return 3600
}

func buildProfileContact(cfg Config, state registerState, localPort int, profile voiceclient.RegisterProfile) string {
	switch strings.ToLower(strings.TrimSpace(profile.ContactFeatures)) {
	case "phone_xiaomi":
		return buildHandsetContact(cfg, state, localPort, profile, true)
	case "sms_only":
		return buildHandsetContact(cfg, state, localPort, profile, false)
	default:
		return buildIMSCoreContact(cfg, state, localPort)
	}
}

func buildHandsetContact(cfg Config, state registerState, localPort int, profile voiceclient.RegisterProfile, xiaomi bool) string {
	user := strings.TrimSpace(cfg.IMSI)
	if xiaomi && strings.TrimSpace(state.contactUser) != "" {
		user = strings.TrimSpace(state.contactUser)
	}
	if user == "" {
		user = "anonymous"
	}
	local := fmt.Sprintf("sip:%s@%s;transport=tcp", user, net.JoinHostPort(cfg.LocalIP.String(), strconv.Itoa(localPort)))
	var features string
	if xiaomi {
		features = `;+g.3gpp.accesstype="wlan1";audio;+g.3gpp.icsi-ref="` + policy.IMSMmtelICSIRef + `"`
		if urn := strings.TrimSpace(state.sipInstance); urn != "" {
			features += `;+sip.instance="<` + urn + `>"`
		}
	} else {
		features = `;+g.3gpp.accesstype="IEEE-802.11";+g.3gpp.smsip`
	}
	return "<" + local + ">" + features + ";expires=" + strconv.Itoa(profileRegisterExpires(cfg, profile))
}

func finalizeRegisterSuccess(cfg Config, state registerState, res *sip.Response) (*registerResult, error) {
	expires := 3600
	if h := res.GetHeader("Expires"); h != nil {
		if v, err := strconv.Atoi(strings.TrimSpace(h.Value())); err == nil && v > 0 {
			expires = v
		}
	}
	logger.Info(fmt.Sprintf("[%s] IMS REGISTER 成功", strings.TrimSpace(cfg.DeviceID)),
		logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
		logger.Int("code", res.StatusCode),
		logger.Int("expires_seconds", expires),
		logger.String("sip_security_mode", securityModeLabel(state.secureConn != nil)),
		logger.String("verify", state.verifyHeader))
	serviceRoutes := make([]string, 0, len(res.GetHeaders("Service-Route")))
	for _, header := range res.GetHeaders("Service-Route") {
		if header != nil && strings.TrimSpace(header.Value()) != "" {
			serviceRoutes = append(serviceRoutes, strings.TrimSpace(header.Value()))
		}
	}
	responseContactCount := len(res.GetHeaders("Contact"))
	responseSMSIPBinding := false
	for _, header := range res.GetHeaders("Contact") {
		if header != nil && strings.Contains(strings.ToLower(header.Value()), "+g.3gpp.smsip") {
			responseSMSIPBinding = true
			break
		}
	}
	logger.Info("IMS REGISTER service routing received",
		logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
		logger.Int("service_route_count", len(serviceRoutes)),
		logger.Int("response_contact_count", responseContactCount),
		logger.Bool("response_smsip_binding", responseSMSIPBinding))
	associated := voiceclient.AssociatedPublicIdentities(res)
	phoneNumber := voiceclient.AssociatedPhoneNumber(associated)
	preferredIdentity := ""
	preferredIdentityScheme := ""
	if len(associated) > 0 {
		preferredIdentity = associated[0]
		var preferredURI sip.Uri
		if sip.ParseUri(preferredIdentity, &preferredURI) == nil {
			preferredIdentityScheme = strings.ToLower(strings.TrimSpace(preferredURI.Scheme))
		}
	}
	logger.Info("IMS REGISTER associated identities received",
		logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
		logger.Int("associated_identity_count", len(associated)),
		logger.String("preferred_identity_scheme", preferredIdentityScheme),
		logger.Bool("preferred_identity_selected", preferredIdentity != ""))
	messageAuth := cloneDigestAuthSession(state.messageAuth)
	nextNonce := nextNonceFromAuthenticationInfo(res)
	if messageAuth != nil && nextNonce != "" {
		messageAuth.Challenge.Nonce = nextNonce
	}
	logger.Info("IMS REGISTER digest session retained",
		logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
		logger.Bool("authentication_info", len(res.GetHeaders("Authentication-Info")) > 0),
		logger.Bool("nextnonce", nextNonce != ""),
		logger.Bool("message_auth", messageAuth != nil))
	return &registerResult{
		pcscfAddr:          cfg.PCSCFAddr,
		transportPCSCFAddr: effectiveTransportAddr(cfg),
		serviceRoutes:      serviceRoutes,
		preferredIdentity:  preferredIdentity,
		phoneNumber:        phoneNumber,
		expiresSeconds:     expires,
		verifyHeader:       state.verifyHeader,
		secureConn:         state.secureConn,
		ipsecPolicy:        state.ipsecPolicy,
		transport:          state.transport,
		messageAuth:        messageAuth,
	}, nil
}

func newDigestAuthSession(chal *digest.Challenge, akaResponse []byte, username string, res *sip.Response) *voiceclient.DigestAuthSession {
	if chal == nil || len(akaResponse) == 0 {
		return nil
	}
	challenge := *chal
	challenge.Domain = append([]string(nil), chal.Domain...)
	challenge.QOP = append([]string(nil), chal.QOP...)
	return &voiceclient.DigestAuthSession{
		Challenge:   challenge,
		AKAResponse: append([]byte(nil), akaResponse...),
		Username:    strings.TrimSpace(username),
		Proxy:       res != nil && len(res.GetHeaders("WWW-Authenticate")) == 0 && len(res.GetHeaders("Proxy-Authenticate")) > 0,
	}
}

func nextNonceFromAuthenticationInfo(res *sip.Response) string {
	if res == nil {
		return ""
	}
	for _, header := range res.GetHeaders("Authentication-Info") {
		if header == nil {
			continue
		}
		if nonce := digestInfoParam(header.Value(), "nextnonce"); nonce != "" {
			return nonce
		}
	}
	return ""
}

func digestInfoParam(value, key string) string {
	lower := strings.ToLower(value)
	key = strings.ToLower(strings.TrimSpace(key))
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], key)
		if idx < 0 {
			return ""
		}
		idx += offset
		beforeOK := idx == 0 || lower[idx-1] == ',' || lower[idx-1] == ' ' || lower[idx-1] == '\t'
		pos := idx + len(key)
		for pos < len(value) && (value[pos] == ' ' || value[pos] == '\t') {
			pos++
		}
		if !beforeOK || pos >= len(value) || value[pos] != '=' {
			offset = idx + len(key)
			continue
		}
		pos++
		for pos < len(value) && (value[pos] == ' ' || value[pos] == '\t') {
			pos++
		}
		if pos >= len(value) {
			return ""
		}
		if value[pos] == '"' {
			pos++
			start := pos
			for pos < len(value) && value[pos] != '"' {
				pos++
			}
			return strings.TrimSpace(value[start:pos])
		}
		start := pos
		for pos < len(value) && value[pos] != ',' && value[pos] != ' ' && value[pos] != '\t' {
			pos++
		}
		return strings.TrimSpace(value[start:pos])
	}
	return ""
}

func cloneDigestAuthSession(auth *voiceclient.DigestAuthSession) *voiceclient.DigestAuthSession {
	if auth == nil {
		return nil
	}
	clone := *auth
	clone.Challenge.Domain = append([]string(nil), auth.Challenge.Domain...)
	clone.Challenge.QOP = append([]string(nil), auth.Challenge.QOP...)
	clone.AKAResponse = append([]byte(nil), auth.AKAResponse...)
	return &clone
}

func doRegisterTransaction(ctx context.Context, client *sipgo.Client, req *sip.Request, opts ...sipgo.ClientRequestOption) (*sip.Response, error) {
	txCtx, cancel := context.WithTimeout(ctx, registerTransactionTimeout)
	defer cancel()
	tx, err := client.TransactionRequest(txCtx, req, opts...)
	if err != nil {
		return nil, err
	}
	defer tx.Terminate()
	select {
	case <-tx.Done():
		if err := tx.Err(); err != nil {
			return nil, fmt.Errorf("transaction ended: %w", err)
		}
		return nil, fmt.Errorf("transaction ended without a response")
	case res := <-tx.Responses():
		return res, nil
	case <-txCtx.Done():
		return nil, txCtx.Err()
	}
}

func buildInitialAuthorization(cfg Config, mode string) string {
	authMode := strings.ToLower(strings.TrimSpace(mode))
	if authMode == "" {
		if strings.EqualFold(strings.TrimSpace(cfg.Template.SecAgreeMode), "auto") {
			authMode = "aka_empty_uri_first"
		} else if !cfg.Template.UsePlainDigestPlaceholder {
			authMode = "none"
		} else {
			authMode = "aka_empty_uri_first"
		}
	}
	requestURI := "sip:" + strings.TrimSpace(cfg.HomeDomain)
	username := authorizationUsername(cfg)
	realm := quoteSipParam(strings.TrimSpace(cfg.Realm))
	switch authMode {
	case "none":
		return ""
	case "aka_empty":
		return fmt.Sprintf(
			`Digest username="%s",realm="%s",nonce="",uri="%s",response="",algorithm=AKAv1-MD5`,
			quoteSipParam(username),
			realm,
			quoteSipParam(requestURI),
		)
	case "aka_zero_response_uri_first":
		return fmt.Sprintf(
			`Digest uri="%s",username="%s",algorithm=AKAv1-MD5,response="00000000000000000000000000000000",realm="%s",nonce=""`,
			quoteSipParam(requestURI),
			quoteSipParam(username),
			realm,
		)
	default:
		return fmt.Sprintf(
			`Digest uri="%s",username="%s",algorithm=AKAv1-MD5,response="",realm="%s",nonce=""`,
			quoteSipParam(requestURI),
			quoteSipParam(username),
			realm,
		)
	}
}

func authorizationUsername(cfg Config) string {
	if v := strings.TrimSpace(cfg.PrivateID); v != "" {
		return v
	}
	imsi := strings.TrimSpace(cfg.IMSI)
	realm := strings.TrimSpace(cfg.Realm)
	if imsi != "" && realm != "" {
		if privateID, _ := voiceclient.BuildIMSIdentity(imsi, realm, strings.TrimSpace(cfg.HomeDomain), "imsi_home_domain"); privateID != "" {
			return privateID
		}
	}
	return ""
}

func buildIMSCoreContact(cfg Config, state registerState, localPort int) string {
	sipInstance := strings.TrimSpace(state.sipInstance)
	if sipInstance == "" {
		sipInstance = strings.TrimSpace(cfg.SIPInstanceURN)
	}
	if sipInstance == "" {
		sipInstance = voiceclient.NewSIPInstanceURN()
	}
	return policy.BuildIMSContactHeader(cfg.Template, policy.ContactBuildInput{
		IMSI:               cfg.IMSI,
		PublicURI:          cfg.PublicURI,
		LocalIP:            cfg.LocalIP,
		LocalPort:          localPort,
		SIPInstanceURN:     sipInstance,
		RegisterExpirySecs: cfg.RegisterExpirySeconds,
	})
}

func buildCellularNetworkInfo(cfg Config) string {
	plmn := strings.TrimSpace(cfg.MCC) + strings.TrimLeft(strings.TrimSpace(cfg.MNC), "0")
	if plmn == "" {
		plmn = "00000"
	}
	cell := strings.TrimSpace(cfg.CellID)
	if cell == "" {
		cell = "0000000"
	}
	return fmt.Sprintf("3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=%s%s;cell-info-age=0", plmn, cell)
}

func computeAKAAuth(cfg Config, chal *digest.Challenge, req *sip.Request) (sim.AKAResult, string, error) {
	digestURI := req.Recipient.String()
	result, err := simauth.ComputeDigest(cfg.AKA, chal, digest.Options{
		Method:   req.Method.String(),
		URI:      digestURI,
		Username: cfg.PrivateID,
	})
	if err != nil {
		logger.Warn("IMS AKA digest computation failed",
			logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
			logger.String("algorithm", strings.TrimSpace(chal.Algorithm)),
			logger.String("qop", strings.Join(chal.QOP, ",")),
			logger.Int("nonce_encoded_len", len(chal.Nonce)),
			logger.String("digest_uri", digestURI),
			logger.String("authorization_identity", cfg.PrivateID),
			logger.String("error", err.Error()))
		return sim.AKAResult{}, "", err
	}
	logger.Info("IMS AKA digest computed",
		logger.String("trace_id", strings.TrimSpace(cfg.TraceID)),
		logger.String("algorithm", strings.TrimSpace(chal.Algorithm)),
		logger.String("qop", strings.Join(chal.QOP, ",")),
		logger.Int("nonce_encoded_len", len(chal.Nonce)),
		logger.String("digest_uri", digestURI),
		logger.String("authorization_identity", cfg.PrivateID),
		logger.Int("res_len", len(result.AKAResult.RES)),
		logger.Int("ck_len", len(result.AKAResult.CK)),
		logger.Int("ik_len", len(result.AKAResult.IK)),
		logger.Bool("auts", len(result.AKAResult.AUTS) > 0))
	return result.AKAResult, result.Header, nil
}

func selectDigestChallenge(cfg Config, res *sip.Response) (*digest.Challenge, error) {
	headers := res.GetHeaders("WWW-Authenticate")
	if len(headers) == 0 && res.StatusCode == sip.StatusProxyAuthRequired {
		headers = res.GetHeaders("Proxy-Authenticate")
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("%d response with no authenticate header", res.StatusCode)
	}
	for _, header := range headers {
		chal, err := digest.ParseChallenge(header.Value())
		if err == nil {
			return chal, nil
		}
	}
	return nil, fmt.Errorf("parse challenge failed")
}

func quoteSipParam(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func registerSIPLocalPort(cfg Config) int {
	return registerAttemptLocalPort(cfg, 0)
}

func registerAttemptLocalPort(cfg Config, attemptIndex int) int {
	if attemptIndex > 0 || !registrarHostEqualsLocalIP(cfg.PCSCFAddr, cfg.LocalIP) {
		return randomEphemeralSIPPort()
	}
	return 5060
}

func randomEphemeralSIPPort() int {
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(50000))
		if err != nil {
			return 5062
		}
		port := 10000 + int(n.Int64())
		if port != 5060 && port != 5061 {
			return port
		}
	}
}

func randomNonZeroUint32() uint32 {
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(1<<32-1))
		if err != nil {
			return 0xc0ffee01
		}
		if v := uint32(n.Int64()) + 1; v != 0 {
			return v
		}
	}
}
