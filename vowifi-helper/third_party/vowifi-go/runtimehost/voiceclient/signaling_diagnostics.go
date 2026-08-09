package voiceclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/1239t/swu-go/pkg/logger"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

const signalingResponseSourceRemote = "remote_response"
const signalingResponseSourceRemote487 = "remote_final_without_local_cancel"
const signalingResponseSourceLocalCancel = "local_cancel_correlated"

// signalingInviteDiagnostics contains only non-secret request shape. It never
// stores the Authorization value, IMS username, nonce, AKA RES/CK/IK, target
// user/phone number, or the raw Security-Verify value.
type signalingInviteDiagnostics struct {
	CallID                  string
	Method                  string
	RequestScheme           string
	RequestHost             string
	RequestPort             int
	RequestUserPresent      bool
	RequestUserPhone        bool
	Destination             string
	RouteCount              int
	RouteHost               string
	RoutePort               int
	ContactPresent          bool
	ContactScheme           string
	ContactHost             string
	ContactPort             int
	ContactTransport        string
	ContactUserPresent      bool
	ContactAudio            bool
	ContactMMTel            bool
	ContactSIPInstance      bool
	PPreferredIDPresent     bool
	PPreferredIDScheme      string
	PPreferredIDUserPresent bool
	PPreferredService       bool
	AcceptContactMMTel      bool
	AcceptContactRequire    bool
	AcceptContactExplicit   bool
	PANIPresent             bool
	PANIAccessType          string
	CellularInfoPresent     bool
	CellularAccessType      string
	CellularCellIDPresent   bool
	ContentType             string
	SDPBytes                int
	SDPSHA256               string
	SDPAudioPort            int
	SDPAudioProtocol        string
	SDPPayloads             string
	SDPConnectionFamily     string
	SDPDirection            string
	SDPAMR                  bool
	SDPAMRWB                bool
	SDPPreconditions        bool
	AuthHeader              string
	AuthParsed              bool
	DigestMethod            string
	DigestAlgorithm         string
	DigestQOP               string
	DigestURIHost           string
	DigestURIUserPresent    bool
	DigestURIMatchesRequest bool
	SecurityVerifyPresent   bool
	SecurityVerifyMode      string
	SecurityVerifySHA256    string
	Supported100rel         bool
	SupportedPrecondition   bool
	SupportedTimer          bool
	AllowPRACK              bool
}

type signalingResponseDiagnostics struct {
	CallID             string
	StatusCode         int
	Reason             string
	Provisional        bool
	Final              bool
	CSeqMethod         string
	CSeqNumber         uint32
	Require100rel      bool
	Supported100rel    bool
	RSeqPresent        bool
	RSeq               int
	PRACKRequired      bool
	ContactPresent     bool
	RecordRouteCount   int
	ContentType        string
	BodyBytes          int
	SDPAnswerParsed    bool
	SDPAnswerCodec     string
	SDPAnswerDirection string
	SDPAnswerRTPPort   int
	SDPAnswerIPFamily  string
	SDPAnswerError     string
	ToTagPresent       bool
	LocalHangup        bool
	ResponseSource     string
	ReasonHeaderCount  int
	ReasonProtocols    string
	ReasonCauses       string
	ReasonTextPresent  bool
	ReasonTextSHA256   string
	WarningPresent     bool
	WarningCode        int
	WarningTextSHA256  string
	AuthChallenge      bool
	ProxyAuthChallenge bool
}

func inspectSignalingInvite(request *sip.Request) signalingInviteDiagnostics {
	diagnostics := signalingInviteDiagnostics{}
	if request == nil {
		return diagnostics
	}
	diagnostics.Method = request.Method.String()
	if callID := request.CallID(); callID != nil {
		diagnostics.CallID = callID.Value()
	}
	diagnostics.RequestScheme = request.Recipient.Scheme
	diagnostics.RequestHost = request.Recipient.Host
	diagnostics.RequestPort = request.Recipient.Port
	diagnostics.RequestUserPresent = strings.TrimSpace(request.Recipient.User) != ""
	if request.Recipient.UriParams != nil {
		userParam, _ := request.Recipient.UriParams.Get("user")
		diagnostics.RequestUserPhone = strings.EqualFold(strings.TrimSpace(userParam), "phone")
	}
	diagnostics.Destination = strings.TrimSpace(request.Destination())
	diagnostics.RouteCount = len(request.GetHeaders("Route"))
	if route := request.GetHeader("Route"); route != nil {
		if uri, ok := parseAddressHeaderURI(route.Value()); ok {
			diagnostics.RouteHost = uri.Host
			diagnostics.RoutePort = uri.Port
		}
	}
	if contact := request.Contact(); contact != nil {
		diagnostics.ContactPresent = true
		diagnostics.ContactScheme = contact.Address.Scheme
		diagnostics.ContactHost = contact.Address.Host
		diagnostics.ContactPort = contact.Address.Port
		diagnostics.ContactUserPresent = strings.TrimSpace(contact.Address.User) != ""
		if contact.Address.UriParams != nil {
			diagnostics.ContactTransport, _ = contact.Address.UriParams.Get("transport")
		}
		contactValue := strings.ToLower(contact.Value())
		diagnostics.ContactAudio = sipContactHasFeature(contactValue, "audio")
		diagnostics.ContactMMTel = strings.Contains(contactValue, "+g.3gpp.icsi-ref") && strings.Contains(contactValue, "mmtel")
		diagnostics.ContactSIPInstance = strings.Contains(contactValue, "+sip.instance")
	}
	if preferredID := request.GetHeader("P-Preferred-Identity"); preferredID != nil {
		diagnostics.PPreferredIDPresent = true
		if uri, ok := parseAddressHeaderURI(preferredID.Value()); ok {
			diagnostics.PPreferredIDScheme = safeSIPText(uri.Scheme, 16)
			diagnostics.PPreferredIDUserPresent = strings.TrimSpace(uri.User) != ""
		}
	}
	diagnostics.PPreferredService = request.GetHeader("P-Preferred-Service") != nil
	for _, header := range request.GetHeaders("Accept-Contact") {
		value := strings.ToLower(header.Value())
		diagnostics.AcceptContactMMTel = diagnostics.AcceptContactMMTel ||
			(strings.Contains(value, "+g.3gpp.icsi-ref") && strings.Contains(value, "mmtel"))
		diagnostics.AcceptContactRequire = diagnostics.AcceptContactRequire || sipParameterPresent(value, "require")
		diagnostics.AcceptContactExplicit = diagnostics.AcceptContactExplicit || sipParameterPresent(value, "explicit")
	}
	if pani := request.GetHeader("P-Access-Network-Info"); pani != nil {
		diagnostics.PANIPresent = true
		diagnostics.PANIAccessType = sipAccessType(pani.Value())
	}
	if cellular := request.GetHeader("Cellular-Network-Info"); cellular != nil {
		diagnostics.CellularInfoPresent = true
		diagnostics.CellularAccessType = sipAccessType(cellular.Value())
		diagnostics.CellularCellIDPresent = strings.Contains(strings.ToLower(cellular.Value()), "utran-cell-id-3gpp=")
	}
	if contentType := request.GetHeader("Content-Type"); contentType != nil {
		diagnostics.ContentType = safeSIPText(contentType.Value(), 96)
	}
	diagnostics.SDPBytes = len(request.Body())
	diagnostics.SDPSHA256 = shortSignalingHash(request.Body())
	inspectInactiveSDP(request.Body(), &diagnostics)

	authHeaderName := "Authorization"
	authHeader := request.GetHeader(authHeaderName)
	if authHeader == nil {
		authHeaderName = "Proxy-Authorization"
		authHeader = request.GetHeader(authHeaderName)
	}
	if authHeader != nil {
		diagnostics.AuthHeader = authHeaderName
		credentials, err := digest.ParseCredentials(authHeader.Value())
		if err == nil {
			diagnostics.AuthParsed = true
			diagnostics.DigestMethod = request.Method.String()
			diagnostics.DigestAlgorithm = safeSIPText(credentials.Algorithm, 48)
			diagnostics.DigestQOP = safeSIPText(credentials.QOP, 48)
			diagnostics.DigestURIMatchesRequest = credentials.URI == request.Recipient.String()
			uri := sip.Uri{}
			if sip.ParseUri(credentials.URI, &uri) == nil {
				diagnostics.DigestURIHost = uri.Host
				diagnostics.DigestURIUserPresent = strings.TrimSpace(uri.User) != ""
			}
		}
	}
	if securityVerify := request.GetHeader("Security-Verify"); securityVerify != nil {
		value := strings.TrimSpace(securityVerify.Value())
		diagnostics.SecurityVerifyPresent = value != ""
		diagnostics.SecurityVerifySHA256 = shortSignalingHash([]byte(value))
		if mechanism, _, ok := strings.Cut(value, ";"); ok {
			diagnostics.SecurityVerifyMode = safeSIPText(mechanism, 32)
		} else {
			diagnostics.SecurityVerifyMode = safeSIPText(value, 32)
		}
	}
	diagnostics.Supported100rel = sipHeadersContainToken(request.GetHeaders("Supported"), "100rel")
	diagnostics.SupportedPrecondition = sipHeadersContainToken(request.GetHeaders("Supported"), "precondition")
	diagnostics.SupportedTimer = sipHeadersContainToken(request.GetHeaders("Supported"), "timer")
	diagnostics.AllowPRACK = sipHeadersContainToken(request.GetHeaders("Allow"), "PRACK")
	return diagnostics
}

func inspectSignalingResponse(callID string, response *sip.Response, localHangup bool) signalingResponseDiagnostics {
	diagnostics := signalingResponseDiagnostics{CallID: callID, LocalHangup: localHangup, ResponseSource: signalingResponseSourceRemote}
	if response == nil {
		return diagnostics
	}
	diagnostics.StatusCode = response.StatusCode
	diagnostics.Reason = safeSIPText(response.Reason, 96)
	diagnostics.Provisional = response.IsProvisional()
	diagnostics.Final = !diagnostics.Provisional
	if cseq := response.CSeq(); cseq != nil {
		diagnostics.CSeqMethod = cseq.MethodName.String()
		diagnostics.CSeqNumber = cseq.SeqNo
	}
	diagnostics.Require100rel = sipHeadersContainToken(response.GetHeaders("Require"), "100rel")
	diagnostics.Supported100rel = sipHeadersContainToken(response.GetHeaders("Supported"), "100rel")
	if rseq := response.GetHeader("RSeq"); rseq != nil {
		diagnostics.RSeqPresent = true
		diagnostics.RSeq, _ = strconv.Atoi(strings.TrimSpace(rseq.Value()))
	}
	diagnostics.PRACKRequired = diagnostics.Provisional && diagnostics.Require100rel && diagnostics.RSeqPresent
	diagnostics.ContactPresent = response.Contact() != nil
	diagnostics.RecordRouteCount = len(response.GetHeaders("Record-Route"))
	if contentType := response.GetHeader("Content-Type"); contentType != nil {
		diagnostics.ContentType = safeSIPText(contentType.Value(), 96)
	}
	diagnostics.BodyBytes = len(response.Body())
	if diagnostics.BodyBytes > 0 && strings.EqualFold(strings.TrimSpace(strings.Split(diagnostics.ContentType, ";")[0]), "application/sdp") {
		answer, err := parseIMSVoiceAnswer(response.Body())
		if err != nil {
			diagnostics.SDPAnswerError = safeSIPText(err.Error(), 96)
		} else {
			diagnostics.SDPAnswerParsed = true
			diagnostics.SDPAnswerCodec = answer.Codec
			diagnostics.SDPAnswerDirection = answer.Direction
			diagnostics.SDPAnswerRTPPort = answer.RTPPort
			if answer.RemoteIP.To4() != nil {
				diagnostics.SDPAnswerIPFamily = "IP4"
			} else {
				diagnostics.SDPAnswerIPFamily = "IP6"
			}
		}
	}
	if to := response.To(); to != nil && to.Params != nil {
		_, diagnostics.ToTagPresent = to.Params.Get("tag")
	}
	if response.StatusCode == sip.StatusRequestTerminated {
		if localHangup {
			diagnostics.ResponseSource = signalingResponseSourceLocalCancel
		} else {
			diagnostics.ResponseSource = signalingResponseSourceRemote487
		}
	}
	inspectSignalingReasonHeaders(response, &diagnostics)
	inspectSignalingWarningHeader(response, &diagnostics)
	diagnostics.AuthChallenge = len(response.GetHeaders("WWW-Authenticate")) > 0
	diagnostics.ProxyAuthChallenge = len(response.GetHeaders("Proxy-Authenticate")) > 0
	return diagnostics
}

func (c *Client) logSignalingInviteDiagnostics(request *sip.Request) {
	diagnostics := inspectSignalingInvite(request)
	logger.Info("IMS signaling INVITE prepared",
		logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
		logger.String("call_id", diagnostics.CallID),
		logger.String("method", diagnostics.Method),
		logger.String("request_scheme", diagnostics.RequestScheme),
		logger.String("request_host", diagnostics.RequestHost),
		logger.Int("request_port", diagnostics.RequestPort),
		logger.Bool("request_user_present", diagnostics.RequestUserPresent),
		logger.Bool("request_user_phone", diagnostics.RequestUserPhone),
		logger.String("destination", diagnostics.Destination),
		logger.Int("route_count", diagnostics.RouteCount),
		logger.String("route_host", diagnostics.RouteHost),
		logger.Int("route_port", diagnostics.RoutePort),
		logger.Bool("contact_present", diagnostics.ContactPresent),
		logger.String("contact_scheme", diagnostics.ContactScheme),
		logger.String("contact_host", diagnostics.ContactHost),
		logger.Int("contact_port", diagnostics.ContactPort),
		logger.String("contact_transport", diagnostics.ContactTransport),
		logger.Bool("contact_user_present", diagnostics.ContactUserPresent),
		logger.Bool("contact_audio", diagnostics.ContactAudio),
		logger.Bool("contact_mmtel", diagnostics.ContactMMTel),
		logger.Bool("contact_sip_instance", diagnostics.ContactSIPInstance),
		logger.Bool("p_preferred_identity_present", diagnostics.PPreferredIDPresent),
		logger.String("p_preferred_identity_scheme", diagnostics.PPreferredIDScheme),
		logger.Bool("p_preferred_identity_user_present", diagnostics.PPreferredIDUserPresent),
		logger.Bool("p_preferred_service_present", diagnostics.PPreferredService),
		logger.Bool("accept_contact_mmtel", diagnostics.AcceptContactMMTel),
		logger.Bool("accept_contact_require", diagnostics.AcceptContactRequire),
		logger.Bool("accept_contact_explicit", diagnostics.AcceptContactExplicit),
		logger.Bool("pani_present", diagnostics.PANIPresent),
		logger.String("pani_access_type", diagnostics.PANIAccessType),
		logger.Bool("cellular_info_present", diagnostics.CellularInfoPresent),
		logger.String("cellular_access_type", diagnostics.CellularAccessType),
		logger.Bool("cellular_cell_id_present", diagnostics.CellularCellIDPresent),
		logger.String("content_type", diagnostics.ContentType),
		logger.Int("sdp_bytes", diagnostics.SDPBytes),
		logger.String("sdp_sha256", diagnostics.SDPSHA256),
		logger.Int("sdp_audio_port", diagnostics.SDPAudioPort),
		logger.String("sdp_audio_protocol", diagnostics.SDPAudioProtocol),
		logger.String("sdp_payloads", diagnostics.SDPPayloads),
		logger.String("sdp_connection_family", diagnostics.SDPConnectionFamily),
		logger.String("sdp_direction", diagnostics.SDPDirection),
		logger.Bool("sdp_amr", diagnostics.SDPAMR),
		logger.Bool("sdp_amr_wb", diagnostics.SDPAMRWB),
		logger.Bool("sdp_preconditions", diagnostics.SDPPreconditions),
		logger.String("auth_header", diagnostics.AuthHeader),
		logger.Bool("auth_parsed", diagnostics.AuthParsed),
		logger.String("digest_method", diagnostics.DigestMethod),
		logger.String("digest_algorithm", diagnostics.DigestAlgorithm),
		logger.String("digest_qop", diagnostics.DigestQOP),
		logger.String("digest_uri_host", diagnostics.DigestURIHost),
		logger.Bool("digest_uri_user_present", diagnostics.DigestURIUserPresent),
		logger.Bool("digest_uri_matches_request", diagnostics.DigestURIMatchesRequest),
		logger.Bool("security_verify_present", diagnostics.SecurityVerifyPresent),
		logger.String("security_verify_mode", diagnostics.SecurityVerifyMode),
		logger.String("security_verify_sha256", diagnostics.SecurityVerifySHA256),
		logger.Bool("supported_100rel", diagnostics.Supported100rel),
		logger.Bool("supported_precondition", diagnostics.SupportedPrecondition),
		logger.Bool("supported_timer", diagnostics.SupportedTimer),
		logger.Bool("allow_prack", diagnostics.AllowPRACK))
}

func (c *Client) logSignalingResponseDiagnostics(callID string, response *sip.Response) {
	localHangup := c != nil && c.signalingCalls != nil && c.signalingCalls.hangupWasRequested(callID)
	diagnostics := inspectSignalingResponse(callID, response, localHangup)
	logger.Info("IMS signaling response",
		logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
		logger.String("call_id", diagnostics.CallID),
		logger.Int("sip_code", diagnostics.StatusCode),
		logger.String("reason", diagnostics.Reason),
		logger.Bool("provisional", diagnostics.Provisional),
		logger.Bool("final", diagnostics.Final),
		logger.String("cseq_method", diagnostics.CSeqMethod),
		logger.Uint32("cseq_number", diagnostics.CSeqNumber),
		logger.Bool("require_100rel", diagnostics.Require100rel),
		logger.Bool("supported_100rel", diagnostics.Supported100rel),
		logger.Bool("rseq_present", diagnostics.RSeqPresent),
		logger.Int("rseq", diagnostics.RSeq),
		logger.Bool("prack_required", diagnostics.PRACKRequired),
		logger.Bool("prack_implemented", true),
		logger.Bool("contact_present", diagnostics.ContactPresent),
		logger.Int("record_route_count", diagnostics.RecordRouteCount),
		logger.String("content_type", diagnostics.ContentType),
		logger.Int("body_bytes", diagnostics.BodyBytes),
		logger.Bool("sdp_answer_parsed", diagnostics.SDPAnswerParsed),
		logger.String("sdp_answer_codec", diagnostics.SDPAnswerCodec),
		logger.String("sdp_answer_direction", diagnostics.SDPAnswerDirection),
		logger.Int("sdp_answer_rtp_port", diagnostics.SDPAnswerRTPPort),
		logger.String("sdp_answer_ip_family", diagnostics.SDPAnswerIPFamily),
		logger.String("sdp_answer_error", diagnostics.SDPAnswerError),
		logger.Bool("to_tag_present", diagnostics.ToTagPresent),
		logger.Bool("local_hangup_requested", diagnostics.LocalHangup),
		logger.String("response_source", diagnostics.ResponseSource),
		logger.Int("reason_header_count", diagnostics.ReasonHeaderCount),
		logger.String("reason_protocols", diagnostics.ReasonProtocols),
		logger.String("reason_causes", diagnostics.ReasonCauses),
		logger.Bool("reason_text_present", diagnostics.ReasonTextPresent),
		logger.String("reason_text_sha256", diagnostics.ReasonTextSHA256),
		logger.Bool("warning_present", diagnostics.WarningPresent),
		logger.Int("warning_code", diagnostics.WarningCode),
		logger.String("warning_text_sha256", diagnostics.WarningTextSHA256),
		logger.Bool("auth_challenge", diagnostics.AuthChallenge),
		logger.Bool("proxy_auth_challenge", diagnostics.ProxyAuthChallenge))
}

func (c *Client) logSignalingWaitFailure(callID string, response *sip.Response, waitError error) {
	localHangup := c != nil && c.signalingCalls != nil && c.signalingCalls.hangupWasRequested(callID)
	diagnostics := inspectSignalingResponse(callID, response, localHangup)
	errorClass := "transaction_error"
	switch {
	case diagnostics.StatusCode >= 300:
		errorClass = "sip_final_response"
	case errors.Is(waitError, context.Canceled):
		errorClass = "context_canceled"
	case errors.Is(waitError, context.DeadlineExceeded):
		errorClass = "deadline_exceeded"
	}
	logger.Warn("IMS signaling WaitAnswer failed",
		logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
		logger.String("call_id", diagnostics.CallID),
		logger.String("error_class", errorClass),
		logger.Int("sip_code", diagnostics.StatusCode),
		logger.String("reason", diagnostics.Reason),
		logger.Bool("local_hangup_requested", diagnostics.LocalHangup),
		logger.String("response_source", diagnostics.ResponseSource))
}

func parseAddressHeaderURI(value string) (sip.Uri, bool) {
	value = strings.TrimSpace(value)
	if start := strings.Index(value, "<"); start >= 0 {
		value = value[start+1:]
		if end := strings.Index(value, ">"); end >= 0 {
			value = value[:end]
		}
	}
	uri := sip.Uri{}
	if sip.ParseUri(strings.TrimSpace(value), &uri) != nil {
		return sip.Uri{}, false
	}
	return uri, true
}

func inspectInactiveSDP(body []byte, diagnostics *signalingInviteDiagnostics) {
	if diagnostics == nil {
		return
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "m=audio "):
			fields := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(fields) >= 3 {
				diagnostics.SDPAudioPort, _ = strconv.Atoi(fields[1])
				diagnostics.SDPAudioProtocol = safeSIPText(fields[2], 32)
				if len(fields) > 3 {
					diagnostics.SDPPayloads = safeSIPText(strings.Join(fields[3:], ","), 96)
				}
			}
		case strings.HasPrefix(line, "c=IN "):
			fields := strings.Fields(strings.TrimPrefix(line, "c="))
			if len(fields) >= 2 {
				diagnostics.SDPConnectionFamily = safeSIPText(fields[1], 8)
			}
		case line == "a=inactive":
			diagnostics.SDPDirection = "inactive"
		case line == "a=sendrecv" || line == "a=sendonly" || line == "a=recvonly":
			diagnostics.SDPDirection = strings.TrimPrefix(line, "a=")
		case strings.HasPrefix(strings.ToLower(line), "a=rtpmap:"):
			lower := strings.ToLower(line)
			diagnostics.SDPAMRWB = diagnostics.SDPAMRWB || strings.Contains(lower, " amr-wb/")
			diagnostics.SDPAMR = diagnostics.SDPAMR || strings.Contains(lower, " amr/")
		case strings.HasPrefix(strings.ToLower(line), "a=curr:qos ") ||
			strings.HasPrefix(strings.ToLower(line), "a=des:qos ") ||
			strings.HasPrefix(strings.ToLower(line), "a=conf:qos "):
			diagnostics.SDPPreconditions = true
		}
	}
}

func sipContactHasFeature(value, feature string) bool {
	feature = strings.ToLower(strings.TrimSpace(feature))
	for _, part := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == ';' || r == ',' || r == '>' || r == '<'
	}) {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if name == feature {
			return true
		}
	}
	return false
}

func sipParameterPresent(value, parameter string) bool {
	parameter = strings.ToLower(strings.TrimSpace(parameter))
	for _, part := range strings.Split(strings.ToLower(value), ";") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if name == parameter {
			return true
		}
	}
	return false
}

func sipAccessType(value string) string {
	accessType, _, _ := strings.Cut(strings.TrimSpace(value), ";")
	return safeSIPText(accessType, 48)
}

func inspectSignalingReasonHeaders(response *sip.Response, diagnostics *signalingResponseDiagnostics) {
	if response == nil || diagnostics == nil {
		return
	}
	var protocols, causes, texts []string
	for _, header := range response.GetHeaders("Reason") {
		for _, value := range splitQuotedHeaderValues(header.Value(), ',') {
			parts := splitQuotedHeaderValues(value, ';')
			if len(parts) == 0 {
				continue
			}
			diagnostics.ReasonHeaderCount++
			if protocol := safeSIPText(parts[0], 24); protocol != "" {
				protocols = append(protocols, protocol)
			}
			for _, parameter := range parts[1:] {
				name, rawValue, ok := strings.Cut(parameter, "=")
				if !ok {
					continue
				}
				switch strings.ToLower(strings.TrimSpace(name)) {
				case "cause":
					if cause, err := strconv.Atoi(strings.TrimSpace(rawValue)); err == nil {
						causes = append(causes, strconv.Itoa(cause))
					}
				case "text":
					text := strings.Trim(strings.TrimSpace(rawValue), `"`)
					if text != "" {
						diagnostics.ReasonTextPresent = true
						texts = append(texts, text)
					}
				}
			}
		}
	}
	diagnostics.ReasonProtocols = safeSIPText(strings.Join(protocols, ","), 96)
	diagnostics.ReasonCauses = safeSIPText(strings.Join(causes, ","), 96)
	if len(texts) > 0 {
		diagnostics.ReasonTextSHA256 = shortSignalingHash([]byte(strings.Join(texts, "\x00")))
	}
}

func inspectSignalingWarningHeader(response *sip.Response, diagnostics *signalingResponseDiagnostics) {
	if response == nil || diagnostics == nil {
		return
	}
	warning := response.GetHeader("Warning")
	if warning == nil {
		return
	}
	diagnostics.WarningPresent = true
	fields := strings.Fields(warning.Value())
	if len(fields) > 0 {
		diagnostics.WarningCode, _ = strconv.Atoi(fields[0])
	}
	if len(fields) > 2 {
		diagnostics.WarningTextSHA256 = shortSignalingHash([]byte(strings.Join(fields[2:], " ")))
	}
}

func splitQuotedHeaderValues(value string, separator rune) []string {
	var out []string
	start := 0
	quoted := false
	escaped := false
	for index, char := range value {
		switch {
		case escaped:
			escaped = false
		case char == '\\' && quoted:
			escaped = true
		case char == '"':
			quoted = !quoted
		case char == separator && !quoted:
			if part := strings.TrimSpace(value[start:index]); part != "" {
				out = append(out, part)
			}
			start = index + 1
		}
	}
	if part := strings.TrimSpace(value[start:]); part != "" {
		out = append(out, part)
	}
	return out
}

func sipHeadersContainToken(headers []sip.Header, token string) bool {
	for _, header := range headers {
		for _, value := range strings.Split(header.Value(), ",") {
			if strings.EqualFold(strings.TrimSpace(value), token) {
				return true
			}
		}
	}
	return false
}

func shortSignalingHash(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:8])
}

func safeSIPText(value string, maxLength int) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
	if maxLength > 0 && len(value) > maxLength {
		value = value[:maxLength]
	}
	return value
}
