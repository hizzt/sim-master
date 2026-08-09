package voiceclient

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"mime"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/1239t/swu-go/pkg/logger"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
)

const (
	signalingCallContentType  = "application/sdp"
	signalingCallTimeout      = 90 * time.Second
	maxRetainedCalls          = 32
	signalingTransportRetries = 1
)

// SignalingCallStatus describes an actual IMS INVITE dialog. Each call owns a
// real RTP/RTCP endpoint on the SWu netstack. PCMU/PCMA calls can use the
// built-in file audio path; AMR/AMR-WB still require an external codec.
type SignalingCallStatus struct {
	CallID                  string    `json:"call_id"`
	PhoneNumber             string    `json:"phone_number"`
	State                   string    `json:"state"`
	SIPCode                 int       `json:"sip_code,omitempty"`
	Reason                  string    `json:"reason,omitempty"`
	StartedAt               time.Time `json:"started_at"`
	UpdatedAt               time.Time `json:"updated_at"`
	MediaReady              bool      `json:"media_ready"`
	MediaSupported          bool      `json:"media_supported"`
	MediaMode               string    `json:"media_mode"`
	MediaCodec              string    `json:"media_codec,omitempty"`
	MediaDirection          string    `json:"media_direction,omitempty"`
	AudioReady              bool      `json:"audio_ready"`
	AudioMode               string    `json:"audio_mode"`
	RTPPacketsReceived      uint64    `json:"rtp_packets_received"`
	RTPBytesReceived        uint64    `json:"rtp_bytes_received"`
	RTPPacketsSent          uint64    `json:"rtp_packets_sent"`
	RTPBytesSent            uint64    `json:"rtp_bytes_sent"`
	RTCPPacketsReceived     uint64    `json:"rtcp_packets_received"`
	RTCPBytesReceived       uint64    `json:"rtcp_bytes_received"`
	AudioPacketsDecoded     uint64    `json:"audio_packets_decoded"`
	AudioSamplesRecorded    uint64    `json:"audio_samples_recorded"`
	AudioRecordingBytes     uint64    `json:"audio_recording_bytes"`
	AudioRecordingTruncated bool      `json:"audio_recording_truncated"`
	RTPPacketsLost          uint64    `json:"rtp_packets_lost"`
	RTPPacketsOutOfOrder    uint64    `json:"rtp_packets_out_of_order"`
}

type signalingCall struct {
	status          SignalingCallStatus
	session         *sipgo.DialogClientSession
	media           *signalingMediaEndpoint
	cancel          context.CancelFunc
	hangupRequested bool
}

type signalingMediaEndpoint struct {
	mu       sync.Mutex
	playMu   sync.Mutex
	rtp      net.PacketConn
	rtcp     net.PacketConn
	rtpPort  int
	rtcpPort int
	answer   *imsVoiceAnswer
	closed   bool
	audio    signalingAudioState
	close    sync.Once
}

func (m *signalingMediaEndpoint) Close() {
	if m == nil {
		return
	}
	m.close.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.audio.playbackActive = false
		m.mu.Unlock()
		if m.rtp != nil {
			_ = m.rtp.Close()
		}
		if m.rtcp != nil {
			_ = m.rtcp.Close()
		}
	})
}

func (m *signalingMediaEndpoint) setAnswer(answer imsVoiceAnswer) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := answer
	m.answer = &copy
}

func (m *signalingMediaEndpoint) currentAnswer() (imsVoiceAnswer, bool) {
	if m == nil {
		return imsVoiceAnswer{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.answer == nil {
		return imsVoiceAnswer{}, false
	}
	return *m.answer, true
}

type imsVoiceAnswer struct {
	RemoteIP  net.IP
	RTCPIP    net.IP
	RTPPort   int
	RTCPPort  int
	Codec     string
	Payload   int
	Direction string
}

type signalingHangupPlan struct {
	status      SignalingCallStatus
	session     *sipgo.DialogClientSession
	cancel      context.CancelFunc
	established bool
	terminal    bool
}

type signalingCallStore struct {
	mu    sync.Mutex
	calls map[string]*signalingCall
}

type signalingReliableProvisionalTracker struct {
	pracked map[uint32]struct{}
}

// SignalingCallController is implemented by registered IMS services that can
// originate and terminate real SIP dialogs.
type SignalingCallController interface {
	StartSignalingCall(context.Context, string) (SignalingCallStatus, error)
	HangupSignalingCall(context.Context, string) (SignalingCallStatus, error)
	SignalingCallStatuses() []SignalingCallStatus
}

func newSignalingCallStore() *signalingCallStore {
	return &signalingCallStore{calls: make(map[string]*signalingCall)}
}

func (c *Client) StartSignalingCall(ctx context.Context, phoneNumber string) (SignalingCallStatus, error) {
	phoneNumber = strings.TrimSpace(phoneNumber)
	if phoneNumber == "" {
		return SignalingCallStatus{}, errors.New("voiceclient: phone number is required")
	}
	if c == nil || c.client == nil {
		return SignalingCallStatus{}, errors.New("voiceclient: IMS client is not ready")
	}
	if c.signalingCalls == nil {
		return SignalingCallStatus{}, errors.New("voiceclient: signaling call store is not ready")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	mediaCtx, mediaCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	media, err := c.openSignalingMedia(mediaCtx)
	mediaCancel()
	if err != nil {
		return SignalingCallStatus{}, fmt.Errorf("voiceclient: allocate IMS media endpoint: %w", err)
	}
	request, err := c.newSignalingInviteRequest(phoneNumber, media.rtpPort, media.rtcpPort)
	if err != nil {
		media.Close()
		return SignalingCallStatus{}, err
	}
	callID := request.CallID().Value()
	now := time.Now().UTC()
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), signalingCallTimeout)
	status := SignalingCallStatus{
		CallID:         callID,
		PhoneNumber:    phoneNumber,
		State:          "dialing",
		StartedAt:      now,
		UpdatedAt:      now,
		MediaReady:     false,
		MediaSupported: true,
		MediaMode:      "rtp_transport_waiting",
		AudioReady:     false,
		AudioMode:      "waiting_for_codec",
	}
	if err := c.signalingCalls.add(&signalingCall{status: status, media: media, cancel: cancel}); err != nil {
		cancel()
		media.Close()
		return SignalingCallStatus{}, err
	}
	c.startSignalingMediaReaders(callID, media)

	go c.runSignalingCall(callCtx, request, callID, media)
	return status, nil
}

func (c *Client) runSignalingCall(ctx context.Context, request *sip.Request, callID string, media *signalingMediaEndpoint) {
	if request == nil {
		c.finishSignalingCall(callID, 0, "failed", "INVITE request is unavailable")
		return
	}
	for attempt := 0; attempt <= signalingTransportRetries; attempt++ {
		if attempt > 0 {
			request = retrySignalingInviteRequest(request)
			if request == nil {
				c.finishSignalingCall(callID, 0, "failed", "could not rebuild INVITE after transport failure")
				return
			}
			logger.Warn("IMS signaling retry after transport failure",
				logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
				logger.String("call_id", callID),
				logger.Int("attempt", attempt+1))
		}

		c.logSignalingInviteDiagnostics(request)
		contact := request.Contact()
		if contact == nil {
			c.finishSignalingCall(callID, 0, "failed", "INVITE Contact header is unavailable")
			return
		}
		dialogUA := sipgo.DialogUA{Client: c.client, ContactHDR: *contact.Clone()}
		session, err := dialogUA.WriteInvite(ctx, request)
		if err != nil {
			if attempt < signalingTransportRetries && isSignalingTransportFailure(err) && !c.signalingCalls.hangupWasRequested(callID) {
				recovered, recoverErr := c.recoverSignalingInvite(ctx, request, false)
				if recoverErr != nil {
					c.finishSignalingCall(callID, 0, "failed", recoverErr.Error())
					return
				}
				request = recovered
				continue
			}
			state := "failed"
			if c.signalingCalls.hangupWasRequested(callID) {
				state = "terminated"
			}
			c.finishSignalingCall(callID, 0, state, err.Error())
			return
		}
		c.signalingCalls.setSession(callID, session)
		responseSeen := false
		reliableProvisional := signalingReliableProvisionalTracker{pracked: make(map[uint32]struct{})}
		err = session.WaitAnswer(ctx, sipgo.AnswerOptions{OnResponse: func(response *sip.Response) error {
			responseSeen = true
			c.logSignalingResponseDiagnostics(callID, response)
			if err := c.handleSignalingReliableProvisional(ctx, session, response, &reliableProvisional); err != nil {
				return err
			}
			state := "dialing"
			if response.StatusCode == sip.StatusRinging {
				state = "ringing"
			} else if response.IsProvisional() {
				state = "proceeding"
			}
			c.signalingCalls.update(callID, func(status *SignalingCallStatus) {
				status.State = state
				status.SIPCode = response.StatusCode
				status.Reason = response.Reason
			})
			if response.IsProvisional() && len(response.Body()) > 0 {
				if answer, hasSDP, parseErr := parseIMSVoiceResponse(response); parseErr == nil && hasSDP {
					c.applySignalingMediaAnswer(callID, media, answer)
				}
			}
			return nil
		}})
		if err != nil {
			c.logSignalingWaitFailure(callID, session.InviteResponse, err)
			if attempt < signalingTransportRetries && !responseSeen && isSignalingTransportFailure(err) && !c.signalingCalls.hangupWasRequested(callID) {
				recovered, recoverErr := c.recoverSignalingInvite(ctx, request, false)
				if recoverErr != nil {
					c.finishSignalingCall(callID, 0, "failed", recoverErr.Error())
					return
				}
				request = recovered
				c.signalingCalls.setSession(callID, nil)
				continue
			}
			if attempt < signalingTransportRetries && signalingAuthResponse(session.InviteResponse) && !c.signalingCalls.hangupWasRequested(callID) {
				var retry *sip.Request
				var retryErr error
				if isBareSMSAuthFailure(session.InviteResponse) {
					retry, retryErr = c.recoverSignalingInvite(ctx, request, true)
				} else {
					retry, retryErr = c.buildSMSChallengeRequest(request, session.InviteResponse)
					if retryErr == nil {
						retryErr = incrementSignalingInviteCSeq(retry)
					}
				}
				if retryErr != nil {
					c.finishSignalingCall(callID, session.InviteResponse.StatusCode, "failed", "INVITE authentication retry failed: "+retryErr.Error())
					return
				}
				request = retry
				c.signalingCalls.setSession(callID, nil)
				continue
			}
			// RFC 3261 permits the final 2xx to win a CANCEL race. sipgo then
			// returns the canceled WaitAnswer context while retaining that 2xx in
			// InviteResponse. Such a dialog must still be ACKed and immediately
			// terminated with BYE; treating it as canceled would leak a live call
			// at the remote endpoint.
			if session.InviteResponse != nil && session.InviteResponse.IsSuccess() {
				c.ackAndTerminateSignalingCall(session, callID, "local hangup won a CANCEL/2xx race")
				return
			}
			state := "failed"
			if c.signalingCalls.hangupWasRequested(callID) || errors.Is(err, context.Canceled) {
				state = "terminated"
			}
			code, reason := signalingResponseFromError(err)
			c.finishSignalingCall(callID, code, state, reason)
			return
		}
		if err := ackSignalingSession(session); err != nil {
			_ = byeSignalingSession(session)
			c.finishSignalingCall(callID, 0, "failed", "ACK failed: "+err.Error())
			return
		}
		answer, answerOK := media.currentAnswer()
		if session.InviteResponse != nil && len(session.InviteResponse.Body()) > 0 {
			var answerErr error
			var hasSDP bool
			answer, hasSDP, answerErr = parseIMSVoiceResponse(session.InviteResponse)
			if answerErr != nil {
				_ = byeSignalingSession(session)
				c.finishSignalingCall(callID, session.InviteResponse.StatusCode, "failed", "invalid IMS media answer: "+answerErr.Error())
				return
			}
			if hasSDP {
				answerOK = true
				c.applySignalingMediaAnswer(callID, media, answer)
			}
		}
		if !answerOK {
			_ = byeSignalingSession(session)
			c.finishSignalingCall(callID, 200, "failed", "IMS answer did not contain a compatible audio SDP")
			return
		}
		code, reason := 200, "OK"
		if session.InviteResponse != nil {
			code = session.InviteResponse.StatusCode
			reason = session.InviteResponse.Reason
		}
		if c.signalingCalls.markEstablished(callID, code, fmt.Sprintf("%s; IMS %s media answer on RTP port %d; local audio codec unavailable", reason, answer.Codec, answer.RTPPort)) {
			c.terminateEstablishedSignalingCall(session, callID, "local hangup requested while answer completed")
		}
		return
	}
}

func retrySignalingInviteRequest(request *sip.Request) *sip.Request {
	if request == nil {
		return nil
	}
	retry := request.Clone()
	// sipgo creates a new branch whenever Via is absent. Keep Call-ID/CSeq
	// stable for the original INVITE while creating a new transaction key.
	retry.RemoveHeader("Via")
	return retry
}

func isSignalingTransportFailure(err error) bool {
	return err != nil && errors.Is(err, sip.ErrTransactionTransport)
}

func signalingAuthResponse(response *sip.Response) bool {
	return response != nil && (response.StatusCode == sip.StatusUnauthorized || response.StatusCode == sip.StatusProxyAuthRequired)
}

func (c *Client) recoverSignalingInvite(ctx context.Context, request *sip.Request, increaseCSeq bool) (*sip.Request, error) {
	if err := c.recoverRegisteredFlow(ctx); err != nil {
		return nil, fmt.Errorf("INVITE registered-flow recovery failed: %w", err)
	}
	retry := retrySignalingInviteRequest(request)
	if retry == nil {
		return nil, errors.New("INVITE registered-flow recovery could not rebuild request")
	}
	retry.RemoveHeader("Authorization")
	retry.RemoveHeader("Proxy-Authorization")
	if err := c.applyPreferredServiceIdentity(retry); err != nil {
		return nil, fmt.Errorf("INVITE recovered preferred identity: %w", err)
	}
	if increaseCSeq {
		if err := incrementSignalingInviteCSeq(retry); err != nil {
			return nil, err
		}
	}
	if err := c.addPreemptiveSMSAuthorization(retry); err != nil {
		return nil, fmt.Errorf("INVITE recovered authorization: %w", err)
	}
	return retry, nil
}

func incrementSignalingInviteCSeq(request *sip.Request) error {
	if request == nil || request.CSeq() == nil {
		return errors.New("INVITE CSeq is unavailable")
	}
	cseq := request.CSeq()
	cseq.SeqNo++
	return nil
}

func (c *Client) HangupSignalingCall(ctx context.Context, callID string) (SignalingCallStatus, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return SignalingCallStatus{}, errors.New("voiceclient: call_id is required")
	}
	if c == nil || c.signalingCalls == nil {
		return SignalingCallStatus{}, errors.New("voiceclient: signaling call store is not ready")
	}
	plan, err := c.signalingCalls.requestHangup(callID)
	if err != nil {
		return SignalingCallStatus{}, fmt.Errorf("voiceclient: signaling call %s was not found", callID)
	}
	if plan.terminal {
		return plan.status, nil
	}

	if plan.established && plan.session != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		hangupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := plan.session.Bye(hangupCtx)
		cancel()
		if err != nil {
			if status, ok := c.signalingCalls.status(callID); ok && signalingCallTerminal(status.State) {
				return status, nil
			}
			c.finishSignalingCall(callID, 0, "failed", "BYE failed: "+err.Error())
			status, _ := c.signalingCalls.status(callID)
			return status, err
		}
		c.finishSignalingCall(callID, 200, "terminated", "BYE accepted")
	} else if plan.cancel != nil {
		// Canceling WaitAnswer emits a real SIP CANCEL after a provisional
		// response, as required by RFC 3261.
		plan.cancel()
	}
	status, _ := c.signalingCalls.status(callID)
	return status, nil
}

func (c *Client) SignalingCallStatuses() []SignalingCallStatus {
	if c == nil || c.signalingCalls == nil {
		return []SignalingCallStatus{}
	}
	return c.signalingCalls.list()
}

func (c *Client) handleIncomingSignalingBye(request *sip.Request, transaction sip.ServerTransaction) {
	if request == nil {
		return
	}
	if request.CallID() == nil || c.signalingCalls == nil {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		return
	}
	callID := request.CallID().Value()
	call, ok := c.signalingCalls.get(callID)
	if !ok || call.session == nil || signalingCallTerminal(call.status.State) {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		return
	}
	requestDialogID, err := sip.DialogIDFromRequestUAC(request)
	if err != nil || call.session.ID == "" || requestDialogID != call.session.ID {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		return
	}
	if err := call.session.ReadBye(request, transaction); err != nil {
		c.finishSignalingCall(callID, 0, "failed", "remote BYE handling failed: "+err.Error())
		return
	}
	c.finishSignalingCall(callID, 200, "terminated", "remote BYE received")
}

func (c *Client) ackAndTerminateSignalingCall(session *sipgo.DialogClientSession, callID, reason string) {
	if err := ackSignalingSession(session); err != nil {
		_ = byeSignalingSession(session)
		c.finishSignalingCall(callID, 0, "failed", "ACK after answered CANCEL failed: "+err.Error())
		return
	}
	c.terminateEstablishedSignalingCall(session, callID, reason)
}

func (c *Client) terminateEstablishedSignalingCall(session *sipgo.DialogClientSession, callID, reason string) {
	if err := byeSignalingSession(session); err != nil {
		c.finishSignalingCall(callID, 0, "failed", "BYE failed after "+reason+": "+err.Error())
		return
	}
	c.finishSignalingCall(callID, 200, "terminated", reason+"; ACK and BYE completed")
}

func ackSignalingSession(session *sipgo.DialogClientSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return session.Ack(ctx)
}

func byeSignalingSession(session *sipgo.DialogClientSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return session.Bye(ctx)
}

func (c *Client) finishSignalingCall(callID string, sipCode int, state, reason string) {
	if c == nil || c.signalingCalls == nil {
		return
	}
	c.signalingCalls.update(callID, func(status *SignalingCallStatus) {
		status.State = state
		if sipCode != 0 {
			status.SIPCode = sipCode
		}
		status.Reason = reason
		status.MediaReady = false
		if status.MediaSupported {
			status.MediaMode = "rtp_transport_closed"
		}
	})
	if media := c.signalingCalls.media(callID); media != nil {
		media.Close()
	}
}

func (c *Client) newSignalingInviteRequest(phoneNumber string, mediaPorts ...int) (*sip.Request, error) {
	remoteURI := c.voicePeerURI(phoneNumber)
	if remoteURI == "" {
		return nil, errors.New("voiceclient: phone number is required")
	}
	recipient := sip.Uri{}
	if err := sip.ParseUri(remoteURI, &recipient); err != nil {
		return nil, fmt.Errorf("voiceclient: parse voice peer URI %q: %w", remoteURI, err)
	}

	callID := uuid.NewString()
	localIdentity := c.preferredPublicIdentity()
	request := sip.NewRequest(sip.INVITE, recipient)
	request.AppendHeader(sip.NewHeader("From", "<"+localIdentity+">;tag="+sip.GenerateTagN(16)))
	request.AppendHeader(sip.NewHeader("To", "<"+remoteURI+">"))
	request.AppendHeader(sip.NewHeader("Contact", c.cfg.dialogContactHeader(c.registerProfile, c.sipInstanceURN, c.contactUser)))
	request.AppendHeader(sip.NewHeader("Call-ID", callID))
	request.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	request.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	request.AppendHeader(sip.NewHeader("Allow", "INVITE,ACK,CANCEL,BYE,PRACK"))
	request.AppendHeader(sip.NewHeader("Supported", "100rel"))
	request.AppendHeader(sip.NewHeader("Accept", "application/sdp"))
	request.AppendHeader(sip.NewHeader("P-Preferred-Identity", "<"+localIdentity+">"))
	request.AppendHeader(sip.NewHeader("P-Preferred-Service", "urn:urn-7:3gpp-service.ims.icsi.mmtel"))
	request.AppendHeader(sip.NewHeader("Accept-Contact", "*;+g.3gpp.icsi-ref=\""+imsMmtelICSIRef+"\";require;explicit"))
	if securityVerify := strings.TrimSpace(c.cfg.SecurityVerify); securityVerify != "" {
		request.AppendHeader(sip.NewHeader("Security-Verify", securityVerify))
	}
	c.applyServiceLocationHeaders(request)
	if userAgent := strings.TrimSpace(c.registerProfile.UserAgent); userAgent != "" {
		request.AppendHeader(sip.NewHeader("User-Agent", userAgent))
	}
	for _, route := range c.cfg.ServiceRoutes {
		if route = strings.TrimSpace(route); route != "" {
			request.AppendHeader(sip.NewHeader("Route", route))
		}
	}
	if len(c.cfg.ServiceRoutes) == 0 && strings.TrimSpace(c.cfg.PCSCFAddr) != "" {
		request.AppendHeader(sip.NewHeader("Route", "<sip:"+strings.TrimSpace(c.cfg.PCSCFAddr)+";lr>"))
	}
	request.AppendHeader(sip.NewHeader("Content-Type", signalingCallContentType))
	rtpPort, rtcpPort := 9, 0
	if len(mediaPorts) > 0 && mediaPorts[0] > 0 {
		rtpPort = mediaPorts[0]
	}
	if len(mediaPorts) > 1 && mediaPorts[1] > 0 {
		rtcpPort = mediaPorts[1]
	}
	request.SetBody(imsVoiceSDP(c.cfg.LocalIP, rtpPort, rtcpPort, callID))
	request.SetDestination(c.cfg.pcscfDestination())
	if c.cfg.transportNetwork() == "udp" {
		request.SetTransport("UDP")
	} else {
		request.SetTransport("TCP")
	}
	if err := c.addPreemptiveSMSAuthorization(request); err != nil {
		return nil, fmt.Errorf("voiceclient: preemptive INVITE authorization: %w", err)
	}
	return request, nil
}

func (c *Client) handleSignalingReliableProvisional(ctx context.Context, session *sipgo.DialogClientSession, response *sip.Response, tracker *signalingReliableProvisionalTracker) error {
	if response == nil || !response.IsProvisional() || response.StatusCode <= 100 {
		return nil
	}
	reliable := signalingHeaderHasToken(response.GetHeaders("Require"), "100rel") || response.GetHeader("RSeq") != nil
	if !reliable {
		return nil
	}
	if session == nil || session.InviteRequest == nil {
		return errors.New("IMS reliable provisional response has no INVITE dialog")
	}
	rseqHeader := response.GetHeader("RSeq")
	if rseqHeader == nil {
		return errors.New("IMS reliable provisional response is missing RSeq")
	}
	rseqValue, err := strconv.ParseUint(strings.TrimSpace(rseqHeader.Value()), 10, 32)
	if err != nil || rseqValue == 0 {
		return fmt.Errorf("IMS reliable provisional response has invalid RSeq %q", rseqHeader.Value())
	}
	rseq := uint32(rseqValue)
	if tracker != nil {
		if tracker.pracked == nil {
			tracker.pracked = make(map[uint32]struct{})
		}
		if _, ok := tracker.pracked[rseq]; ok {
			return nil
		}
	}
	inviteCSeq := response.CSeq()
	if inviteCSeq == nil || !strings.EqualFold(inviteCSeq.MethodName.String(), sip.INVITE.String()) || inviteCSeq.SeqNo == 0 {
		return errors.New("IMS reliable provisional response has invalid INVITE CSeq")
	}

	recipient := session.InviteRequest.Recipient.Clone()
	if contact := response.Contact(); contact != nil {
		recipient = contact.Address.Clone()
	}
	prack := sip.NewRequest(sip.PRACK, *recipient)
	prack.AppendHeader(sip.NewHeader("RAck", fmt.Sprintf("%d %d INVITE", rseq, inviteCSeq.SeqNo)))
	prack.AppendHeader(sip.NewHeader("Allow", "INVITE,ACK,CANCEL,BYE,PRACK"))
	prack.AppendHeader(sip.NewHeader("P-Preferred-Identity", "<"+c.preferredPublicIdentity()+">"))
	if securityVerify := strings.TrimSpace(c.cfg.SecurityVerify); securityVerify != "" {
		prack.AppendHeader(sip.NewHeader("Security-Verify", securityVerify))
	}
	c.applyServiceLocationHeaders(prack)
	if userAgent := strings.TrimSpace(c.registerProfile.UserAgent); userAgent != "" {
		prack.AppendHeader(sip.NewHeader("User-Agent", userAgent))
	}
	if len(response.GetHeaders("Record-Route")) == 0 {
		for _, route := range c.cfg.ServiceRoutes {
			if route = strings.TrimSpace(route); route != "" {
				prack.AppendHeader(sip.NewHeader("Route", route))
			}
		}
	}
	prack.SetDestination(session.InviteRequest.Destination())
	prack.SetTransport(session.InviteRequest.Transport())
	prack.SetBody(nil)
	if err := c.addPreemptiveSMSAuthorization(prack); err != nil {
		return fmt.Errorf("IMS PRACK authorization: %w", err)
	}

	tx, err := session.TransactionRequest(ctx, prack)
	if err != nil {
		return fmt.Errorf("IMS PRACK transaction: %w", err)
	}
	defer tx.Terminate()
	for {
		select {
		case prackResponse := <-tx.Responses():
			if prackResponse == nil {
				continue
			}
			if prackResponse.IsProvisional() {
				continue
			}
			logger.Info("IMS signaling PRACK response",
				logger.String("trace_id", strings.TrimSpace(c.cfg.TraceID)),
				logger.String("call_id", strings.TrimSpace(session.InviteRequest.CallID().Value())),
				logger.Uint32("rseq", rseq),
				logger.Int("sip_code", prackResponse.StatusCode),
				logger.String("reason", prackResponse.Reason))
			if !prackResponse.IsSuccess() {
				return fmt.Errorf("IMS PRACK rejected: %d %s", prackResponse.StatusCode, strings.TrimSpace(prackResponse.Reason))
			}
			if tracker != nil {
				tracker.pracked[rseq] = struct{}{}
			}
			return nil
		case <-tx.Done():
			return fmt.Errorf("IMS PRACK transaction ended: %w", tx.Err())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func signalingHeaderHasToken(headers []sip.Header, token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return false
	}
	for _, header := range headers {
		if header == nil {
			continue
		}
		for _, part := range strings.Split(header.Value(), ",") {
			if strings.ToLower(strings.TrimSpace(part)) == token {
				return true
			}
		}
	}
	return false
}

func (c *Client) voicePeerURI(phoneNumber string) string {
	phoneNumber = strings.TrimSpace(phoneNumber)
	if phoneNumber == "" {
		return ""
	}
	lower := strings.ToLower(phoneNumber)
	if strings.HasPrefix(lower, "sip:") || strings.HasPrefix(lower, "sips:") || strings.HasPrefix(lower, "tel:") {
		return phoneNumber
	}
	if strings.Contains(phoneNumber, "@") {
		return "sip:" + phoneNumber
	}
	domain := strings.TrimSpace(c.cfg.HomeDomain)
	if domain == "" {
		return "sip:" + phoneNumber
	}
	return "sip:" + phoneNumber + "@" + domain + ";user=phone"
}

func imsVoiceSDP(localIP net.IP, rtpPort, rtcpPort int, callID string) []byte {
	network := "IP4"
	address := "0.0.0.0"
	if localIP != nil {
		address = localIP.String()
		if localIP.To4() == nil {
			network = "IP6"
		}
	}
	sessionID := time.Now().UnixNano()
	compactCallID := strings.ReplaceAll(callID, "-", "")
	if len(compactCallID) > 12 {
		compactCallID = compactCallID[:12]
	}
	if compactCallID != "" {
		if parsed, err := strconv.ParseUint(compactCallID, 16, 64); err == nil {
			sessionID = int64(parsed)
		}
	}
	if rtpPort <= 0 || rtpPort > 65535 {
		rtpPort = 9
	}
	rtcpAttribute := ""
	if rtcpPort > 0 && rtcpPort <= 65535 {
		rtcpAttribute = fmt.Sprintf("a=rtcp:%d IN %s %s\r\n", rtcpPort, network, address)
	}
	return []byte(fmt.Sprintf("v=0\r\no=- %d %d IN %s %s\r\ns=SimAdmin VoWiFi\r\nc=IN %s %s\r\nt=0 0\r\nm=audio %d RTP/AVP 97 96 8 0 101\r\n%s"+
		"a=rtpmap:97 AMR-WB/16000/1\r\na=fmtp:97 octet-align=1\r\n"+
		"a=rtpmap:96 AMR/8000/1\r\na=fmtp:96 octet-align=1\r\n"+
		"a=rtpmap:8 PCMA/8000\r\na=rtpmap:0 PCMU/8000\r\n"+
		"a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\n"+
		"a=ptime:20\r\na=maxptime:240\r\na=sendrecv\r\n",
		sessionID, sessionID, network, address, network, address, rtpPort, rtcpAttribute))
}

func (c *Client) openSignalingMedia(ctx context.Context) (*signalingMediaEndpoint, error) {
	if c == nil || c.cfg.LocalIP == nil {
		return nil, errors.New("local IMS IP is unavailable")
	}
	listen := func(port int) (net.PacketConn, error) {
		if dialer, ok := c.swuTCP.(SWUUDPDialer); ok && dialer != nil {
			return dialer.ListenPacketUDP(ctx, c.cfg.LocalIP, port)
		}
		network := "udp6"
		if c.cfg.LocalIP.To4() != nil {
			network = "udp4"
		}
		return net.ListenUDP(network, &net.UDPAddr{IP: c.cfg.LocalIP, Port: port})
	}

	for attempt := 0; attempt < 32; attempt++ {
		rtp, err := listen(0)
		if err != nil {
			return nil, err
		}
		rtpPort, err := packetConnPort(rtp)
		if err != nil || rtpPort%2 != 0 || rtpPort >= 65535 {
			_ = rtp.Close()
			continue
		}
		rtcp, err := listen(rtpPort + 1)
		if err != nil {
			_ = rtp.Close()
			continue
		}
		return &signalingMediaEndpoint{rtp: rtp, rtcp: rtcp, rtpPort: rtpPort, rtcpPort: rtpPort + 1}, nil
	}
	return nil, errors.New("could not allocate an even RTP/RTCP UDP port pair")
}

func (c *Client) startSignalingMediaReaders(callID string, media *signalingMediaEndpoint) {
	if c == nil || c.signalingCalls == nil || media == nil {
		return
	}
	go c.readSignalingMedia(callID, media, media.rtp, false)
	go c.readSignalingMedia(callID, media, media.rtcp, true)
}

func (c *Client) applySignalingMediaAnswer(callID string, media *signalingMediaEndpoint, answer imsVoiceAnswer) {
	if c == nil || c.signalingCalls == nil || media == nil {
		return
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	previous := media.answer
	endpointChanged := previous != nil && !sameIMSVoiceEndpoint(*previous, answer)
	copy := answer
	media.answer = &copy
	if endpointChanged {
		media.audio.resetReceiveSequence()
	}
	audioSupported := signalingAudioCodecSupported(answer.Codec)
	c.signalingCalls.update(callID, func(status *SignalingCallStatus) {
		if signalingCallTerminal(status.State) {
			return
		}
		status.MediaCodec = answer.Codec
		status.MediaDirection = answer.Direction
		status.AudioReady = audioSupported
		if audioSupported {
			status.AudioMode = "g711_file_io"
		} else {
			status.AudioMode = "codec_not_supported"
		}
		if endpointChanged {
			status.MediaReady = false
		}
		if !status.MediaReady {
			status.MediaMode = "rtp_transport_waiting"
		}
	})
}

func sameIMSVoiceEndpoint(left, right imsVoiceAnswer) bool {
	return left.RemoteIP.Equal(right.RemoteIP) && left.RTCPIP.Equal(right.RTCPIP) &&
		left.RTPPort == right.RTPPort && left.RTCPPort == right.RTCPPort &&
		left.Payload == right.Payload && left.Codec == right.Codec && left.Direction == right.Direction
}

func (c *Client) readSignalingMedia(callID string, media *signalingMediaEndpoint, conn net.PacketConn, rtcp bool) {
	if conn == nil {
		return
	}
	buffer := make([]byte, 64*1024)
	for {
		n, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			return
		}
		if n <= 0 || peer == nil {
			continue
		}
		media.mu.Lock()
		if media.answer == nil || media.closed {
			media.mu.Unlock()
			continue
		}
		answer := *media.answer
		media.mu.Unlock()
		if !mediaPeerMatches(peer, answer, rtcp) {
			continue
		}
		if rtcp {
			if !validRTCPPacket(buffer[:n]) {
				continue
			}
		} else {
			if !remoteMaySendMedia(answer.Direction) || !validRTPPacket(buffer[:n], answer.Payload) {
				continue
			}
		}
		var audioUpdate signalingAudioReceiveUpdate
		if !rtcp && signalingAudioCodecSupported(answer.Codec) {
			media.mu.Lock()
			if !media.closed && media.answer != nil && sameIMSVoiceEndpoint(*media.answer, answer) {
				audioUpdate = media.audio.recordRTP(answer.Codec, buffer[:n], answer.Payload)
			}
			media.mu.Unlock()
		}
		c.signalingCalls.update(callID, func(status *SignalingCallStatus) {
			if signalingCallTerminal(status.State) {
				return
			}
			if rtcp {
				status.RTCPPacketsReceived++
				status.RTCPBytesReceived += uint64(n)
				return
			}
			status.RTPPacketsReceived++
			status.RTPBytesReceived += uint64(n)
			status.AudioPacketsDecoded += audioUpdate.DecodedPackets
			status.AudioSamplesRecorded += audioUpdate.RecordedSamples
			status.AudioRecordingBytes += audioUpdate.RecordedBytes
			status.RTPPacketsLost += audioUpdate.LostPackets
			status.RTPPacketsOutOfOrder += audioUpdate.OutOfOrderPackets
			if audioUpdate.RecordingTruncated {
				status.AudioRecordingTruncated = true
			}
			status.MediaReady = true
			status.MediaMode = "rtp_transport_receiving"
		})
	}
}

func mediaPeerMatches(peer net.Addr, answer imsVoiceAnswer, rtcp bool) bool {
	peerIP, peerPort, ok := mediaPeerIPPort(peer)
	wantIP := answer.RemoteIP
	if rtcp && answer.RTCPIP != nil {
		wantIP = answer.RTCPIP
	}
	if !ok || wantIP == nil || !peerIP.Equal(wantIP) {
		return false
	}
	wantPort := answer.RTPPort
	if rtcp {
		wantPort = answer.RTCPPort
	}
	return wantPort > 0 && peerPort == wantPort
}

func mediaPeerIPPort(peer net.Addr) (net.IP, int, bool) {
	if peer == nil {
		return nil, 0, false
	}
	if udp, ok := peer.(*net.UDPAddr); ok && udp != nil {
		return udp.IP, udp.Port, udp.IP != nil && udp.Port > 0
	}
	host, portText, err := net.SplitHostPort(peer.String())
	if err != nil {
		return nil, 0, false
	}
	port, err := strconv.Atoi(portText)
	ip := net.ParseIP(host)
	return ip, port, err == nil && ip != nil && port > 0
}

func remoteMaySendMedia(direction string) bool {
	return direction == "sendrecv" || direction == "sendonly"
}

func validRTPPacket(packet []byte, payloadType int) bool {
	_, _, ok := signalingRTPPayload(packet, payloadType)
	return ok
}

func validRTCPPacket(packet []byte) bool {
	if len(packet) < 4 || len(packet)%4 != 0 {
		return false
	}
	for offset := 0; offset < len(packet); {
		if len(packet)-offset < 4 || packet[offset]>>6 != 2 {
			return false
		}
		packetType := packet[offset+1]
		if !validRTCPPacketType(packetType) {
			return false
		}
		packetLength := (int(binary.BigEndian.Uint16(packet[offset+2:offset+4])) + 1) * 4
		if packetLength < 4 || packetLength > len(packet)-offset {
			return false
		}
		last := offset+packetLength == len(packet)
		if packet[offset]&0x20 != 0 {
			if !last {
				return false
			}
			paddingLength := int(packet[offset+packetLength-1])
			if paddingLength == 0 || paddingLength > packetLength-4 {
				return false
			}
		}
		offset += packetLength
	}
	return true
}

func validRTCPPacketType(packetType byte) bool {
	switch packetType {
	case 192, 193, 194, 195, 200, 201, 202, 203, 204, 205, 206, 207, 208, 209, 210, 211:
		return true
	default:
		return false
	}
}

func packetConnPort(conn net.PacketConn) (int, error) {
	if conn == nil || conn.LocalAddr() == nil {
		return 0, errors.New("media endpoint has no local address")
	}
	switch addr := conn.LocalAddr().(type) {
	case *net.UDPAddr:
		if addr.Port > 0 {
			return addr.Port, nil
		}
	case interface{ Port() int }:
		if addr.Port() > 0 {
			return addr.Port(), nil
		}
	}
	_, portText, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return 0, fmt.Errorf("parse media endpoint %q: %w", conn.LocalAddr(), err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("invalid media endpoint port %q", portText)
	}
	return port, nil
}

type sdpRTPMap struct {
	codec    string
	clock    int
	channels int
}

type sdpAudioMedia struct {
	rtpPort   int
	rtcpPort  int
	rtpIP     net.IP
	rtcpIP    net.IP
	direction string
	payloads  []int
	rtpMaps   map[int]sdpRTPMap
	rtcpMux   bool
}

func parseIMSVoiceResponse(response *sip.Response) (imsVoiceAnswer, bool, error) {
	if response == nil || len(response.Body()) == 0 {
		return imsVoiceAnswer{}, false, nil
	}
	contentType := response.GetHeader("Content-Type")
	if contentType == nil {
		return imsVoiceAnswer{}, false, errors.New("SDP body has no Content-Type")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType.Value()))
	if err != nil {
		return imsVoiceAnswer{}, false, fmt.Errorf("invalid Content-Type: %w", err)
	}
	if !strings.EqualFold(mediaType, signalingCallContentType) {
		return imsVoiceAnswer{}, false, nil
	}
	answer, err := parseIMSVoiceAnswer(response.Body())
	return answer, true, err
}

func parseIMSVoiceAnswer(body []byte) (imsVoiceAnswer, error) {
	var sessionIP net.IP
	sessionDirection := "sendrecv"
	seenMedia := false
	var selectedAudio, currentAudio *sdpAudioMedia

	for _, rawLine := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "m="):
			seenMedia = true
			fields := strings.Fields(strings.TrimSpace(line[2:]))
			if len(fields) == 0 || !strings.EqualFold(fields[0], "audio") {
				currentAudio = nil
				continue
			}
			if len(fields) < 4 {
				return imsVoiceAnswer{}, fmt.Errorf("invalid audio media line %q", line)
			}
			if selectedAudio != nil {
				return imsVoiceAnswer{}, errors.New("multiple audio media sections are unsupported")
			}
			parsed, err := parseSDPAudioMedia(fields)
			if err != nil {
				return imsVoiceAnswer{}, err
			}
			selectedAudio = parsed
			currentAudio = parsed
		case strings.HasPrefix(lower, "c="):
			ip, err := parseSDPConnection(line[2:])
			if err != nil {
				return imsVoiceAnswer{}, err
			}
			if !seenMedia {
				sessionIP = ip
			} else if currentAudio != nil {
				currentAudio.rtpIP = ip
			}
		case isSDPDirection(lower):
			direction := strings.TrimPrefix(lower, "a=")
			if !seenMedia {
				sessionDirection = direction
			} else if currentAudio != nil {
				currentAudio.direction = direction
			}
		case currentAudio != nil && strings.HasPrefix(lower, "a=rtpmap:"):
			payload, mapping, err := parseSDPRTPMap(strings.TrimSpace(line[len("a=rtpmap:"):]))
			if err != nil {
				return imsVoiceAnswer{}, err
			}
			if payloadOffered(currentAudio.payloads, payload) {
				currentAudio.rtpMaps[payload] = mapping
			}
		case currentAudio != nil && strings.HasPrefix(lower, "a=rtcp:"):
			port, ip, err := parseSDPRTCP(strings.TrimSpace(line[len("a=rtcp:"):]))
			if err != nil {
				return imsVoiceAnswer{}, err
			}
			currentAudio.rtcpPort = port
			currentAudio.rtcpIP = ip
		case currentAudio != nil && lower == "a=rtcp-mux":
			currentAudio.rtcpMux = true
		}
	}

	audio := selectedAudio
	if audio == nil {
		return imsVoiceAnswer{}, errors.New("missing audio media section")
	}
	if audio.rtpPort == 0 {
		return imsVoiceAnswer{}, errors.New("audio media was rejected with port 0")
	}
	direction := audio.direction
	if direction == "" {
		direction = sessionDirection
	}
	if direction == "inactive" {
		return imsVoiceAnswer{}, errors.New("audio media is inactive")
	}
	if audio.rtcpMux {
		return imsVoiceAnswer{}, errors.New("rtcp-mux is unsupported")
	}
	remoteIP := audio.rtpIP
	if remoteIP == nil {
		remoteIP = sessionIP
	}
	if remoteIP == nil || remoteIP.IsUnspecified() {
		return imsVoiceAnswer{}, errors.New("missing usable audio connection address")
	}
	rtcpIP := audio.rtcpIP
	if rtcpIP == nil {
		rtcpIP = remoteIP
	}
	rtcpPort := audio.rtcpPort
	if rtcpPort == 0 {
		if audio.rtpPort >= 65535 {
			return imsVoiceAnswer{}, errors.New("RTP port leaves no default RTCP port")
		}
		rtcpPort = audio.rtpPort + 1
	}
	codec, payload, err := selectIMSVoiceCodec(audio)
	if err != nil {
		return imsVoiceAnswer{}, err
	}
	return imsVoiceAnswer{
		RemoteIP: remoteIP, RTCPIP: rtcpIP, RTPPort: audio.rtpPort, RTCPPort: rtcpPort,
		Codec: codec, Payload: payload, Direction: direction,
	}, nil
}

func parseSDPAudioMedia(fields []string) (*sdpAudioMedia, error) {
	portText := strings.SplitN(fields[1], "/", 2)[0]
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid RTP port %q", fields[1])
	}
	protocol := strings.ToUpper(fields[2])
	if protocol != "RTP/AVP" && protocol != "RTP/AVPF" {
		return nil, fmt.Errorf("unsupported audio protocol %q", fields[2])
	}
	media := &sdpAudioMedia{rtpPort: port, rtpMaps: make(map[int]sdpRTPMap)}
	for _, payloadText := range fields[3:] {
		payload, err := strconv.Atoi(payloadText)
		if err != nil || payload < 0 || payload > 127 {
			return nil, fmt.Errorf("invalid RTP payload type %q", payloadText)
		}
		media.payloads = append(media.payloads, payload)
	}
	return media, nil
}

func parseSDPConnection(value string) (net.IP, error) {
	fields := strings.Fields(value)
	if len(fields) < 3 || !strings.EqualFold(fields[0], "IN") {
		return nil, fmt.Errorf("invalid connection line %q", value)
	}
	ip := net.ParseIP(strings.TrimSpace(strings.SplitN(fields[2], "/", 2)[0]))
	if ip == nil || (strings.EqualFold(fields[1], "IP4") && ip.To4() == nil) ||
		(strings.EqualFold(fields[1], "IP6") && ip.To4() != nil) ||
		(!strings.EqualFold(fields[1], "IP4") && !strings.EqualFold(fields[1], "IP6")) {
		return nil, fmt.Errorf("invalid connection address %q", value)
	}
	return ip, nil
}

func parseSDPRTPMap(value string) (int, sdpRTPMap, error) {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return 0, sdpRTPMap{}, fmt.Errorf("invalid rtpmap %q", value)
	}
	payload, err := strconv.Atoi(fields[0])
	if err != nil || payload < 0 || payload > 127 {
		return 0, sdpRTPMap{}, fmt.Errorf("invalid rtpmap payload %q", fields[0])
	}
	encoding := strings.Split(fields[1], "/")
	if len(encoding) < 2 || len(encoding) > 3 {
		return 0, sdpRTPMap{}, fmt.Errorf("invalid rtpmap encoding %q", fields[1])
	}
	clock, err := strconv.Atoi(encoding[1])
	if err != nil || clock <= 0 {
		return 0, sdpRTPMap{}, fmt.Errorf("invalid rtpmap clock %q", encoding[1])
	}
	channels := 1
	if len(encoding) == 3 {
		channels, err = strconv.Atoi(encoding[2])
		if err != nil || channels <= 0 {
			return 0, sdpRTPMap{}, fmt.Errorf("invalid rtpmap channels %q", encoding[2])
		}
	}
	return payload, sdpRTPMap{codec: strings.ToUpper(encoding[0]), clock: clock, channels: channels}, nil
}

func parseSDPRTCP(value string) (int, net.IP, error) {
	fields := strings.Fields(value)
	if len(fields) != 1 && len(fields) != 4 {
		return 0, nil, fmt.Errorf("invalid rtcp attribute %q", value)
	}
	port, err := strconv.Atoi(fields[0])
	if err != nil || port <= 0 || port > 65535 {
		return 0, nil, fmt.Errorf("invalid RTCP port %q", fields[0])
	}
	if len(fields) == 1 {
		return port, nil, nil
	}
	ip, err := parseSDPConnection(strings.Join(fields[1:], " "))
	if err != nil {
		return 0, nil, fmt.Errorf("invalid RTCP address: %w", err)
	}
	return port, ip, nil
}

func selectIMSVoiceCodec(media *sdpAudioMedia) (string, int, error) {
	// Prefer the codecs that have an in-process audio path. Keep AMR variants
	// as signaling/transport fallbacks when the carrier does not return G.711.
	for _, wanted := range []string{"PCMA", "PCMU", "AMR-WB", "AMR"} {
		for _, payload := range media.payloads {
			mapping, hasMapping := media.rtpMaps[payload]
			if wanted == "AMR-WB" || wanted == "AMR" {
				clock := 16000
				if wanted == "AMR" {
					clock = 8000
				}
				if payload >= 96 && payload <= 127 && hasMapping && mapping.codec == wanted && mapping.clock == clock && mapping.channels == 1 {
					return wanted, payload, nil
				}
				continue
			}
			staticCodec := staticRTPCodec(payload)
			if staticCodec != wanted {
				continue
			}
			if !hasMapping || (mapping.codec == wanted && mapping.clock == 8000 && mapping.channels == 1) {
				return wanted, payload, nil
			}
		}
	}
	return "", 0, errors.New("no supported PCMA/PCMU/AMR-WB/AMR codec with a valid RTP mapping")
}

func payloadOffered(payloads []int, want int) bool {
	for _, payload := range payloads {
		if payload == want {
			return true
		}
	}
	return false
}

func isSDPDirection(line string) bool {
	return line == "a=sendrecv" || line == "a=sendonly" || line == "a=recvonly" || line == "a=inactive"
}

func staticRTPCodec(payload int) string {
	switch payload {
	case 0:
		return "PCMU"
	case 8:
		return "PCMA"
	default:
		return ""
	}
}

func signalingResponseFromError(err error) (int, string) {
	var responseError *sipgo.ErrDialogResponse
	if errors.As(err, &responseError) && responseError.Res != nil {
		return responseError.Res.StatusCode, responseError.Res.Reason
	}
	if err == nil {
		return 0, ""
	}
	return 0, err.Error()
}

func (s *signalingCallStore) add(call *signalingCall) error {
	if call == nil || strings.TrimSpace(call.status.CallID) == "" {
		return errors.New("voiceclient: invalid signaling call")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, current := range s.calls {
		if !signalingCallTerminal(current.status.State) {
			return fmt.Errorf("voiceclient: signaling call %s is already in progress", current.status.CallID)
		}
	}
	if len(s.calls) >= maxRetainedCalls {
		var oldestID string
		var oldestTime time.Time
		for id, current := range s.calls {
			if !signalingCallTerminal(current.status.State) {
				continue
			}
			if oldestID == "" || current.status.UpdatedAt.Before(oldestTime) {
				oldestID = id
				oldestTime = current.status.UpdatedAt
			}
		}
		if oldestID != "" {
			delete(s.calls, oldestID)
		}
	}
	s.calls[call.status.CallID] = call
	return nil
}

func (s *signalingCallStore) setSession(callID string, session *sipgo.DialogClientSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if call := s.calls[callID]; call != nil {
		call.session = session
		call.status.UpdatedAt = time.Now().UTC()
	}
}

func (s *signalingCallStore) update(callID string, update func(*SignalingCallStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if call := s.calls[callID]; call != nil {
		update(&call.status)
		call.status.UpdatedAt = time.Now().UTC()
	}
}

func (s *signalingCallStore) get(callID string) (*signalingCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return nil, false
	}
	return &signalingCall{status: call.status, session: call.session, cancel: call.cancel, hangupRequested: call.hangupRequested}, true
}

func (s *signalingCallStore) media(callID string) *signalingMediaEndpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.calls[callID]
	if call == nil {
		return nil
	}
	return call.media
}

func (s *signalingCallStore) requestHangup(callID string) (signalingHangupPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return signalingHangupPlan{}, errors.New("signaling call was not found")
	}
	if signalingCallTerminal(call.status.State) {
		return signalingHangupPlan{status: call.status, terminal: true}, nil
	}
	established := call.status.State == "signaling_established"
	call.hangupRequested = true
	call.status.State = "terminating"
	call.status.Reason = "local hangup requested"
	call.status.UpdatedAt = time.Now().UTC()
	return signalingHangupPlan{
		status:      call.status,
		session:     call.session,
		cancel:      call.cancel,
		established: established,
	}, nil
}

// markEstablished performs the answer/hangup arbitration under the store
// mutex. It returns true when a concurrent hangup already won and the caller
// must ACK then immediately BYE instead of publishing an established state.
func (s *signalingCallStore) markEstablished(callID string, sipCode int, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.calls[callID]
	if call == nil {
		return false
	}
	if call.hangupRequested {
		return true
	}
	call.status.State = "signaling_established"
	call.status.SIPCode = sipCode
	call.status.Reason = reason
	call.status.UpdatedAt = time.Now().UTC()
	return false
}

func (s *signalingCallStore) hangupWasRequested(callID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.calls[callID]
	return call != nil && call.hangupRequested
}

func (s *signalingCallStore) status(callID string) (SignalingCallStatus, bool) {
	call, ok := s.get(callID)
	if !ok {
		return SignalingCallStatus{}, false
	}
	return call.status, true
}

func (s *signalingCallStore) list() []SignalingCallStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses := make([]SignalingCallStatus, 0, len(s.calls))
	for _, call := range s.calls {
		statuses = append(statuses, call.status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].StartedAt.After(statuses[j].StartedAt) })
	return statuses
}

func signalingCallTerminal(state string) bool {
	return state == "terminated" || state == "failed"
}
