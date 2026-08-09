package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/techblack/sim-master/vowifi-helper/smscodec"
	"github.com/1239t/vowifi-go/runtimehost"
	"github.com/1239t/vowifi-go/runtimehost/messaging"
	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

var rpMRSequence atomic.Uint32

type controlRequest struct {
	Action              string `json:"action"`
	CallID              string `json:"call_id,omitempty"`
	PhoneNumber         string `json:"phone_number,omitempty"`
	Content             string `json:"content,omitempty"`
	Encoding            string `json:"encoding,omitempty"`
	TimeoutSeconds      int    `json:"timeout_seconds,omitempty"`
	AfterID             string `json:"after_id,omitempty"`
	AudioFormat         string `json:"audio_format,omitempty"`
	AudioBase64         string `json:"audio_base64,omitempty"`
	TargetURI           string `json:"target_uri,omitempty"`
	LocationMode        string `json:"location_header_mode,omitempty"`
	RouteMode           string `json:"route_mode,omitempty"`
	PreemptiveAuth      bool   `json:"preemptive_authorization,omitempty"`
	PayloadProfile      string `json:"payload_profile,omitempty"`
	IdentityMode        string `json:"preferred_identity_mode,omitempty"`
	ServiceHeaders      bool   `json:"sms_service_headers,omitempty"`
	AuthMode            string `json:"preemptive_authorization_mode,omitempty"`
	OmitTransfer        bool   `json:"omit_transfer_encoding,omitempty"`
	StrictMinimal       bool   `json:"strict_minimal_headers,omitempty"`
	RecoverFlow         bool   `json:"registered_flow_recovery,omitempty"`
	PANIValue           string `json:"p_access_network_info_value,omitempty"`
	RequestStatusReport *bool  `json:"request_status_report,omitempty"`
}

type smsSendResult struct {
	Verified    bool   `json:"verified"`
	State       string `json:"state"`
	MessageID   string `json:"message_id,omitempty"`
	PartsTotal  int    `json:"parts_total"`
	SIPCode     int    `json:"sip_code,omitempty"`
	RPState     string `json:"rp_state,omitempty"`
	RPCause     int    `json:"rp_cause,omitempty"`
	Evidence    string `json:"evidence"`
	ObservedAt  string `json:"observed_at,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Content     string `json:"content,omitempty"`
}

type smsReceiveResult struct {
	Verified   bool   `json:"verified"`
	State      string `json:"state"`
	MessageID  string `json:"message_id,omitempty"`
	From       string `json:"from,omitempty"`
	Content    string `json:"content,omitempty"`
	RPMR       int    `json:"rp_mr,omitempty"`
	RPAckCode  int    `json:"rp_ack_sip_code,omitempty"`
	Evidence   string `json:"evidence"`
	ObservedAt string `json:"observed_at,omitempty"`
}

type controlResponse struct {
	OK      bool                               `json:"ok"`
	Error   string                             `json:"error,omitempty"`
	Status  *tunnelStatus                      `json:"status,omitempty"`
	Send    *smsSendResult                     `json:"send,omitempty"`
	Receive *smsReceiveResult                  `json:"receive,omitempty"`
	Call    *voiceclient.SignalingCallStatus   `json:"call,omitempty"`
	Calls   []voiceclient.SignalingCallStatus  `json:"calls,omitempty"`
	Audio   *audioControlResult                `json:"audio,omitempty"`
	Probe   *voiceclient.SIPOptionsProbeResult `json:"probe,omitempty"`
}

type audioControlResult struct {
	CallID        string                          `json:"call_id"`
	Format        string                          `json:"format,omitempty"`
	ContentType   string                          `json:"content_type,omitempty"`
	SampleRate    int                             `json:"sample_rate"`
	Channels      int                             `json:"channels"`
	BitsPerSample int                             `json:"bits_per_sample"`
	DataBase64    string                          `json:"data_base64,omitempty"`
	Stats         voiceclient.SignalingAudioStats `json:"stats"`
}

type controlServer struct {
	path     string
	report   *reporter
	instance *runtimehost.Instance
	store    *memoryDeliveryStore
	smsc     string
}

func newControlServer(path string, report *reporter, instance *runtimehost.Instance, store *memoryDeliveryStore, smsc string) *controlServer {
	return &controlServer{path: path, report: report, instance: instance, store: store, smsc: smsc}
}

func (s *controlServer) serve(ctx context.Context) error {
	if strings.TrimSpace(s.path) == "" || filepath.Clean(s.path) == "/" {
		return errors.New("invalid control socket path")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	defer listener.Close()
	// Do not unlink the shared path on exit. A replacement helper may already
	// own a newly-created socket at the same pathname; an older process must not
	// delete that new control endpoint. The backend removes stale sockets before
	// every launch.
	if err := os.Chmod(s.path, 0o600); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleConnection(ctx, conn)
	}
}

func (s *controlServer) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
	var request controlRequest
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(controlResponse{OK: false, Error: "decode request: " + err.Error()})
		return
	}
	response := s.handle(ctx, request)
	_ = json.NewEncoder(conn).Encode(response)
}

func (s *controlServer) handle(ctx context.Context, request controlRequest) controlResponse {
	switch strings.ToLower(strings.TrimSpace(request.Action)) {
	case "status":
		status := s.report.snapshot()
		return controlResponse{OK: true, Status: &status}
	case "send_sms":
		result, err := s.sendSMS(ctx, request)
		if err != nil {
			return controlResponse{OK: false, Error: err.Error(), Send: result}
		}
		return controlResponse{OK: true, Send: result}
	case "probe_options":
		result, err := s.probeOptions(ctx, request)
		if err != nil {
			return controlResponse{OK: false, Error: err.Error(), Probe: result}
		}
		return controlResponse{OK: true, Probe: result}
	case "probe_message":
		result, err := s.probeMessage(ctx, request)
		if err != nil {
			return controlResponse{OK: false, Error: err.Error(), Probe: result}
		}
		return controlResponse{OK: true, Probe: result}
	case "wait_receive":
		result := s.waitReceive(ctx, request.AfterID, requestTimeout(request.TimeoutSeconds, 60*time.Second))
		return controlResponse{OK: true, Receive: result}
	case "dial_call":
		result, err := s.dialCall(ctx, request.PhoneNumber)
		if err != nil {
			return controlResponse{OK: false, Error: err.Error(), Call: result}
		}
		return controlResponse{OK: true, Call: result}
	case "hangup_call":
		result, err := s.hangupCall(ctx, request.CallID)
		if err != nil {
			return controlResponse{OK: false, Error: err.Error(), Call: result}
		}
		return controlResponse{OK: true, Call: result}
	case "voice_status":
		return controlResponse{OK: true, Calls: s.instance.SignalingCallStatuses()}
	case "play_audio":
		result, err := s.playAudio(ctx, request)
		if err != nil {
			return controlResponse{OK: false, Error: err.Error(), Audio: result}
		}
		return controlResponse{OK: true, Audio: result}
	case "get_recording":
		result, err := s.getRecording(request)
		if err != nil {
			return controlResponse{OK: false, Error: err.Error(), Audio: result}
		}
		return controlResponse{OK: true, Audio: result}
	case "audio_stats":
		result, err := s.audioStats(request.CallID)
		if err != nil {
			return controlResponse{OK: false, Error: err.Error(), Audio: result}
		}
		return controlResponse{OK: true, Audio: result}
	default:
		return controlResponse{OK: false, Error: "action must be status, send_sms, probe_options, probe_message, wait_receive, dial_call, hangup_call, voice_status, play_audio, get_recording or audio_stats"}
	}
}

func (s *controlServer) probeOptions(parent context.Context, request controlRequest) (*voiceclient.SIPOptionsProbeResult, error) {
	if s.instance == nil {
		return nil, errors.New("IMS SIP OPTIONS probe is unavailable")
	}
	probeContext, cancel := context.WithTimeout(parent, requestTimeout(request.TimeoutSeconds, 10*time.Second))
	defer cancel()
	result, err := s.instance.ProbeSIPOptions(probeContext, voiceclient.SIPOptionsProbeRequest{
		TargetURI:               request.TargetURI,
		LocationHeaderMode:      request.LocationMode,
		RouteMode:               request.RouteMode,
		PreemptiveAuthorization: request.PreemptiveAuth,
		PreferredIdentityMode:   request.IdentityMode,
		SMSServiceHeaders:       request.ServiceHeaders,
		PreemptiveAuthMode:      request.AuthMode,
		OmitTransferEncoding:    request.OmitTransfer,
	})
	return &result, err
}

func (s *controlServer) probeMessage(parent context.Context, request controlRequest) (*voiceclient.SIPOptionsProbeResult, error) {
	if s.instance == nil {
		return nil, errors.New("IMS SIP MESSAGE probe is unavailable")
	}
	probeContext, cancel := context.WithTimeout(parent, requestTimeout(request.TimeoutSeconds, 10*time.Second))
	defer cancel()
	result, err := s.instance.ProbeSIPEmptyMessage(probeContext, voiceclient.SIPOptionsProbeRequest{
		TargetURI:               request.TargetURI,
		LocationHeaderMode:      request.LocationMode,
		RouteMode:               request.RouteMode,
		PreemptiveAuthorization: request.PreemptiveAuth,
		PayloadProfile:          request.PayloadProfile,
		PreferredIdentityMode:   request.IdentityMode,
		SMSServiceHeaders:       request.ServiceHeaders,
		PreemptiveAuthMode:      request.AuthMode,
		OmitTransferEncoding:    request.OmitTransfer,
		StrictMinimalHeaders:    request.StrictMinimal,
		RegisteredFlowRecovery:  request.RecoverFlow,
		PAccessNetworkInfoValue: request.PANIValue,
	})
	return &result, err
}

func (s *controlServer) playAudio(ctx context.Context, request controlRequest) (*audioControlResult, error) {
	callID := strings.TrimSpace(request.CallID)
	if callID == "" {
		return nil, errors.New("call_id is required")
	}
	if strings.TrimSpace(request.AudioBase64) == "" {
		return nil, errors.New("audio_base64 is required")
	}
	if len(request.AudioBase64) > base64.StdEncoding.EncodedLen(voiceclient.SignalingAudioSampleRate*2*120+64*1024) {
		return nil, errors.New("audio_base64 exceeds the 120 second PCM limit")
	}
	data, err := base64.StdEncoding.DecodeString(request.AudioBase64)
	if err != nil {
		return nil, errors.New("audio_base64 is not valid standard base64")
	}
	if s.instance == nil {
		return nil, errors.New("IMS signaling audio controller is unavailable")
	}
	stats, err := s.instance.PlaySignalingAudio(ctx, callID, voiceclient.SignalingAudioPlayback{
		Format: request.AudioFormat,
		Data:   data,
	})
	result := audioResultFromStats(stats)
	result.Format = strings.ToLower(strings.TrimSpace(request.AudioFormat))
	if err != nil {
		return result, err
	}
	return result, nil
}

func (s *controlServer) getRecording(request controlRequest) (*audioControlResult, error) {
	callID := strings.TrimSpace(request.CallID)
	if callID == "" {
		return nil, errors.New("call_id is required")
	}
	if s.instance == nil {
		return nil, errors.New("IMS signaling audio controller is unavailable")
	}
	recording, err := s.instance.SignalingAudioRecording(callID, request.AudioFormat)
	if err != nil {
		return nil, err
	}
	return &audioControlResult{
		CallID: recording.CallID, Format: recording.Format, ContentType: recording.ContentType,
		SampleRate: recording.SampleRate, Channels: recording.Channels,
		BitsPerSample: recording.BitsPerSample,
		DataBase64:    base64.StdEncoding.EncodeToString(recording.Data), Stats: recording.Stats,
	}, nil
}

func (s *controlServer) audioStats(callID string) (*audioControlResult, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil, errors.New("call_id is required")
	}
	if s.instance == nil {
		return nil, errors.New("IMS signaling audio controller is unavailable")
	}
	stats, err := s.instance.SignalingAudioStats(callID)
	if err != nil {
		return nil, err
	}
	return audioResultFromStats(stats), nil
}

func audioResultFromStats(stats voiceclient.SignalingAudioStats) *audioControlResult {
	return &audioControlResult{
		CallID: stats.CallID, SampleRate: voiceclient.SignalingAudioSampleRate,
		Channels: 1, BitsPerSample: 16, Stats: stats,
	}
}

func (s *controlServer) dialCall(ctx context.Context, phoneNumber string) (*voiceclient.SignalingCallStatus, error) {
	phoneNumber = strings.TrimSpace(phoneNumber)
	if phoneNumber == "" {
		return nil, errors.New("phone_number is required")
	}
	if s.instance == nil || !s.instance.State().IMSReady {
		return nil, errors.New("IMS registration over SWu is not ready")
	}
	status, err := s.instance.StartSignalingCall(ctx, phoneNumber)
	if err != nil {
		return nil, err
	}
	return &status, nil
}

func (s *controlServer) hangupCall(ctx context.Context, callID string) (*voiceclient.SignalingCallStatus, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil, errors.New("call_id is required")
	}
	if s.instance == nil {
		return nil, errors.New("IMS signaling call controller is unavailable")
	}
	status, err := s.instance.HangupSignalingCall(ctx, callID)
	if err != nil {
		return nil, err
	}
	return &status, nil
}

func (s *controlServer) sendSMS(parent context.Context, request controlRequest) (*smsSendResult, error) {
	phoneNumber := strings.TrimSpace(request.PhoneNumber)
	content := strings.TrimSpace(request.Content)
	if phoneNumber == "" || content == "" {
		return nil, errors.New("phone_number and content are required")
	}
	if strings.TrimSpace(s.smsc) == "" {
		return nil, errors.New("the selected SIM has no SMSC, so RP-DATA cannot be built")
	}
	state := s.instance.State()
	if !state.IMSReady || !state.SMSReady {
		return nil, errors.New("IMS registration and SMS over IMS are not ready")
	}
	encoding, err := smscodec.NormalizeSMSEncoding(request.Encoding)
	if err != nil {
		return nil, err
	}
	requestStatusReport := smsRequestStatusReport(request.RequestStatusReport)
	tpdus, _, err := smscodec.BuildSubmitTPDUsWithOptions(phoneNumber, content, smscodec.SubmitOptions{
		Encoding: encoding,
		// Match the captured 06:13 "A" request that the carrier accepted: its
		// SMS-SUBMIT first octet was 0x01, with TP-SRR clear. Callers may still
		// request a report explicitly for controlled delivery-report testing.
		RequestStatusReport: requestStatusReport,
	})
	if err != nil {
		return nil, fmt.Errorf("encode SMS TPDU: %w", err)
	}
	parts := make([]messaging.SMSPart, 0, len(tpdus))
	for _, tpdu := range tpdus {
		rpMR := byte(rpMRSequence.Add(1))
		parts = append(parts, messaging.SMSPart{RPMR: rpMR, Body: smscodec.BuildRPData(rpMR, tpdu, s.smsc)})
	}

	timeout := requestTimeout(request.TimeoutSeconds, 30*time.Second)
	sendContext, cancel := context.WithTimeout(parent, 20*time.Second)
	outcome, err := s.instance.SendSMS(sendContext, phoneNumber, content, parts)
	cancel()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		sipCode := sipCodeFromError(err.Error())
		s.report.mutate(func(status *tunnelStatus) {
			status.SMSLastTXAt = now
			status.SMSLastTXTo = phoneNumber
			status.SMSLastTXText = content
			status.SMSLastTXRPState = "submit_failed"
			status.SMSLastTXSIPCode = sipCode
			status.SMSLastTXError = err.Error()
			status.SMSTXVerified = false
		})
		return &smsSendResult{Verified: false, State: "failed", SIPCode: sipCode, Evidence: err.Error(), ObservedAt: now, PhoneNumber: phoneNumber, Content: content}, err
	}

	s.report.mutate(func(status *tunnelStatus) {
		status.SMSLastTXAt = now
		status.SMSLastTXTo = phoneNumber
		status.SMSLastTXText = content
		status.SMSLastTXMessageID = outcome.MessageID
		status.SMSLastTXSIPCode = 202
		status.SMSLastTXRPState = "pending"
		status.SMSLastTXRPCause = 0
		status.SMSLastTXError = ""
		status.SMSTXVerified = false
	})

	completed := make(chan *messaging.DeliveryStatus, 1)
	go func() {
		final, _ := s.store.wait(outcome.MessageID, 5*time.Minute)
		if final != nil {
			s.applyDeliveryStatus(phoneNumber, content, final)
		}
		completed <- final
	}()
	requestTimer := time.NewTimer(timeout)
	defer requestTimer.Stop()
	select {
	case final := <-completed:
		return sendResult(phoneNumber, content, final, now), nil
	case <-requestTimer.C:
		current, _ := s.store.GetSMSDeliveryStatus(outcome.MessageID)
		result := sendResult(phoneNumber, content, current, now)
		result.State = "incomplete"
		result.Evidence = "SIP 202 Accepted received; waiting for the asynchronous RP-ACK"
		return result, nil
	case <-parent.Done():
		return nil, parent.Err()
	}
}

func smsRequestStatusReport(requested *bool) bool {
	return requested != nil && *requested
}

func (s *controlServer) applyDeliveryStatus(phoneNumber, content string, delivery *messaging.DeliveryStatus) {
	if delivery == nil {
		return
	}
	rpCause := 0
	for _, part := range delivery.Parts {
		if part.RPCause != 0 {
			rpCause = part.RPCause
			break
		}
	}
	s.report.mutate(func(status *tunnelStatus) {
		status.SMSLastTXAt = delivery.UpdatedAt.UTC().Format(time.RFC3339Nano)
		status.SMSLastTXTo = phoneNumber
		status.SMSLastTXText = content
		status.SMSLastTXMessageID = delivery.MessageID
		status.SMSLastTXSIPCode = 202
		status.SMSLastTXRPState = delivery.State
		status.SMSLastTXRPCause = rpCause
		status.SMSLastTXError = delivery.LastError
		status.SMSTXVerified = delivery.State == "acked" && delivery.Acks >= delivery.PartsTotal
	})
}

func sendResult(phoneNumber, content string, delivery *messaging.DeliveryStatus, fallbackTime string) *smsSendResult {
	result := &smsSendResult{
		State: "incomplete", SIPCode: 202, RPState: "pending", Evidence: "SIP 202 Accepted received; waiting for RP-ACK",
		ObservedAt: fallbackTime, PhoneNumber: phoneNumber, Content: content,
	}
	if delivery == nil {
		return result
	}
	result.MessageID = delivery.MessageID
	result.PartsTotal = delivery.PartsTotal
	result.RPState = delivery.State
	result.ObservedAt = delivery.UpdatedAt.UTC().Format(time.RFC3339Nano)
	for _, part := range delivery.Parts {
		if part.RPCause != 0 {
			result.RPCause = part.RPCause
			break
		}
	}
	if delivery.State == "acked" && delivery.Acks >= delivery.PartsTotal {
		result.Verified = true
		result.State = "passed"
		result.Evidence = fmt.Sprintf("SMS over IMS verified: SIP 202 and RP-ACK received for %d/%d parts", delivery.Acks, delivery.PartsTotal)
	} else if delivery.State == "failed" {
		result.State = "failed"
		result.Evidence = delivery.LastError
		if result.Evidence == "" {
			result.Evidence = fmt.Sprintf("network returned RP-ERROR cause %d", result.RPCause)
		}
	}
	return result
}

func (s *controlServer) waitReceive(ctx context.Context, afterID string, timeout time.Duration) *smsReceiveResult {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		status, changed := s.report.snapshotAndWaitForChange()
		if result, ok := receivedSMSResultAfter(status, afterID); ok {
			return result
		}
		select {
		case <-changed:
		case <-deadline.C:
			status = s.report.snapshot()
			if result, ok := receivedSMSResultAfter(status, afterID); ok {
				return result
			}
			return &smsReceiveResult{
				Verified: false, State: "not_tested", MessageID: status.SMSLastRXID,
				Evidence:   "No new decoded SMS-DELIVER with a successful RP-ACK transaction was received before timeout",
				ObservedAt: status.SMSLastRXAt,
			}
		case <-ctx.Done():
			return &smsReceiveResult{Verified: false, State: "failed", Evidence: ctx.Err().Error()}
		}
	}
}

func receivedSMSResultAfter(status tunnelStatus, afterID string) (*smsReceiveResult, bool) {
	if message, ok := newestReceivedSMS(status.SMSReceivedMessages, afterID); ok {
		return receiveHistoryResult(message), true
	}
	if status.SMSRXVerified && status.SMSLastRXID != "" && (strings.TrimSpace(afterID) == "" || status.SMSLastRXID != strings.TrimSpace(afterID)) {
		return receiveResult(status), true
	}
	return nil, false
}

func newestReceivedSMS(messages []receivedSMSStatus, afterID string) (receivedSMSStatus, bool) {
	afterID = strings.TrimSpace(afterID)
	if len(messages) == 0 {
		return receivedSMSStatus{}, false
	}
	if afterID == "" {
		return messages[len(messages)-1], true
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].ID == afterID {
			if i+1 < len(messages) {
				return messages[len(messages)-1], true
			}
			return receivedSMSStatus{}, false
		}
	}
	latest := messages[len(messages)-1]
	return latest, latest.ID != ""
}

func receiveHistoryResult(message receivedSMSStatus) *smsReceiveResult {
	return &smsReceiveResult{
		Verified: true, State: "passed", MessageID: message.ID, From: message.From,
		Content: message.Text, RPMR: message.RPMR, RPAckCode: message.RPAckSIPCode,
		Evidence:   fmt.Sprintf("Decoded SMS-DELIVER and the network accepted RP-ACK with SIP %d", message.RPAckSIPCode),
		ObservedAt: message.ReceivedAt,
	}
}

func receiveResult(status tunnelStatus) *smsReceiveResult {
	return &smsReceiveResult{
		Verified: true, State: "passed", MessageID: status.SMSLastRXID, From: status.SMSLastRXFrom,
		Content: status.SMSLastRXText, RPMR: status.SMSLastRXRPMR, RPAckCode: status.SMSLastRXRPAckSIPCode,
		Evidence:   fmt.Sprintf("Decoded SMS-DELIVER and the network accepted RP-ACK with SIP %d", status.SMSLastRXRPAckSIPCode),
		ObservedAt: status.SMSLastRXAt,
	}
}

func requestTimeout(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	if seconds > 120 {
		seconds = 120
	}
	return time.Duration(seconds) * time.Second
}
