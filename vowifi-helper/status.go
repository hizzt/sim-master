package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/1239t/vowifi-go/runtimehost"
)

const maxRestoredStatusBytes = 4 << 20

type tunnelStatus struct {
	Stage                  string              `json:"stage"`
	Running                bool                `json:"running"`
	Established            bool                `json:"established"`
	PID                    int                 `json:"pid"`
	ModemID                string              `json:"modem_id,omitempty"`
	SerialDevice           string              `json:"serial_device,omitempty"`
	AccessInterface        string              `json:"access_interface,omitempty"`
	LocalIP                string              `json:"local_ip,omitempty"`
	EPDGFQDN               string              `json:"epdg_fqdn,omitempty"`
	EPDGIP                 string              `json:"epdg_ip,omitempty"`
	TunnelIPv4             string              `json:"tunnel_ipv4,omitempty"`
	TunnelIPv6             string              `json:"tunnel_ipv6,omitempty"`
	PCSCFv4                []string            `json:"pcscf_v4,omitempty"`
	PCSCFv6                []string            `json:"pcscf_v6,omitempty"`
	PCSCFAddress           string              `json:"pcscf_address,omitempty"`
	PCSCFOverride          string              `json:"pcscf_override,omitempty"`
	IMSIprefix             string              `json:"imsi_prefix,omitempty"`
	PhoneNumber            string              `json:"phone_number,omitempty"`
	SMSC                   string              `json:"smsc,omitempty"`
	ProxyEnabled           bool                `json:"proxy_enabled"`
	ProxyAddress           string              `json:"proxy_address,omitempty"`
	ProxyUsername          string              `json:"proxy_username,omitempty"`
	ProxyHasPassword       bool                `json:"proxy_has_password"`
	OuterTransport         string              `json:"outer_transport"`
	InnerTXPackets         uint64              `json:"inner_tx_packets"`
	InnerRXPackets         uint64              `json:"inner_rx_packets"`
	PCSCFProbeState        string              `json:"pcscf_probe_state"`
	PCSCFReachable         bool                `json:"pcscf_reachable"`
	PCSCFSIPCode           int                 `json:"pcscf_sip_code,omitempty"`
	PCSCFProbeSent         string              `json:"pcscf_probe_sent_at,omitempty"`
	PCSCFProbeReply        string              `json:"pcscf_probe_response_at,omitempty"`
	PCSCFProbeError        string              `json:"pcscf_probe_error,omitempty"`
	IMSState               string              `json:"ims_registration_state"`
	IMSRegistered          bool                `json:"ims_registered"`
	IMSAuthenticated       bool                `json:"ims_authenticated"`
	IMSTransport           string              `json:"ims_transport"`
	IMSSecurityMode        string              `json:"ims_security_mode,omitempty"`
	IMSIPSecEstablished    bool                `json:"ims_ipsec_established"`
	IMSErrorClass          string              `json:"ims_error_class,omitempty"`
	CarrierPresetID        string              `json:"carrier_preset_id,omitempty"`
	CarrierOverridesLoaded bool                `json:"carrier_overrides_loaded"`
	IMSCellID              string              `json:"ims_cell_id,omitempty"`
	IMSCellIDSource        string              `json:"ims_cell_id_source,omitempty"`
	IMSRegisterProfile     string              `json:"ims_register_profile,omitempty"`
	IMSUserAgent           string              `json:"ims_user_agent,omitempty"`
	SMSOverIMSReady        bool                `json:"sms_over_ims_ready"`
	SMSTXVerified          bool                `json:"sms_tx_path_verified"`
	SMSRXVerified          bool                `json:"sms_rx_path_verified"`
	SMSLastTXAt            string              `json:"sms_last_tx_at,omitempty"`
	SMSLastTXTo            string              `json:"sms_last_tx_to,omitempty"`
	SMSLastTXText          string              `json:"sms_last_tx_text,omitempty"`
	SMSLastTXMessageID     string              `json:"sms_last_tx_message_id,omitempty"`
	SMSLastTXSIPCode       int                 `json:"sms_last_tx_sip_code,omitempty"`
	SMSLastTXRPState       string              `json:"sms_last_tx_rp_state,omitempty"`
	SMSLastTXRPCause       int                 `json:"sms_last_tx_rp_cause,omitempty"`
	SMSLastTXError         string              `json:"sms_last_tx_error,omitempty"`
	SMSLastRXAt            string              `json:"sms_last_rx_at,omitempty"`
	SMSLastRXID            string              `json:"sms_last_rx_id,omitempty"`
	SMSLastRXFrom          string              `json:"sms_last_rx_from,omitempty"`
	SMSLastRXText          string              `json:"sms_last_rx_text,omitempty"`
	SMSLastRXRPMR          int                 `json:"sms_last_rx_rp_mr,omitempty"`
	SMSLastRXRPAckSIPCode  int                 `json:"sms_last_rx_rp_ack_sip_code,omitempty"`
	SMSLastRXState         string              `json:"sms_last_rx_state,omitempty"`
	SMSLastRXError         string              `json:"sms_last_rx_error,omitempty"`
	SMSReceivedMessages    []receivedSMSStatus `json:"sms_received_messages,omitempty"`
	StartedAt              string              `json:"started_at,omitempty"`
	UpdatedAt              string              `json:"updated_at"`
	Error                  string              `json:"error,omitempty"`
}

// receivedSMSStatus is appended only after a complete SMS-DELIVER has been
// decoded and the network has accepted its RP-ACK MESSAGE. Keeping a bounded
// history prevents the Rust status poller from losing bursts between polls.
type receivedSMSStatus struct {
	ID           string `json:"id"`
	From         string `json:"from"`
	Text         string `json:"text"`
	ReceivedAt   string `json:"received_at"`
	RPMR         int    `json:"rp_mr"`
	RPAckSIPCode int    `json:"rp_ack_sip_code"`
}

type reporter struct {
	mu      sync.Mutex
	path    string
	state   tunnelStatus
	changed chan struct{}
}

func newReporter(path string, state tunnelStatus) *reporter {
	return &reporter{path: path, state: state, changed: make(chan struct{})}
}

func (r *reporter) update(stage string, mutate func(*tunnelStatus)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.Stage = stage
	r.persistLocked(mutate)
}

func (r *reporter) mutate(mutate func(*tunnelStatus)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.persistLocked(mutate)
}

func (r *reporter) mutateIfChanged(mutate func(*tunnelStatus)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := cloneStatus(r.state)
	if mutate != nil {
		mutate(&next)
	}
	next.UpdatedAt = r.state.UpdatedAt
	if reflect.DeepEqual(next, r.state) {
		return
	}
	r.state = next
	r.persistLocked(nil)
}

func (r *reporter) fail(message string) {
	r.update("failed", func(status *tunnelStatus) {
		status.Running = false
		status.Error = message
	})
}

func (r *reporter) snapshot() tunnelStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneStatus(r.state)
}

// snapshotAndWaitForChange returns a state snapshot and the notification
// channel for that exact state generation under one lock. Callers must use the
// pair together to avoid missing an update between a snapshot and subscription.
func (r *reporter) snapshotAndWaitForChange() (tunnelStatus, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneStatus(r.state), r.changed
}

func (r *reporter) waitForChange() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changed
}

func restoreReceivedSMSState(path string, state *tunnelStatus) {
	if state == nil || strings.TrimSpace(path) == "" {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	var previous tunnelStatus
	if err := json.NewDecoder(io.LimitReader(file, maxRestoredStatusBytes)).Decode(&previous); err != nil {
		return
	}

	restored := tunnelStatus{}
	for _, message := range previous.SMSReceivedMessages {
		if validReceivedSMSStatus(message) {
			appendReceivedSMS(&restored, message)
		}
	}
	if previous.SMSRXVerified && previous.SMSLastRXState == "rp_acked" {
		message := receivedSMSStatus{
			ID:           previous.SMSLastRXID,
			From:         previous.SMSLastRXFrom,
			Text:         previous.SMSLastRXText,
			ReceivedAt:   previous.SMSLastRXAt,
			RPMR:         previous.SMSLastRXRPMR,
			RPAckSIPCode: previous.SMSLastRXRPAckSIPCode,
		}
		if validReceivedSMSStatus(message) {
			appendReceivedSMS(&restored, message)
		}
	}
	if len(restored.SMSReceivedMessages) == 0 {
		return
	}

	state.SMSReceivedMessages = restored.SMSReceivedMessages
	latest := restored.SMSReceivedMessages[len(restored.SMSReceivedMessages)-1]
	state.SMSRXVerified = true
	state.SMSLastRXAt = latest.ReceivedAt
	state.SMSLastRXID = latest.ID
	state.SMSLastRXFrom = latest.From
	state.SMSLastRXText = latest.Text
	state.SMSLastRXRPMR = latest.RPMR
	state.SMSLastRXRPAckSIPCode = latest.RPAckSIPCode
	state.SMSLastRXState = "rp_acked"
	state.SMSLastRXError = ""
}

func validReceivedSMSStatus(message receivedSMSStatus) bool {
	return strings.TrimSpace(message.ID) != "" &&
		strings.TrimSpace(message.From) != "" &&
		message.RPAckSIPCode >= 200 && message.RPAckSIPCode < 300
}

func (r *reporter) persistLocked(mutate func(*tunnelStatus)) {
	if mutate != nil {
		mutate(&r.state)
	}
	r.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	close(r.changed)
	r.changed = make(chan struct{})
	encoded, err := json.Marshal(r.state)
	if err == nil {
		fmt.Println(string(encoded))
	}
	if err := writeStatus(r.path, r.state); err != nil {
		fmt.Fprintf(os.Stderr, "write status: %v\n", err)
	}
}

func (r *reporter) applyRuntimeState(state runtimehost.State, instance *runtimehost.Instance) {
	r.mutateIfChanged(func(status *tunnelStatus) {
		status.Running = true
		status.Established = state.TunnelReady
		status.TunnelIPv4 = state.TunnelIPv4
		status.TunnelIPv6 = state.TunnelIPv6
		status.PCSCFv4 = append([]string(nil), state.PCSCFv4...)
		status.PCSCFv6 = append([]string(nil), state.PCSCFv6...)
		status.InnerTXPackets = state.InnerTXPackets
		status.InnerRXPackets = state.InnerRXPackets
		if phoneNumber := strings.TrimSpace(state.PhoneNumber); phoneNumber != "" {
			status.PhoneNumber = phoneNumber
		}
		if state.EPDGIP != "" {
			status.EPDGIP = state.EPDGIP
		}
		status.IMSErrorClass = state.LastErrorClass
		if state.LastError != "" {
			status.Error = state.LastError
		}

		switch {
		case state.IMSReady && state.SMSReady:
			status.Stage = "established"
			status.PCSCFProbeState = "responded"
			status.PCSCFReachable = true
			status.PCSCFSIPCode = 200
			status.PCSCFProbeReply = state.UpdatedAt.UTC().Format(time.RFC3339Nano)
			status.IMSState = "registered"
			status.IMSRegistered = true
			status.IMSAuthenticated = true
			status.IMSTransport = "swu"
			status.SMSOverIMSReady = true
			status.PCSCFProbeError = ""
			status.Error = ""
		case state.TunnelReady:
			status.Stage = "established"
			status.PCSCFProbeState = "sent"
			status.PCSCFProbeSent = state.UpdatedAt.UTC().Format(time.RFC3339Nano)
			status.IMSState = "registering"
			status.IMSTransport = "swu"
			if state.LastErrorClass == "ims" {
				status.PCSCFProbeState = "failed"
				status.PCSCFProbeError = state.LastError
				if strings.Contains(strings.ToLower(state.LastError), "service not allowed in this location") {
					status.IMSState = "register_location_rejected"
				} else {
					status.IMSState = "register_failed"
				}
				if code := sipCodeFromError(state.LastError); code != 0 {
					status.PCSCFReachable = true
					status.PCSCFSIPCode = code
					status.PCSCFProbeReply = state.UpdatedAt.UTC().Format(time.RFC3339Nano)
				}
			}
		default:
			status.Stage = "authenticating"
			status.IMSState = "not_started"
			if state.LastError != "" {
				status.Stage = "failed"
			}
		}

		if instance != nil {
			obs := instance.Obs()
			if registrar, ok := obs["registrar"].(string); ok {
				status.PCSCFAddress = registrar
			}
			if mode, ok := obs["effective_security"].(string); ok {
				status.IMSSecurityMode = mode
			}
			if installed, ok := obs["ipsec_installed"].(bool); ok {
				status.IMSIPSecEstablished = installed
			}
		}
	})
}

func cloneStatus(status tunnelStatus) tunnelStatus {
	status.PCSCFv4 = append([]string(nil), status.PCSCFv4...)
	status.PCSCFv6 = append([]string(nil), status.PCSCFv6...)
	status.SMSReceivedMessages = append([]receivedSMSStatus(nil), status.SMSReceivedMessages...)
	return status
}

func writeStatus(path string, state tunnelStatus) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vowifi-tunnel-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := json.NewEncoder(tmp).Encode(state); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

var sipStatusCodePattern = regexp.MustCompile(`(?i)(?:\bSIP\s+|\bstatus[=: ]+|\bresponse[=: ]+)([1-6][0-9]{2})\b`)
var sipResponseCodePattern = regexp.MustCompile(`\b([1-6][0-9]{2})\s+response\b`)

func sipCodeFromError(message string) int {
	match := sipStatusCodePattern.FindStringSubmatch(message)
	if len(match) != 2 {
		match = sipResponseCodePattern.FindStringSubmatch(message)
	}
	if len(match) != 2 {
		return 0
	}
	code, err := strconv.Atoi(match[1])
	if err == nil && code >= 100 && code <= 699 {
		return code
	}
	return 0
}
