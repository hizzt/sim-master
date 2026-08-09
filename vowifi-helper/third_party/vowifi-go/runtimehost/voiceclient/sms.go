package voiceclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/1239t/swu-go/pkg/logger"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
	"github.com/icholy/digest"

	"github.com/1239t/vowifi-go/runtimehost/messaging"
	"github.com/1239t/vowifi-go/runtimehost/simauth"
)

const (
	smsContentType      = "application/vnd.3gpp.sms"
	smsPreferredService = "urn:urn-7:3gpp-service.ims.icsi.sms"
	smsAcceptContact    = "*;+g.3gpp.smsip"
)

func applySMSServiceHeaders(request *sip.Request) {
	if request == nil {
		return
	}
	request.RemoveHeader("P-Preferred-Service")
	request.AppendHeader(sip.NewHeader("P-Preferred-Service", smsPreferredService))
	request.RemoveHeader("Accept-Contact")
	request.AppendHeader(sip.NewHeader("Accept-Contact", smsAcceptContact))
}

// SendSMS submits each of parts as a separate SIP MESSAGE (3GPP TS 24.341),
// expecting the immediate 202 Accepted per part, and records delivery
// tracking via DeliveryStore. It does not wait for the delivery report
// (RP-ACK/RP-ERROR) -- that arrives asynchronously as a separate incoming
// MESSAGE and is handled by handleIncomingMessage, matching how vohive's own
// DeliveryStore.MarkSMSDeliveryPartReport is designed to be called well
// after the initial submission returns (see its In-Reply-To/Call-ID/
// rp_mr-plus-time-window correlation cascade).
func (c *Client) SendSMS(ctx context.Context, peer, content string, parts []messaging.SMSPart) (messaging.SendOutcome, error) {
	if len(parts) == 0 {
		return messaging.SendOutcome{}, fmt.Errorf("voiceclient: no parts to send")
	}

	messageID := uuid.NewString()
	now := time.Now()

	if c.cfg.DeliveryStore != nil {
		if err := c.cfg.DeliveryStore.CreateSMSDelivery(messageID, "", c.cfg.DeviceID, peer, content, len(parts), now); err != nil {
			return messaging.SendOutcome{}, fmt.Errorf("voiceclient: CreateSMSDelivery: %w", err)
		}
	}

	for partNo, part := range parts {
		req, err := c.newSMSSubmitRequest()
		if err != nil {
			return messaging.SendOutcome{}, err
		}
		req.AppendHeader(sip.NewHeader("Content-Type", smsContentType))
		req.AppendHeader(sip.NewHeader("Content-Transfer-Encoding", "binary"))
		req.SetBody(part.Body)

		res, err := c.doSMSSubmitTransaction(ctx, req)
		if err != nil {
			return messaging.SendOutcome{}, fmt.Errorf("voiceclient: submit part %d: %w", partNo, err)
		}
		if res.StatusCode != 202 {
			return messaging.SendOutcome{}, fmt.Errorf("voiceclient: submit part %d: unexpected response %d %s", partNo, res.StatusCode, res.Reason)
		}

		if c.cfg.DeliveryStore != nil {
			callID := req.CallID().Value()
			if err := c.cfg.DeliveryStore.UpsertSMSDeliveryPart(messageID, partNo, callID, int(part.RPMR), "pending", now); err != nil {
				return messaging.SendOutcome{}, fmt.Errorf("voiceclient: UpsertSMSDeliveryPart: %w", err)
			}
		}
	}

	return messaging.SendOutcome{
		MessageID:     messageID,
		PartsTotal:    len(parts),
		DeliveryState: "pending",
	}, nil
}

func (c *Client) addPreemptiveSMSAuthorization(req *sip.Request) error {
	if req == nil {
		return nil
	}
	auth := c.preemptiveAuthSnapshot()
	if auth == nil || len(auth.AKAResponse) == 0 {
		return nil
	}
	return c.addPreemptiveSMSAuthorizationForSession(req, req.Recipient.String(), auth.Proxy, auth)
}

func (c *Client) addPreemptiveSMSAuthorizationFor(req *sip.Request, digestURI string, proxy bool) error {
	if req == nil {
		return nil
	}
	auth := c.preemptiveAuthSnapshot()
	return c.addPreemptiveSMSAuthorizationForSession(req, digestURI, proxy, auth)
}

func (c *Client) addPreemptiveSMSAuthorizationForSession(req *sip.Request, digestURI string, proxy bool, auth *DigestAuthSession) error {
	if auth == nil || len(auth.AKAResponse) == 0 {
		return nil
	}
	c.digestMu.Lock()
	defer c.digestMu.Unlock()
	c.digestNC++
	username := strings.TrimSpace(auth.Username)
	if username == "" {
		username = c.cfg.PrivateID
	}
	header, err := simauth.ComputeDigestWithAKAResponse(&auth.Challenge, auth.AKAResponse, digest.Options{
		Method:   req.Method.String(),
		URI:      digestURI,
		Username: username,
		Count:    c.digestNC,
		GetBody: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(req.Body())), nil
		},
	})
	if err != nil {
		return err
	}
	headerName := "Authorization"
	if proxy {
		headerName = "Proxy-Authorization"
	}
	req.RemoveHeader("Authorization")
	req.RemoveHeader("Proxy-Authorization")
	req.AppendHeader(sip.NewHeader(headerName, header))
	logger.Info("IMS preemptive digest attached",
		logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
		logger.String("method", req.Method.String()),
		logger.String("header", headerName),
		logger.Bool("username_present", username != ""),
		logger.Int("nonce_len", len(auth.Challenge.Nonce)),
		logger.Bool("home_domain_uri", digestURI != req.Recipient.String()),
		logger.String("algorithm", auth.Challenge.Algorithm),
		logger.String("qop", strings.Join(auth.Challenge.QOP, ",")))
	return nil
}

func (c *Client) doSMSSubmitTransaction(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	initial := req.Clone()
	initial.RemoveHeader("Authorization")
	initial.RemoveHeader("Proxy-Authorization")
	recoveryBase := initial
	protectedFlow := strings.TrimSpace(c.cfg.SecurityVerify) != ""
	phase := "initial"
	if protectedFlow {
		// A plain REGISTER recovery on port-s is invalid for this flow. Keep the
		// established protected binding and handle a challenge-less 401 below by
		// retrying the same SMS with the carrier-accepted cellular PANI profile.
		phase = "initial_protected"
	}
	res, err := c.doSMSSubmitTransactionAttempt(ctx, initial, phase)
	if err == nil && isBareSMSAuthFailure(res) {
		// O2's IP-SM-GW can reject the CNI-only service request that follows a
		// successful REGISTER, while accepting the identical RP-DATA when the
		// location is expressed as a cellular P-Access-Network-Info. This is a
		// retry of the original SIP MESSAGE, not a probe: Call-ID, destination,
		// RP-DATA and its embedded RP-MR all remain unchanged, so the eventual
		// asynchronous RP-ACK still correlates with the DeliveryStore entry.
		fallback := initial.Clone()
		fallback.RemoveHeader("Via")
		fallback.RemoveHeader("Authorization")
		fallback.RemoveHeader("Proxy-Authorization")
		if c.applySMSBare401CellularPANI(fallback) {
			recoveryBase = fallback
			res, err = c.doSMSSubmitTransactionAttempt(
				ctx,
				fallback,
				"bare_401_cellular_pani_retry",
				sipgo.ClientRequestIncreaseCSEQ,
				sipgo.ClientRequestAddVia,
			)
		}
	}
	if protectedFlow {
		return res, err
	}
	if !registeredFlowRecoveryNeeded(res, err) {
		return res, err
	}
	if recoverErr := c.recoverRegisteredFlow(ctx); recoverErr != nil {
		return nil, recoverErr
	}
	retry := recoveryBase.Clone()
	retry.RemoveHeader("Via")
	retry.RemoveHeader("Authorization")
	retry.RemoveHeader("Proxy-Authorization")
	if identityErr := c.applyPreferredSMSIdentity(retry); identityErr != nil {
		return nil, identityErr
	}
	if authErr := c.addPreemptiveSMSAuthorization(retry); authErr != nil {
		return nil, fmt.Errorf("voiceclient: fresh post-REGISTER SMS authorization: %w", authErr)
	}
	return c.doSMSSubmitTransactionAttempt(ctx, retry, "post_register_retry", sipgo.ClientRequestIncreaseCSEQ, sipgo.ClientRequestAddVia)
}

// applySMSBare401CellularPANI changes only the location representation used by
// an SMS retry. It deliberately omits cell-info-age because the accepted O2
// MESSAGE profile carried only the E-UTRAN cell identity in PANI.
func (c *Client) applySMSBare401CellularPANI(req *sip.Request) bool {
	if req == nil {
		return false
	}
	plmn := plmnFromIMSDomain(c.cfg.HomeDomain)
	if plmn == "" {
		plmn = plmnFromIMSDomain(c.cfg.Realm)
	}
	cellID := strings.TrimSpace(c.cfg.CellID)
	if plmn == "" || cellID == "" {
		return false
	}
	pani := fmt.Sprintf("3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=%s%s", plmn, cellID)
	if existing := req.GetHeader("P-Access-Network-Info"); existing != nil &&
		strings.EqualFold(strings.TrimSpace(existing.Value()), pani) &&
		req.GetHeader("Cellular-Network-Info") == nil {
		return false
	}
	for req.GetHeader("P-Access-Network-Info") != nil {
		req.RemoveHeader("P-Access-Network-Info")
	}
	for req.GetHeader("Cellular-Network-Info") != nil {
		req.RemoveHeader("Cellular-Network-Info")
	}
	req.AppendHeader(sip.NewHeader("P-Access-Network-Info", pani))
	return true
}

func (c *Client) doSMSSubmitTransactionAttempt(ctx context.Context, req *sip.Request, phase string, opts ...sipgo.ClientRequestOption) (*sip.Response, error) {
	res, err := c.doTransaction(ctx, req, opts...)
	c.logSMSSubmitDiagnostics(phase, req, res, err)
	if err != nil {
		return nil, err
	}
	for round := 0; round < maxChallengeRounds && (res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired); round++ {
		// A challenge-less 401/407 means the REGISTER protection space on this
		// TCP flow is no longer usable. Return the response to the outer wrapper,
		// which performs one REGISTER recovery; do not spin challenge retries.
		if isBareSMSAuthFailure(res) {
			return res, nil
		}
		challenged, err := c.buildSMSChallengeRequest(req, res)
		if err != nil {
			return nil, fmt.Errorf("voiceclient: SMS challenge round %d: %w", round+1, err)
		}
		res, err = c.doTransaction(ctx, challenged, sipgo.ClientRequestIncreaseCSEQ, sipgo.ClientRequestAddVia)
		c.logSMSSubmitDiagnostics(fmt.Sprintf("%s_challenge_%d", phase, round+1), challenged, res, err)
		if err != nil {
			return nil, err
		}
		req = challenged
	}
	return res, nil
}

func registeredFlowRecoveryNeeded(res *sip.Response, err error) bool {
	return errors.Is(err, sip.ErrTransactionTransport) || isBareSMSAuthFailure(res)
}

func isBareSMSAuthFailure(res *sip.Response) bool {
	if res == nil || (res.StatusCode != sip.StatusUnauthorized && res.StatusCode != sip.StatusProxyAuthRequired) {
		return false
	}
	return len(res.GetHeaders("WWW-Authenticate")) == 0 && len(res.GetHeaders("Proxy-Authenticate")) == 0
}

func (c *Client) buildSMSChallengeRequest(prevReq *sip.Request, prevRes *sip.Response) (*sip.Request, error) {
	if prevReq == nil || prevRes == nil {
		return nil, fmt.Errorf("SMS digest challenge is incomplete")
	}
	challenge, err := c.selectDigestChallenge(prevRes)
	if err != nil {
		return nil, err
	}
	result, err := simauth.ComputeDigest(c.cfg.AKA, challenge, digest.Options{
		Method:   prevReq.Method.String(),
		URI:      prevReq.Recipient.String(),
		Username: c.cfg.PrivateID,
	})
	if err != nil {
		return nil, err
	}

	request := prevReq.Clone()
	request.RemoveHeader("Via")
	request.RemoveHeader("Authorization")
	request.RemoveHeader("Proxy-Authorization")
	headerName := "Authorization"
	if len(prevRes.GetHeaders("WWW-Authenticate")) == 0 && len(prevRes.GetHeaders("Proxy-Authenticate")) > 0 {
		headerName = "Proxy-Authorization"
	}
	request.AppendHeader(sip.NewHeader(headerName, result.Header))
	return request, nil
}

func (c *Client) newSMSSubmitRequest() (*sip.Request, error) {
	serviceCenterURI := c.smsServiceCenterURI()
	if serviceCenterURI == "" {
		return nil, fmt.Errorf("voiceclient: SMS service centre is required")
	}
	return c.newSMSRequest(serviceCenterURI)
}

func (c *Client) newSMSRequest(peer string) (*sip.Request, error) {
	remoteURI := c.smsPeerURI(peer)
	if remoteURI == "" {
		return nil, fmt.Errorf("voiceclient: SMS peer is required")
	}
	recipient := sip.Uri{}
	if err := sip.ParseUri(remoteURI, &recipient); err != nil {
		return nil, fmt.Errorf("voiceclient: parse SMS peer URI %q: %w", remoteURI, err)
	}

	req := sip.NewRequest(sip.MESSAGE, recipient)
	req.AppendHeader(sip.NewHeader("From", "<"+c.cfg.PublicURI+">;tag="+sip.GenerateTagN(16)))
	req.AppendHeader(sip.NewHeader("To", "<"+remoteURI+">"))
	// Keep MESSAGE on the same registered binding as REGISTER and INVITE.
	// A bare contact drops +g.3gpp.smsip and +sip.instance, which can make a
	// carrier's IP-SM-GW treat an otherwise registered sender as unauthorized.
	req.AppendHeader(sip.NewHeader("Contact", c.cfg.dialogContactHeader(c.registerProfile, c.sipInstanceURN, c.contactUser)))
	req.AppendHeader(sip.NewHeader("Call-ID", uuid.NewString()))
	req.AppendHeader(sip.NewHeader("CSeq", "1 MESSAGE"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Allow", "INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS"))
	req.AppendHeader(sip.NewHeader("Accept", "text/plain, application/vnd.3gpp.sms"))
	// Production SMS keeps the carrier-proven 06:17 service-request shape.
	// P-Preferred-Service, Accept-Contact, sec-agree request headers, Supported,
	// and Request-Disposition remain available to explicit diagnostic probes,
	// but O2's IP-SM-GW did not accept them as part of this MESSAGE profile.
	req.AppendHeader(sip.NewHeader("P-Preferred-Identity", "<"+c.cfg.PublicURI+">"))
	if securityVerify := strings.TrimSpace(c.cfg.SecurityVerify); securityVerify != "" {
		req.AppendHeader(sip.NewHeader("Security-Verify", securityVerify))
	}
	c.applyServiceLocationHeaders(req)
	if userAgent := strings.TrimSpace(c.registerProfile.UserAgent); userAgent != "" {
		req.AppendHeader(sip.NewHeader("User-Agent", userAgent))
	}
	// TS 24.229 requires the preloaded P-CSCF route first, followed by the
	// Service-Route values learned at registration. SetDestination controls the
	// TCP peer but does not put the P-CSCF into the SIP route set.
	if pcscf := strings.TrimSpace(c.cfg.PCSCFAddr); pcscf != "" {
		req.AppendHeader(sip.NewHeader("Route", "<sip:"+pcscf+";lr>"))
	}
	for _, route := range c.cfg.ServiceRoutes {
		if route = strings.TrimSpace(route); route != "" {
			req.AppendHeader(sip.NewHeader("Route", route))
		}
	}
	req.SetDestination(c.cfg.pcscfDestination())
	if c.cfg.transportNetwork() == "udp" {
		req.SetTransport("UDP")
	} else {
		req.SetTransport("TCP")
	}
	if err := c.applyPreferredSMSIdentity(req); err != nil {
		return nil, err
	}
	return req, nil
}

func (c *Client) smsServiceCenterURI() string {
	smsc := strings.TrimSpace(c.cfg.SMSC)
	if smsc == "" {
		return ""
	}
	lower := strings.ToLower(smsc)
	if strings.HasPrefix(lower, "sip:") || strings.HasPrefix(lower, "sips:") || strings.HasPrefix(lower, "tel:") {
		return smsc
	}
	if strings.Contains(smsc, "@") {
		return "sip:" + smsc
	}
	domain := c.smsServiceDomain()
	if domain == "" {
		return "tel:" + smsc
	}
	return "sip:" + smsc + "@" + domain + ";user=phone"
}

// smsServiceDomain follows the public service domain selected by the network.
// Some IMS cores authenticate in the 3GPP home realm but return an operator
// alias as the default P-Associated-URI. The SMSC PSI belongs to that public
// service domain, not necessarily the REGISTER realm.
func (c *Client) smsServiceDomain() string {
	identity := c.preferredPublicIdentity()
	var uri sip.Uri
	if sip.ParseUri(identity, &uri) == nil {
		scheme := strings.ToLower(strings.TrimSpace(uri.Scheme))
		if (scheme == "sip" || scheme == "sips") && strings.TrimSpace(uri.Host) != "" {
			return strings.TrimSpace(uri.Host)
		}
	}
	return strings.TrimSpace(c.cfg.HomeDomain)
}

func (c *Client) smsPeerURI(peer string) string {
	peer = strings.TrimSpace(peer)
	if peer == "" {
		return ""
	}
	lower := strings.ToLower(peer)
	if strings.HasPrefix(lower, "sip:") || strings.HasPrefix(lower, "sips:") || strings.HasPrefix(lower, "tel:") {
		return peer
	}
	if strings.Contains(peer, "@") {
		return "sip:" + peer
	}
	domain := strings.TrimSpace(c.cfg.HomeDomain)
	if domain == "" {
		return "tel:" + peer
	}
	return "sip:" + peer + "@" + domain
}

// rpKind is the outer RP envelope's message type, per 3GPP TS 24.011 --
// just enough to recognize a delivery report and its cause, not a full TPDU
// decode. See the package doc comment for why the TPDU layer itself stays
// in vohive.
type rpKind int

const (
	rpKindUnknown rpKind = iota
	rpKindAck
	rpKindError
)

type deliveryReport struct {
	kind  rpKind
	rpMR  byte
	cause int
}

// smsRPCauseDescription keeps the asynchronous delivery state actionable.
// RP cause 28 is especially important for IMS SMS diagnostics: the IP-SM-GW
// accepted the SIP MESSAGE but the core could not identify/provision the
// subscriber for SMS service.
func smsRPCauseDescription(cause int) string {
	switch cause {
	case 28:
		return "Unidentified subscriber"
	case 1:
		return "Unassigned (unallocated) number"
	case 8:
		return "Operator determined barring"
	case 21:
		return "Short message transfer rejected"
	case 22:
		return "Memory capacity exceeded"
	case 27:
		return "Destination out of service"
	case 41:
		return "Temporary failure"
	default:
		return "unknown cause"
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// classifyRPEnvelope reads the RP-level framing needed to recognize an
// RP-ACK/RP-ERROR and its RP-MR/cause. Message type octet values: 0x02/0x03
// = RP-ACK, 0x04/0x05 = RP-ERROR (MS->Network / Network->MS pairs
// respectively); a delivery report for our own submission arrives as the
// Network->MS variant (0x03 or 0x05), but both are accepted here since the
// direction doesn't affect how we correlate/record it. Cause parsing
// mirrors 3GPP TS 24.011: cause IE is [length][value], value's low 7 bits
// are the cause code.
func classifyRPEnvelope(body []byte) (deliveryReport, error) {
	if len(body) < 2 {
		return deliveryReport{}, fmt.Errorf("voiceclient: RP body too short (%d bytes)", len(body))
	}
	switch body[0] {
	case 0x02, 0x03:
		return deliveryReport{kind: rpKindAck, rpMR: body[1]}, nil
	case 0x04, 0x05:
		if len(body) < 4 {
			return deliveryReport{}, fmt.Errorf("voiceclient: RP-ERROR body too short (%d bytes)", len(body))
		}
		causeIELen := int(body[2])
		if causeIELen <= 0 || 3+causeIELen > len(body) {
			return deliveryReport{}, fmt.Errorf("voiceclient: RP-ERROR cause IE out of range")
		}
		cause := int(body[3] & 0x7F)
		return deliveryReport{kind: rpKindError, rpMR: body[1], cause: cause}, nil
	default:
		return deliveryReport{}, fmt.Errorf("voiceclient: unrecognized RP message type 0x%02x", body[0])
	}
}

// handleIncomingMessage is the SIP server's MESSAGE handler. It accepts a
// raw, CPIM-wrapped, or multipart 3GPP SMS payload, dispatches RP-DATA to the
// embedding application's inbound decoder, and keeps RP-ACK/RP-ERROR delivery
// reports on the existing DeliveryStore path.
func (c *Client) handleIncomingMessage(req *sip.Request, tx sip.ServerTransaction) {
	ct := req.GetHeader("Content-Type")
	if ct == nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 415, "Unsupported Media Type", nil))
		return
	}
	wireBody := req.Body()
	if transfer := req.GetHeader("Content-Transfer-Encoding"); transfer != nil {
		decoded, err := decodeSMSTransferEncoding(transfer.Value(), wireBody)
		if err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
			return
		}
		wireBody = decoded
	}
	payload, payloadErr := decodeSMSInboundPayload(ct.Value(), wireBody)
	if payloadErr != nil {
		baseType := strings.ToLower(strings.TrimSpace(strings.SplitN(ct.Value(), ";", 2)[0]))
		if strings.HasPrefix(baseType, "multipart/") || baseType == cpimContentType {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
			return
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, 415, "Unsupported Media Type", nil))
		return
	}

	report, err := classifyRPEnvelope(payload.body)
	if err != nil {
		if len(payload.body) > 1 && (payload.body[0] == 0x00 || payload.body[0] == 0x01) {
			logger.Info("IMS inbound SMS RP-DATA received",
				logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
				logger.Int("rp_mr", int(payload.body[1])),
				logger.Bool("cpim", payload.replyCPIM))
			c.handleInboundRPData(req, tx, payload)
			return
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return
	}

	if c.cfg.DeliveryStore != nil {
		inReplyTo := ""
		if irt := req.GetHeader("In-Reply-To"); irt != nil {
			inReplyTo = irt.Value()
		}
		callID := req.CallID().Value()

		state := "acked"
		if report.kind == rpKindError {
			state = "failed"
		}
		errText := ""
		if report.kind == rpKindError {
			errText = fmt.Sprintf("RP-ERROR cause %d (%s)", report.cause, smsRPCauseDescription(report.cause))
		}
		match, markErr := c.cfg.DeliveryStore.MarkSMSDeliveryPartReport(
			inReplyTo, callID, c.cfg.DeviceID, int(report.rpMR),
			state, 200, report.cause, errText, time.Now(),
		)
		logger.Info("IMS SMS delivery report received",
			logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
			logger.String("call_id", callID),
			logger.String("in_reply_to", inReplyTo),
			logger.Int("rp_mr", int(report.rpMR)),
			logger.String("rp_state", state),
			logger.Int("rp_cause", report.cause),
			logger.String("rp_cause_text", smsRPCauseDescription(report.cause)),
			logger.String("matched_message_id", match.MessageID),
			logger.Int("matched_part_no", match.PartNo),
			logger.String("match_error", errorString(markErr)))
	}

	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
}

func (c *Client) handleInboundRPData(req *sip.Request, tx sip.ServerTransaction, payload smsInboundPayload) {
	callID := ""
	if header := req.CallID(); header != nil {
		callID = header.Value()
	}
	inReplyTo := ""
	if header := req.GetHeader("In-Reply-To"); header != nil {
		inReplyTo = header.Value()
	}
	peerURI := ""
	// TS 24.341 identifies the IP-SM-GW/SMSC peer for the RP-ACK through
	// P-Asserted-Identity.  From may be the original SMS sender and can route
	// the acknowledgement back to the wrong SIP service, so use PAI first.
	for _, header := range req.GetHeaders("P-Asserted-Identity") {
		if header == nil {
			continue
		}
		for _, value := range splitSIPAddressList(header.Value()) {
			var uri sip.Uri
			var params sip.HeaderParams
			if _, err := sip.ParseAddressValue(value, &uri, &params); err != nil {
				// Some IP-SM-GW implementations emit an unbracketed PAI such as
				// sip:mhrcn-ipsmgw-01.uk.pri.o2.com:5060 (no @ user part).
				// It is still a routable SIP URI and is the correct target for the
				// independent RP-ACK, so accept the URI form as a fallback.
				if sip.ParseUri(strings.TrimSpace(value), &uri) != nil {
					continue
				}
			}
			scheme := strings.ToLower(strings.TrimSpace(uri.Scheme))
			if scheme == "sip" || scheme == "sips" || scheme == "tel" {
				peerURI = uri.String()
				break
			}
		}
		if peerURI != "" {
			break
		}
	}
	if peerURI == "" {
		if header := req.GetHeader("From"); header != nil {
			peerURI = smsCPIMURI(header.Value())
		}
	}
	inbound := messaging.InboundSMS{
		ID:         fmt.Sprintf("%s:%02x", callID, payload.body[1]),
		CallID:     callID,
		InReplyTo:  inReplyTo,
		PeerURI:    peerURI,
		Body:       append([]byte(nil), payload.body...),
		ReceivedAt: time.Now(),
	}

	handler := c.cfg.InboundSMSHandler
	if handler == nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return
	}
	reply, err := handler(inbound)
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	if err != nil || len(reply.Body) == 0 {
		if result := c.cfg.InboundSMSResultHandler; result != nil {
			result(inbound, reply, 0, err)
		}
		return
	}
	if payload.replyCPIM {
		wrapped, wrapErr := c.buildSMSCPIMReply(req, payload, reply.Body)
		if wrapErr != nil {
			if result := c.cfg.InboundSMSResultHandler; result != nil {
				result(inbound, reply, 0, wrapErr)
			}
			return
		}
		reply.Body = wrapped
		reply.ContentType = cpimContentType
	}

	go c.sendInboundSMSReply(inbound, reply)
}

func (c *Client) sendInboundSMSReply(inbound messaging.InboundSMS, reply messaging.InboundSMSReply) {
	result := c.cfg.InboundSMSResultHandler
	var req *sip.Request
	var err error
	if strings.TrimSpace(inbound.PeerURI) != "" {
		req, err = c.newSMSRequest(inbound.PeerURI)
	} else {
		req, err = c.newRequest(sip.MESSAGE, c.cfg.PCSCFAddr, false)
	}
	if err != nil {
		if result != nil {
			result(inbound, reply, 0, err)
		}
		return
	}
	contentType := strings.TrimSpace(reply.ContentType)
	if contentType == "" {
		contentType = smsContentType
	}
	req.AppendHeader(sip.NewHeader("Content-Type", contentType))
	req.AppendHeader(sip.NewHeader("Content-Transfer-Encoding", "binary"))
	if inbound.CallID != "" {
		req.AppendHeader(sip.NewHeader("In-Reply-To", inbound.CallID))
	}
	req.SetBody(reply.Body)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := c.doSMSSubmitTransaction(ctx, req)
	code := 0
	if res != nil {
		code = res.StatusCode
		if code < 200 || code >= 300 {
			err = fmt.Errorf("voiceclient: RP reply unexpected response %d %s", code, res.Reason)
		}
	}
	logger.Info("IMS inbound SMS RP reply completed",
		logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
		logger.Int("sip_code", code),
		logger.Bool("accepted", err == nil && code >= 200 && code < 300))
	if result != nil {
		result(inbound, reply, code, err)
	}
}
