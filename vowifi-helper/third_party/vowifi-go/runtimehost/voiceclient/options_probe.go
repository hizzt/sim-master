package voiceclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
)

// SIPOptionsProbeRequest describes a side-effect-free service-routing probe.
// TargetURI must be an explicit SIP, SIPS, or TEL URI so diagnostics compare
// the exact PSI form sent on the wire.
type SIPOptionsProbeRequest struct {
	TargetURI               string `json:"target_uri"`
	LocationHeaderMode      string `json:"location_header_mode,omitempty"`
	RouteMode               string `json:"route_mode,omitempty"`
	PreemptiveAuthorization bool   `json:"preemptive_authorization,omitempty"`
	PayloadProfile          string `json:"payload_profile,omitempty"`
	PreferredIdentityMode   string `json:"preferred_identity_mode,omitempty"`
	SMSServiceHeaders       bool   `json:"sms_service_headers,omitempty"`
	PreemptiveAuthMode      string `json:"preemptive_authorization_mode,omitempty"`
	OmitTransferEncoding    bool   `json:"omit_transfer_encoding,omitempty"`
	StrictMinimalHeaders    bool   `json:"strict_minimal_headers,omitempty"`
	RegisteredFlowRecovery  bool   `json:"registered_flow_recovery,omitempty"`
	PAccessNetworkInfoValue string `json:"p_access_network_info_value,omitempty"`
}

// SIPOptionsProbeResult intentionally exposes protocol shape only. It omits
// public/private identities, digest values, nonces, and complete server names.
type SIPOptionsProbeResult struct {
	RequestMethod              string `json:"request_method"`
	TargetScheme               string `json:"target_scheme"`
	TargetHostPresent          bool   `json:"target_host_present"`
	LocationHeaderMode         string `json:"location_header_mode"`
	PAccessNetworkInfoPresent  bool   `json:"p_access_network_info_present"`
	CellularNetworkInfoPresent bool   `json:"cellular_network_info_present"`
	PreferredIdentityScheme    string `json:"preferred_identity_scheme,omitempty"`
	PreferredIdentityMode      string `json:"preferred_identity_mode,omitempty"`
	PPreferredServicePresent   bool   `json:"p_preferred_service_present"`
	AcceptContactPresent       bool   `json:"accept_contact_present"`
	PreemptiveAuthMode         string `json:"preemptive_authorization_mode,omitempty"`
	AuthorizationHeader        string `json:"authorization_header,omitempty"`
	TransferEncodingPresent    bool   `json:"transfer_encoding_present"`
	StrictMinimalHeaders       bool   `json:"strict_minimal_headers,omitempty"`
	RegisteredFlowRecovery     bool   `json:"registered_flow_recovery,omitempty"`
	RequestContactPresent      bool   `json:"request_contact_present"`
	RequestAllowPresent        bool   `json:"request_allow_present"`
	RequestAcceptPresent       bool   `json:"request_accept_present"`
	RequestUserAgentPresent    bool   `json:"request_user_agent_present"`
	RequestSecurityVerify      bool   `json:"request_security_verify_present"`
	RouteMode                  string `json:"route_mode,omitempty"`
	RouteCount                 int    `json:"route_count"`
	ConfiguredServiceRoutes    int    `json:"configured_service_route_count"`
	AuthorizationPresent       bool   `json:"authorization_present"`
	ContentType                string `json:"content_type,omitempty"`
	BodyBytes                  int    `json:"body_bytes"`
	PayloadProfile             string `json:"payload_profile,omitempty"`
	ResponseCode               int    `json:"response_code,omitempty"`
	ResponseReason             string `json:"response_reason,omitempty"`
	ResponseSource             string `json:"response_source,omitempty"`
	ResponseHeaderCount        int    `json:"response_header_count"`
	ResponseViaCount           int    `json:"response_via_count"`
	ServerPresent              bool   `json:"server_present"`
	ServerSHA256               string `json:"server_sha256,omitempty"`
	UserAgentPresent           bool   `json:"user_agent_present"`
	UserAgentSHA256            string `json:"user_agent_sha256,omitempty"`
	Warning                    string `json:"warning,omitempty"`
	ReasonHeader               string `json:"reason_header,omitempty"`
	PAssertedService           string `json:"p_asserted_service,omitempty"`
	WWWAuthenticateCount       int    `json:"www_authenticate_count"`
	ProxyAuthenticateCount     int    `json:"proxy_authenticate_count"`
	TransportError             bool   `json:"transport_error"`
}

// SIPOptionsProber is implemented by registered IMS services that can send a
// diagnostic OPTIONS request over their existing signaling flow.
type SIPOptionsProber interface {
	ProbeSIPOptions(context.Context, SIPOptionsProbeRequest) (SIPOptionsProbeResult, error)
}

// SIPEmptyMessageProber sends an empty 3GPP SMS MESSAGE to test method-level
// routing and authorization without forming an RP-DATA SMS.
type SIPEmptyMessageProber interface {
	ProbeSIPEmptyMessage(context.Context, SIPOptionsProbeRequest) (SIPOptionsProbeResult, error)
}

func (c *Client) ProbeSIPOptions(ctx context.Context, input SIPOptionsProbeRequest) (SIPOptionsProbeResult, error) {
	target := strings.TrimSpace(input.TargetURI)
	if target == "" {
		return SIPOptionsProbeResult{}, errors.New("voiceclient: OPTIONS target_uri is required")
	}
	lowerTarget := strings.ToLower(target)
	if !strings.HasPrefix(lowerTarget, "sip:") && !strings.HasPrefix(lowerTarget, "sips:") && !strings.HasPrefix(lowerTarget, "tel:") {
		return SIPOptionsProbeResult{}, errors.New("voiceclient: OPTIONS target_uri must use sip, sips, or tel")
	}
	mode, err := c.normalizeProbeLocationMode(input.LocationHeaderMode)
	if err != nil {
		return SIPOptionsProbeResult{}, err
	}
	recipient := sip.Uri{}
	if err := sip.ParseUri(target, &recipient); err != nil {
		return SIPOptionsProbeResult{}, fmt.Errorf("voiceclient: parse OPTIONS target URI: %w", err)
	}

	req := sip.NewRequest(sip.OPTIONS, recipient)
	req.AppendHeader(sip.NewHeader("From", "<"+c.cfg.PublicURI+">;tag="+sip.GenerateTagN(16)))
	req.AppendHeader(sip.NewHeader("To", "<"+target+">"))
	req.AppendHeader(sip.NewHeader("Contact", "<"+c.cfg.contactURI()+">"))
	req.AppendHeader(sip.NewHeader("Call-ID", uuid.NewString()))
	req.AppendHeader(sip.NewHeader("CSeq", "1 OPTIONS"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Accept", "application/sdp, application/vnd.3gpp.sms"))
	req.AppendHeader(sip.NewHeader("P-Preferred-Identity", "<"+c.cfg.PublicURI+">"))
	if securityVerify := strings.TrimSpace(c.cfg.SecurityVerify); securityVerify != "" {
		req.AppendHeader(sip.NewHeader("Security-Verify", securityVerify))
	}
	c.applyServiceLocationHeadersForMode(req, mode)
	if userAgent := strings.TrimSpace(c.registerProfile.UserAgent); userAgent != "" {
		req.AppendHeader(sip.NewHeader("User-Agent", userAgent))
	}
	routeMode, err := c.applyProbeRoutes(req, input.RouteMode)
	if err != nil {
		return SIPOptionsProbeResult{}, err
	}
	req.SetDestination(c.cfg.pcscfDestination())
	if c.cfg.transportNetwork() == "udp" {
		req.SetTransport("UDP")
	} else {
		req.SetTransport("TCP")
	}
	identityMode, err := c.applyProbePreferredIdentity(req, input.PreferredIdentityMode)
	if err != nil {
		return SIPOptionsProbeResult{}, err
	}
	if input.SMSServiceHeaders {
		applySMSServiceHeaders(req)
	}
	authMode, err := c.applyProbeAuthorization(req, input.PreemptiveAuthorization, input.PreemptiveAuthMode)
	if err != nil {
		return SIPOptionsProbeResult{}, fmt.Errorf("voiceclient: OPTIONS authorization: %w", err)
	}

	res, transactionErr := c.doTransaction(ctx, req)
	result := inspectSIPOptionsProbe(req, res, transactionErr, mode)
	result.RouteMode = routeMode
	result.ConfiguredServiceRoutes = c.configuredServiceRouteCount()
	result.PreferredIdentityMode = identityMode
	result.PreemptiveAuthMode = authMode
	if transactionErr != nil {
		return result, transactionErr
	}
	return result, nil
}

func (c *Client) ProbeSIPEmptyMessage(ctx context.Context, input SIPOptionsProbeRequest) (SIPOptionsProbeResult, error) {
	target := strings.TrimSpace(input.TargetURI)
	if target == "" {
		return SIPOptionsProbeResult{}, errors.New("voiceclient: MESSAGE target_uri is required")
	}
	lowerTarget := strings.ToLower(target)
	if !strings.HasPrefix(lowerTarget, "sip:") && !strings.HasPrefix(lowerTarget, "sips:") && !strings.HasPrefix(lowerTarget, "tel:") {
		return SIPOptionsProbeResult{}, errors.New("voiceclient: MESSAGE target_uri must use sip, sips, or tel")
	}
	mode, err := c.normalizeProbeLocationMode(input.LocationHeaderMode)
	if err != nil {
		return SIPOptionsProbeResult{}, err
	}
	req, err := c.newSMSRequest(target)
	if err != nil {
		return SIPOptionsProbeResult{}, err
	}
	routeMode, err := c.applyProbeRoutes(req, input.RouteMode)
	if err != nil {
		return SIPOptionsProbeResult{}, err
	}
	c.applyServiceLocationHeadersForMode(req, mode)
	body, payloadProfile, err := c.safeProbeMessageBody(input.PayloadProfile)
	if err != nil {
		return SIPOptionsProbeResult{}, err
	}
	req.AppendHeader(sip.NewHeader("Content-Type", smsContentType))
	if !input.OmitTransferEncoding {
		req.AppendHeader(sip.NewHeader("Content-Transfer-Encoding", "binary"))
	}
	req.SetBody(body)
	identityMode, err := c.applyProbePreferredIdentity(req, input.PreferredIdentityMode)
	if err != nil {
		return SIPOptionsProbeResult{}, err
	}
	if input.SMSServiceHeaders {
		applySMSServiceHeaders(req)
	} else {
		// newSMSRequest applies the production defaults. A diagnostic probe
		// must remove them explicitly when comparing the TS 24.341 minimal
		// MESSAGE header set; otherwise "false" still tests the enabled case.
		req.RemoveHeader("P-Preferred-Service")
		req.RemoveHeader("Accept-Contact")
	}
	if input.StrictMinimalHeaders {
		// Reproduce the UE -> P-CSCF example in 3GPP TS 24.341 B.5.1.
		// MESSAGE is not dialog-forming, so the example deliberately carries
		// only routing, identity, transaction, and payload headers.  Keep this
		// opt-in for diagnostics until a carrier accepts the wire shape.
		for _, name := range []string{
			"Contact",
			"Allow",
			"Accept",
			"P-Preferred-Service",
			"Accept-Contact",
			"Security-Verify",
			"P-Access-Network-Info",
			"Cellular-Network-Info",
			"User-Agent",
			"Content-Transfer-Encoding",
		} {
			for req.GetHeader(name) != nil {
				req.RemoveHeader(name)
			}
		}
	}
	if pani := strings.TrimSpace(input.PAccessNetworkInfoValue); pani != "" {
		if len(pani) > 256 || strings.ContainsAny(pani, "\r\n") {
			return SIPOptionsProbeResult{}, errors.New("voiceclient: invalid custom P-Access-Network-Info")
		}
		for req.GetHeader("P-Access-Network-Info") != nil {
			req.RemoveHeader("P-Access-Network-Info")
		}
		for req.GetHeader("Cellular-Network-Info") != nil {
			req.RemoveHeader("Cellular-Network-Info")
		}
		req.AppendHeader(sip.NewHeader("P-Access-Network-Info", pani))
		mode = "custom_pani"
	}
	authMode, err := c.applyProbeAuthorization(req, input.PreemptiveAuthorization, input.PreemptiveAuthMode)
	if err != nil {
		return SIPOptionsProbeResult{}, fmt.Errorf("voiceclient: MESSAGE probe authorization: %w", err)
	}

	var res *sip.Response
	var transactionErr error
	if input.RegisteredFlowRecovery {
		// Exercise the same bare-401 recovery used by production SMS while
		// retaining the diagnostic probe's deliberately unroutable RP-DATA.
		// This performs REGISTER plus MESSAGE only; it never creates a call.
		res, transactionErr = c.doSMSSubmitTransaction(ctx, req)
	} else {
		res, transactionErr = c.doTransaction(ctx, req)
	}
	result := inspectSIPOptionsProbe(req, res, transactionErr, mode)
	result.RouteMode = routeMode
	result.ConfiguredServiceRoutes = c.configuredServiceRouteCount()
	result.PayloadProfile = payloadProfile
	result.PreferredIdentityMode = identityMode
	result.PreemptiveAuthMode = authMode
	result.StrictMinimalHeaders = input.StrictMinimalHeaders
	result.RegisteredFlowRecovery = input.RegisteredFlowRecovery
	if transactionErr != nil {
		return result, transactionErr
	}
	return result, nil
}

func (c *Client) applyProbeRoutes(request *sip.Request, requested string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(requested))
	if mode == "" {
		mode = "registered"
	}
	if request == nil {
		return mode, nil
	}
	// sipgo's RemoveHeader removes one matching header at a time. MESSAGEs are
	// born with both the preloaded P-CSCF and learned Service-Route set, so drain
	// the complete list before constructing the diagnostic variant.
	for request.GetHeader("Route") != nil {
		request.RemoveHeader("Route")
	}
	appendPCSCF := func() {
		if pcscf := strings.TrimSpace(c.cfg.PCSCFAddr); pcscf != "" {
			request.AppendHeader(sip.NewHeader("Route", "<sip:"+pcscf+";lr>"))
		}
	}
	appendServiceRoutes := func() {
		for _, route := range c.cfg.ServiceRoutes {
			if route = strings.TrimSpace(route); route != "" {
				request.AppendHeader(sip.NewHeader("Route", route))
			}
		}
	}
	switch mode {
	case "registered":
		appendPCSCF()
		appendServiceRoutes()
	case "pcscf_only":
		appendPCSCF()
	case "service_route_only":
		appendServiceRoutes()
	case "none":
	default:
		return "", fmt.Errorf("voiceclient: unsupported route_mode %q", requested)
	}
	return mode, nil
}

func (c *Client) configuredServiceRouteCount() int {
	count := 0
	for _, route := range c.cfg.ServiceRoutes {
		if strings.TrimSpace(route) != "" {
			count++
		}
	}
	return count
}

func (c *Client) applyProbeAuthorization(request *sip.Request, enabled bool, requested string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(requested))
	if mode == "" {
		if !enabled {
			return "none", nil
		}
		mode = "session"
	}
	if mode == "none" {
		return mode, nil
	}
	auth := c.preemptiveAuthSnapshot()
	if auth == nil || len(auth.AKAResponse) == 0 {
		return mode, nil
	}
	digestURI := request.Recipient.String()
	proxy := auth.Proxy
	switch mode {
	case "session":
	case "proxy":
		proxy = true
	case "home_domain":
		digestURI = "sip:" + strings.TrimSpace(c.cfg.HomeDomain)
	case "proxy_home_domain":
		proxy = true
		digestURI = "sip:" + strings.TrimSpace(c.cfg.HomeDomain)
	default:
		return "", fmt.Errorf("voiceclient: unsupported preemptive_authorization_mode %q", requested)
	}
	if strings.TrimSpace(digestURI) == "sip:" {
		return "", errors.New("voiceclient: MESSAGE probe home domain is empty")
	}
	if err := c.addPreemptiveSMSAuthorizationForSession(request, digestURI, proxy, auth); err != nil {
		return "", err
	}
	return mode, nil
}

func (c *Client) applyProbePreferredIdentity(request *sip.Request, requested string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(requested))
	if mode == "" {
		mode = "sms"
	}
	var identity string
	switch mode {
	case "sms":
		identity = c.preferredSMSPublicIdentity()
	case "associated":
		identity = c.preferredPublicIdentity()
	case "associated_from_sms_ppi":
		// Keep the dialog identity on the network-selected default IMPU while
		// asking the P-CSCF to assert the associated E.164 tel identity. This
		// is the shape TS 24.341 relies on when the IP-SM-GW maps an IMS
		// submission back to the legacy service centre.
		identity = c.preferredPublicIdentity()
		if err := applyPreferredIdentity(request, identity); err != nil {
			return "", err
		}
		request.RemoveHeader("P-Preferred-Identity")
		request.AppendHeader(sip.NewHeader("P-Preferred-Identity", "<"+c.preferredSMSPublicIdentity()+">"))
		return mode, nil
	case "register_aor":
		identity = strings.TrimSpace(c.cfg.PublicURI)
	default:
		return "", fmt.Errorf("voiceclient: unsupported preferred_identity_mode %q", requested)
	}
	if err := applyPreferredIdentity(request, identity); err != nil {
		return "", err
	}
	return mode, nil
}

func (c *Client) safeProbeMessageBody(requested string) ([]byte, string, error) {
	profile := strings.ToLower(strings.TrimSpace(requested))
	switch profile {
	case "", "empty":
		return nil, "empty", nil
	case "rp_empty":
		return []byte{0x00, 0x01, 0x00, 0x00, 0x00}, profile, nil
	case "rp_smsc_only", "rp_invalid_tpdu", "rp_submit_marker", "rp_submit_srr_marker", "rp_submit_minimal", "rp_submit_bad_address", "rp_submit_srr_bad_address", "rp_submit_unroutable":
		rpDestination, err := encodeProbeRPAddress(c.cfg.SMSC)
		if err != nil {
			return nil, "", err
		}
		body := []byte{0x00, 0x01, 0x00}
		body = append(body, rpDestination...)
		if profile == "rp_smsc_only" {
			return append(body, 0x00), profile, nil
		}
		switch profile {
		case "rp_invalid_tpdu":
			return append(body, 0x01, 0x00), profile, nil
		case "rp_submit_marker":
			return append(body, 0x01, 0x01), profile, nil
		case "rp_submit_srr_marker":
			return append(body, 0x01, 0x21), profile, nil
		case "rp_submit_bad_address":
			return append(body, 0x08, 0x01, 0x01, 0x01, 0x91, 0xff, 0x00, 0x00, 0x00), profile, nil
		case "rp_submit_srr_bad_address":
			return append(body, 0x08, 0x21, 0x01, 0x01, 0x91, 0xff, 0x00, 0x00, 0x00), profile, nil
		case "rp_submit_unroutable":
			return append(body, 0x0d, 0x21, 0x01, 0x0b, 0x91, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x00, 0x00, 0x00), profile, nil
		default:
			return append(body, 0x03, 0x01, 0x01, 0x00), profile, nil
		}
	case "rp_submit_self", "rp_submit_self_srr", "rp_submit_self_no_rpda":
		body, err := c.buildSelfProbeRPData(profile)
		if err != nil {
			return nil, "", err
		}
		return body, profile, nil
	default:
		return nil, "", fmt.Errorf("voiceclient: unsupported MESSAGE payload_profile %q", requested)
	}
}

// buildSelfProbeRPData creates one standards-shaped SMS-SUBMIT containing the
// single GSM-7 character "A" and addressed only to the registered local E.164
// identity. It is used for bounded live diagnostics without accepting an
// arbitrary TPDU from the control socket.
func (c *Client) buildSelfProbeRPData(profile string) ([]byte, error) {
	self := strings.TrimSpace(c.preferredSMSPublicIdentity())
	var selfURI sip.Uri
	if err := sip.ParseUri(self, &selfURI); err != nil || !strings.EqualFold(strings.TrimSpace(selfURI.Scheme), "tel") {
		return nil, errors.New("voiceclient: registered telephone identity unavailable for self probe")
	}
	selfNumber := normalizedTelephoneSubscriber(selfURI.Host)
	if selfNumber == "" {
		return nil, errors.New("voiceclient: registered telephone identity unavailable for self probe")
	}
	tpDA, err := encodeProbeTPAddress(selfNumber)
	if err != nil {
		return nil, err
	}
	firstOctet := byte(0x01)
	if profile == "rp_submit_self_srr" {
		firstOctet |= 0x20
	}
	tpdu := []byte{firstOctet, 0x00}
	tpdu = append(tpdu, tpDA...)
	tpdu = append(tpdu,
		0x00, // TP-PID
		0x00, // TP-DCS GSM 7-bit default alphabet
		0x01, // TP-UDL: one septet
		0x41, // "A"
	)

	body := []byte{0x00, 0x55, 0x00} // RP-DATA(MS), RP-MR, empty RP-OA
	if profile == "rp_submit_self_no_rpda" {
		body = append(body, 0x00)
	} else {
		rpDestination, err := encodeProbeRPAddress(c.cfg.SMSC)
		if err != nil {
			return nil, err
		}
		body = append(body, rpDestination...)
	}
	body = append(body, byte(len(tpdu)))
	return append(body, tpdu...), nil
}

func encodeProbeTPAddress(number string) ([]byte, error) {
	number = strings.TrimSpace(number)
	toa := byte(0x81)
	if strings.HasPrefix(number, "+") {
		toa = 0x91
		number = strings.TrimPrefix(number, "+")
	}
	if number == "" || len(number) > 20 {
		return nil, errors.New("voiceclient: self probe telephone identity is invalid")
	}
	bcd := make([]byte, (len(number)+1)/2)
	for index := 0; index < len(number); index++ {
		if number[index] < '0' || number[index] > '9' {
			return nil, errors.New("voiceclient: self probe telephone identity contains a non-digit")
		}
		digit := number[index] - '0'
		if index%2 == 0 {
			bcd[index/2] = digit
		} else {
			bcd[index/2] |= digit << 4
		}
	}
	if len(number)%2 != 0 {
		bcd[len(number)/2] |= 0xf0
	}
	value := []byte{byte(len(number)), toa}
	return append(value, bcd...), nil
}

func encodeProbeRPAddress(number string) ([]byte, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return []byte{0x00}, nil
	}
	// Production keeps the SIP SMSC PSI in cfg.SMSC so MESSAGE Request-URI
	// selection follows the carrier override. The RP-DATA probe still needs
	// the numeric E.164 SMSC carried in the RP envelope, so accept sip:/tel:
	// forms here and encode their user/host component.
	lower := strings.ToLower(number)
	if strings.HasPrefix(lower, "sip:") || strings.HasPrefix(lower, "sips:") || strings.HasPrefix(lower, "tel:") {
		var parsed sip.Uri
		if err := sip.ParseUri(number, &parsed); err == nil {
			number = strings.TrimSpace(parsed.User)
			if number == "" {
				number = strings.TrimSpace(parsed.Host)
			}
		}
	}
	if at := strings.IndexByte(number, '@'); at >= 0 {
		number = number[:at]
	}
	if strings.HasPrefix(number, "tel:") {
		number = strings.TrimPrefix(number, "tel:")
	}
	toa := byte(0x81)
	number = strings.TrimSpace(number)
	if strings.HasPrefix(number, "+") {
		toa = 0x91
		number = strings.TrimPrefix(number, "+")
	}
	if number == "" {
		return nil, errors.New("voiceclient: MESSAGE probe SMSC has no digits")
	}
	bcd := make([]byte, (len(number)+1)/2)
	for index := 0; index < len(number); index++ {
		if number[index] < '0' || number[index] > '9' {
			return nil, errors.New("voiceclient: MESSAGE probe SMSC contains a non-digit")
		}
		digit := number[index] - '0'
		if index%2 == 0 {
			bcd[index/2] = digit
		} else {
			bcd[index/2] |= digit << 4
		}
	}
	if len(number)%2 != 0 {
		bcd[len(number)/2] |= 0xf0
	}
	value := append([]byte{toa}, bcd...)
	return append([]byte{byte(len(value))}, value...), nil
}

func (c *Client) normalizeProbeLocationMode(requested string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(requested))
	if mode == "" || mode == "registered" {
		mode = strings.ToLower(strings.TrimSpace(c.registerProfile.Normalized().LocationHeaderMode))
		if mode == "" {
			mode = "legacy_pani"
		}
	}
	switch mode {
	case "legacy_pani", "pani_only", "cellular_only", "both", "none":
		return mode, nil
	default:
		return "", fmt.Errorf("voiceclient: unsupported OPTIONS location_header_mode %q", requested)
	}
}

func inspectSIPOptionsProbe(request *sip.Request, response *sip.Response, transactionErr error, mode string) SIPOptionsProbeResult {
	result := SIPOptionsProbeResult{LocationHeaderMode: mode, TransportError: transactionErr != nil}
	if request != nil {
		result.RequestMethod = safeSIPText(request.Method.String(), 16)
		result.TargetScheme = safeSIPText(request.Recipient.Scheme, 16)
		result.TargetHostPresent = strings.TrimSpace(request.Recipient.Host) != ""
		result.PAccessNetworkInfoPresent = request.GetHeader("P-Access-Network-Info") != nil
		result.CellularNetworkInfoPresent = request.GetHeader("Cellular-Network-Info") != nil
		result.RouteCount = len(request.GetHeaders("Route"))
		result.PPreferredServicePresent = request.GetHeader("P-Preferred-Service") != nil
		result.AcceptContactPresent = request.GetHeader("Accept-Contact") != nil
		result.AuthorizationPresent = request.GetHeader("Authorization") != nil || request.GetHeader("Proxy-Authorization") != nil
		if request.GetHeader("Authorization") != nil {
			result.AuthorizationHeader = "Authorization"
		} else if request.GetHeader("Proxy-Authorization") != nil {
			result.AuthorizationHeader = "Proxy-Authorization"
		}
		result.BodyBytes = len(request.Body())
		if contentType := request.GetHeader("Content-Type"); contentType != nil {
			result.ContentType = safeSIPText(contentType.Value(), 96)
		}
		result.TransferEncodingPresent = request.GetHeader("Content-Transfer-Encoding") != nil
		result.RequestContactPresent = request.GetHeader("Contact") != nil
		result.RequestAllowPresent = request.GetHeader("Allow") != nil
		result.RequestAcceptPresent = request.GetHeader("Accept") != nil
		result.RequestUserAgentPresent = request.GetHeader("User-Agent") != nil
		result.RequestSecurityVerify = request.GetHeader("Security-Verify") != nil
		if preferred := request.GetHeader("P-Preferred-Identity"); preferred != nil {
			if uri, ok := parseAddressHeaderURI(preferred.Value()); ok {
				result.PreferredIdentityScheme = safeSIPText(uri.Scheme, 16)
			}
		}
	}
	if response == nil {
		return result
	}
	result.ResponseCode = response.StatusCode
	result.ResponseReason = safeSIPText(response.Reason, 96)
	result.ResponseSource = safeSIPText(response.Source(), 255)
	result.ResponseHeaderCount = len(response.Headers())
	result.ResponseViaCount = len(response.GetHeaders("Via"))
	result.WWWAuthenticateCount = len(response.GetHeaders("WWW-Authenticate"))
	result.ProxyAuthenticateCount = len(response.GetHeaders("Proxy-Authenticate"))
	result.ServerPresent, result.ServerSHA256 = probeHeaderHash(response, "Server")
	result.UserAgentPresent, result.UserAgentSHA256 = probeHeaderHash(response, "User-Agent")
	result.Warning = firstSafeProbeHeader(response, "Warning")
	result.ReasonHeader = firstSafeProbeHeader(response, "Reason")
	result.PAssertedService = firstSafeProbeHeader(response, "P-Asserted-Service")
	return result
}

func probeHeaderHash(response *sip.Response, name string) (bool, string) {
	if response == nil {
		return false, ""
	}
	header := response.GetHeader(name)
	if header == nil {
		return false, ""
	}
	return true, shortSignalingHash([]byte(strings.TrimSpace(header.Value())))
}

func firstSafeProbeHeader(response *sip.Response, name string) string {
	if response == nil {
		return ""
	}
	header := response.GetHeader(name)
	if header == nil {
		return ""
	}
	return safeSIPText(header.Value(), 255)
}
