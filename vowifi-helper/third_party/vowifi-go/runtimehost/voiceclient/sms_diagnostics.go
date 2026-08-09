package voiceclient

import (
	"errors"
	"strings"

	"github.com/1239t/swu-go/pkg/logger"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// smsSubmitDiagnostics deliberately contains request shape only. Phone
// numbers, IMS identities, digest values, AKA material, RPDUs, and SMS content
// must never be added here.
type smsSubmitDiagnostics struct {
	Phase                     string
	CallID                    string
	CSeqNumber                uint32
	CSeqMethod                string
	RequestScheme             string
	RequestHost               string
	RequestUserPresent        bool
	Destination               string
	RouteCount                int
	ContentType               string
	BodyBytes                 int
	PPreferredIdentityPresent bool
	PPreferredIdentityScheme  string
	PPreferredIdentityHasUser bool
	AuthorizationPresent      bool
	AuthorizationHeader       string
	DigestAlgorithm           string
	DigestQOP                 string
	DigestURIMatchesRequest   bool
	ResponseCode              int
	ResponseReason            string
	AuthenticationChallenge   bool
	BareAuthenticationFailure bool
	TransactionTransportError bool
}

func inspectSMSSubmit(phase string, request *sip.Request, response *sip.Response, transactionErr error) smsSubmitDiagnostics {
	diagnostics := smsSubmitDiagnostics{Phase: safeSIPText(phase, 64)}
	if request != nil {
		if callID := request.CallID(); callID != nil {
			diagnostics.CallID = safeSIPText(callID.Value(), 128)
		}
		if cseq := request.CSeq(); cseq != nil {
			diagnostics.CSeqNumber = cseq.SeqNo
			diagnostics.CSeqMethod = safeSIPText(cseq.MethodName.String(), 16)
		}
		diagnostics.RequestScheme = safeSIPText(request.Recipient.Scheme, 16)
		diagnostics.RequestHost = safeSIPText(request.Recipient.Host, 255)
		diagnostics.RequestUserPresent = strings.TrimSpace(request.Recipient.User) != ""
		diagnostics.Destination = safeSIPText(request.Destination(), 255)
		diagnostics.RouteCount = len(request.GetHeaders("Route"))
		if contentType := request.GetHeader("Content-Type"); contentType != nil {
			diagnostics.ContentType = safeSIPText(contentType.Value(), 96)
		}
		diagnostics.BodyBytes = len(request.Body())
		if preferred := request.GetHeader("P-Preferred-Identity"); preferred != nil {
			diagnostics.PPreferredIdentityPresent = true
			if uri, ok := parseAddressHeaderURI(preferred.Value()); ok {
				diagnostics.PPreferredIdentityScheme = safeSIPText(uri.Scheme, 16)
				diagnostics.PPreferredIdentityHasUser = strings.TrimSpace(uri.User) != ""
			}
		}

		authHeaderName := "Authorization"
		authHeader := request.GetHeader(authHeaderName)
		if authHeader == nil {
			authHeaderName = "Proxy-Authorization"
			authHeader = request.GetHeader(authHeaderName)
		}
		if authHeader != nil {
			diagnostics.AuthorizationPresent = true
			diagnostics.AuthorizationHeader = authHeaderName
			if credentials, err := digest.ParseCredentials(authHeader.Value()); err == nil {
				diagnostics.DigestAlgorithm = safeSIPText(credentials.Algorithm, 48)
				diagnostics.DigestQOP = safeSIPText(credentials.QOP, 48)
				diagnostics.DigestURIMatchesRequest = credentials.URI == request.Recipient.String()
			}
		}
	}
	if response != nil {
		diagnostics.ResponseCode = response.StatusCode
		diagnostics.ResponseReason = safeSIPText(response.Reason, 96)
		diagnostics.AuthenticationChallenge = len(response.GetHeaders("WWW-Authenticate")) > 0 ||
			len(response.GetHeaders("Proxy-Authenticate")) > 0
		diagnostics.BareAuthenticationFailure = isBareSMSAuthFailure(response)
	}
	diagnostics.TransactionTransportError = errors.Is(transactionErr, sip.ErrTransactionTransport)
	return diagnostics
}

func (c *Client) logSMSSubmitDiagnostics(phase string, request *sip.Request, response *sip.Response, transactionErr error) {
	diagnostics := inspectSMSSubmit(phase, request, response, transactionErr)
	logger.Info("IMS SMS submit transaction",
		logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
		logger.String("phase", diagnostics.Phase),
		logger.String("call_id", diagnostics.CallID),
		logger.Uint32("cseq", diagnostics.CSeqNumber),
		logger.String("cseq_method", diagnostics.CSeqMethod),
		logger.String("request_scheme", diagnostics.RequestScheme),
		logger.String("request_host", diagnostics.RequestHost),
		logger.Bool("request_user_present", diagnostics.RequestUserPresent),
		logger.String("destination", diagnostics.Destination),
		logger.Int("route_count", diagnostics.RouteCount),
		logger.String("content_type", diagnostics.ContentType),
		logger.Int("body_bytes", diagnostics.BodyBytes),
		logger.Bool("p_preferred_identity_present", diagnostics.PPreferredIdentityPresent),
		logger.String("p_preferred_identity_scheme", diagnostics.PPreferredIdentityScheme),
		logger.Bool("p_preferred_identity_user_present", diagnostics.PPreferredIdentityHasUser),
		logger.Bool("authorization_present", diagnostics.AuthorizationPresent),
		logger.String("authorization_header", diagnostics.AuthorizationHeader),
		logger.String("digest_algorithm", diagnostics.DigestAlgorithm),
		logger.String("digest_qop", diagnostics.DigestQOP),
		logger.Bool("digest_uri_matches_request", diagnostics.DigestURIMatchesRequest),
		logger.Int("response_code", diagnostics.ResponseCode),
		logger.String("response_reason", diagnostics.ResponseReason),
		logger.Bool("auth_challenge", diagnostics.AuthenticationChallenge),
		logger.Bool("bare_auth_failure", diagnostics.BareAuthenticationFailure),
		logger.Bool("transport_error", diagnostics.TransactionTransportError))
}
