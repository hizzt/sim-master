package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/techblack/sim-master/vowifi-helper/smscodec"
	"github.com/1239t/vowifi-go/runtimehost/messaging"
)

const maxReceivedSMSHistory = 256

type decodedInbound struct {
	id       string
	sender   string
	text     string
	at       time.Time
	rpMR     byte
	complete bool
}

type inboundDecoder struct {
	mu          sync.Mutex
	report      *reporter
	reassembler *smscodec.Reassembler
	pending     map[string]decodedInbound
}

func newInboundDecoder(report *reporter) *inboundDecoder {
	return &inboundDecoder{
		report:      report,
		reassembler: smscodec.NewReassembler(),
		pending:     make(map[string]decodedInbound),
	}
}

func (d *inboundDecoder) handle(inbound messaging.InboundSMS) (messaging.InboundSMSReply, error) {
	if len(inbound.Body) < 2 || inbound.Body[0] != 0x01 {
		var rpMR byte
		if len(inbound.Body) > 1 {
			rpMR = inbound.Body[1]
		}
		err := fmt.Errorf("expected network-to-MS RP-DATA (mti=0x01)")
		d.report.mutate(func(status *tunnelStatus) {
			resetCurrentInboundStatus(status)
			status.SMSLastRXAt = inbound.ReceivedAt.UTC().Format(time.RFC3339Nano)
			status.SMSLastRXID = inbound.ID
			status.SMSLastRXRPMR = int(rpMR)
			status.SMSLastRXState = "decode_failed"
			status.SMSLastRXError = err.Error()
		})
		return messaging.InboundSMSReply{Body: smscodec.BuildRPError(rpMR, smscodec.RPCauseTemporaryFailure)}, nil
	}
	rpMR, _, _, tpduBytes, err := smscodec.ParseRPDataWithAddresses(inbound.Body)
	if err != nil {
		if len(inbound.Body) > 1 {
			rpMR = inbound.Body[1]
		}
		d.report.mutate(func(status *tunnelStatus) {
			resetCurrentInboundStatus(status)
			status.SMSLastRXAt = inbound.ReceivedAt.UTC().Format(time.RFC3339Nano)
			status.SMSLastRXID = inbound.ID
			status.SMSLastRXRPMR = int(rpMR)
			status.SMSLastRXState = "decode_failed"
			status.SMSLastRXError = err.Error()
		})
		return messaging.InboundSMSReply{Body: smscodec.BuildRPError(rpMR, smscodec.RPCauseTemporaryFailure)}, nil
	}

	sender, text, sentAt, concat, err := smscodec.DecodeDeliverTPDU(tpduBytes)
	if err != nil {
		d.report.mutate(func(status *tunnelStatus) {
			resetCurrentInboundStatus(status)
			status.SMSLastRXAt = inbound.ReceivedAt.UTC().Format(time.RFC3339Nano)
			status.SMSLastRXID = inbound.ID
			status.SMSLastRXRPMR = int(rpMR)
			status.SMSLastRXState = "decode_failed"
			status.SMSLastRXError = err.Error()
		})
		return messaging.InboundSMSReply{Body: smscodec.BuildRPError(rpMR, smscodec.RPCauseTemporaryFailure)}, nil
	}
	complete, fullText := d.reassembler.Add(sender, concat, text)
	if complete {
		text = fullText
	}
	if sentAt.IsZero() {
		sentAt = inbound.ReceivedAt
	}
	decoded := decodedInbound{
		id: inbound.ID, sender: sender, text: text, at: sentAt, rpMR: rpMR, complete: complete,
	}
	d.mu.Lock()
	d.pending[inbound.ID] = decoded
	d.mu.Unlock()
	d.report.mutate(func(status *tunnelStatus) {
		resetCurrentInboundStatus(status)
		status.SMSLastRXAt = inbound.ReceivedAt.UTC().Format(time.RFC3339Nano)
		status.SMSLastRXID = inbound.ID
		status.SMSLastRXFrom = sender
		status.SMSLastRXText = text
		status.SMSLastRXRPMR = int(rpMR)
		status.SMSLastRXState = "rp_ack_pending"
		status.SMSLastRXError = ""
	})
	return messaging.InboundSMSReply{Body: smscodec.BuildRPAck(rpMR)}, nil
}

func (d *inboundDecoder) handleResult(inbound messaging.InboundSMS, _ messaging.InboundSMSReply, sipCode int, err error) {
	d.mu.Lock()
	decoded, ok := d.pending[inbound.ID]
	delete(d.pending, inbound.ID)
	d.mu.Unlock()
	if !ok {
		return
	}
	d.report.mutate(func(status *tunnelStatus) {
		isCurrent := status.SMSLastRXID == decoded.id
		if err != nil || sipCode < 200 || sipCode >= 300 {
			if isCurrent {
				status.SMSRXVerified = false
				status.SMSLastRXRPAckSIPCode = sipCode
				status.SMSLastRXState = "rp_ack_failed"
				if err != nil {
					status.SMSLastRXError = err.Error()
				} else {
					status.SMSLastRXError = fmt.Sprintf("RP-ACK SIP response %d", sipCode)
				}
			}
			return
		}
		if decoded.complete {
			appendReceivedSMS(status, receivedSMSStatus{
				ID:           decoded.id,
				From:         decoded.sender,
				Text:         decoded.text,
				ReceivedAt:   decoded.at.UTC().Format(time.RFC3339Nano),
				RPMR:         int(decoded.rpMR),
				RPAckSIPCode: sipCode,
			})
		}
		if isCurrent {
			status.SMSLastRXRPAckSIPCode = sipCode
			status.SMSLastRXState = "rp_acked"
			status.SMSLastRXError = ""
			status.SMSRXVerified = decoded.complete
		}
		if isCurrent && decoded.complete {
			status.SMSRXVerified = true
			status.SMSLastRXID = decoded.id
			status.SMSLastRXFrom = decoded.sender
			status.SMSLastRXText = decoded.text
			status.SMSLastRXRPMR = int(decoded.rpMR)
			status.SMSLastRXAt = decoded.at.UTC().Format(time.RFC3339Nano)
		}
	})
}

func resetCurrentInboundStatus(status *tunnelStatus) {
	status.SMSRXVerified = false
	status.SMSLastRXFrom = ""
	status.SMSLastRXText = ""
	status.SMSLastRXRPAckSIPCode = 0
	status.SMSLastRXState = ""
	status.SMSLastRXError = ""
}

func appendReceivedSMS(status *tunnelStatus, message receivedSMSStatus) {
	if strings.TrimSpace(message.ID) == "" {
		return
	}
	for i := range status.SMSReceivedMessages {
		if status.SMSReceivedMessages[i].ID == message.ID {
			status.SMSReceivedMessages[i] = message
			return
		}
	}
	status.SMSReceivedMessages = append(status.SMSReceivedMessages, message)
	if overflow := len(status.SMSReceivedMessages) - maxReceivedSMSHistory; overflow > 0 {
		status.SMSReceivedMessages = append([]receivedSMSStatus(nil), status.SMSReceivedMessages[overflow:]...)
	}
}
