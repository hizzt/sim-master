package main

import (
	"sync"
	"time"

	"github.com/1239t/vowifi-go/runtimehost/messaging"
)

type deliveryRecord struct {
	status messaging.DeliveryStatus
	parts  map[int]messaging.DeliveryPartStatus
}

type memoryDeliveryStore struct {
	mu         sync.Mutex
	deliveries map[string]*deliveryRecord
	changed    chan struct{}
}

func newMemoryDeliveryStore() *memoryDeliveryStore {
	return &memoryDeliveryStore{
		deliveries: make(map[string]*deliveryRecord),
		changed:    make(chan struct{}),
	}
}

func (s *memoryDeliveryStore) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *memoryDeliveryStore) CreateSMSDelivery(messageID, imsi, deviceID, peer, content string, partsTotal int, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveries[messageID] = &deliveryRecord{
		status: messaging.DeliveryStatus{
			MessageID: messageID, IMSI: imsi, DeviceID: deviceID, Peer: peer,
			Content: content, PartsTotal: partsTotal, State: "pending", CreatedAt: at, UpdatedAt: at,
		},
		parts: make(map[int]messaging.DeliveryPartStatus),
	}
	s.notifyLocked()
	return nil
}

func (s *memoryDeliveryStore) UpsertSMSDeliveryPart(messageID string, partNo int, callID string, rpMR int, state string, sentAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.deliveries[messageID]
	if !ok {
		return messaging.ErrDeliveryNotFound
	}
	part := record.parts[partNo]
	part.PartNo = partNo
	part.CallID = callID
	part.RPMR = rpMR
	part.State = state
	part.SentAt = sentAt
	if part.CreatedAt.IsZero() {
		part.CreatedAt = sentAt
	}
	part.UpdatedAt = sentAt
	record.parts[partNo] = part
	record.status.UpdatedAt = sentAt
	s.notifyLocked()
	return nil
}

func (s *memoryDeliveryStore) MarkSMSDeliveryPartReport(inReplyTo, callID, deviceID string, rpMR int, state string, sipCode int, rpCause int, errText string, at time.Time) (messaging.DeliveryPartMatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for messageID, record := range s.deliveries {
		if deviceID != "" && record.status.DeviceID != "" && record.status.DeviceID != deviceID {
			continue
		}
		for partNo, part := range record.parts {
			callMatches := inReplyTo != "" && part.CallID == inReplyTo
			if !callMatches && callID != "" {
				callMatches = part.CallID == callID
			}
			if !callMatches && part.RPMR != rpMR {
				continue
			}
			part.InReplyTo = inReplyTo
			part.State = state
			part.SIPCode = sipCode
			part.RPCause = rpCause
			part.RPCauseText = smsRPCauseText(rpCause)
			part.ErrorText = errText
			part.ReportAt = timePointer(at)
			part.UpdatedAt = at
			record.parts[partNo] = part
			recomputeRecord(record, at)
			s.notifyLocked()
			return messaging.DeliveryPartMatch{MessageID: messageID, PartNo: partNo, State: state}, nil
		}
	}
	return messaging.DeliveryPartMatch{}, messaging.ErrDeliveryNotFound
}

func smsRPCauseText(cause int) string {
	switch cause {
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
	case 28:
		return "Unidentified subscriber"
	case 41:
		return "Temporary failure"
	default:
		if cause == 0 {
			return ""
		}
		return "unknown cause"
	}
}

func (s *memoryDeliveryStore) RecomputeSMSDelivery(messageID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.deliveries[messageID]
	if !ok {
		return messaging.ErrDeliveryNotFound
	}
	recomputeRecord(record, at)
	s.notifyLocked()
	return nil
}

func (s *memoryDeliveryStore) UpdateSMSDeliveryState(messageID, state, lastError string, acks int, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.deliveries[messageID]
	if !ok {
		return messaging.ErrDeliveryNotFound
	}
	record.status.State = state
	record.status.LastError = lastError
	record.status.Acks = acks
	record.status.UpdatedAt = at
	s.notifyLocked()
	return nil
}

func (s *memoryDeliveryStore) GetSMSDeliveryStatus(messageID string) (*messaging.DeliveryStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.deliveries[messageID]
	if !ok {
		return nil, messaging.ErrDeliveryNotFound
	}
	status := record.status
	status.Parts = make([]messaging.DeliveryPartStatus, 0, len(record.parts))
	for partNo := 0; partNo < status.PartsTotal; partNo++ {
		if part, ok := record.parts[partNo]; ok {
			status.Parts = append(status.Parts, part)
		}
	}
	return &status, nil
}

func (s *memoryDeliveryStore) wait(messageID string, timeout time.Duration) (*messaging.DeliveryStatus, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if status, err := s.GetSMSDeliveryStatus(messageID); err == nil {
			if status.State == "acked" || status.State == "failed" {
				return status, true
			}
		}
		s.mu.Lock()
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			status, _ := s.GetSMSDeliveryStatus(messageID)
			return status, false
		}
	}
}

func recomputeRecord(record *deliveryRecord, at time.Time) {
	acks := 0
	failed := false
	lastError := ""
	for _, part := range record.parts {
		switch part.State {
		case "acked":
			acks++
		case "failed":
			failed = true
			lastError = part.ErrorText
			if lastError == "" && part.RPCause != 0 {
				lastError = "RP-ERROR"
			}
		}
	}
	record.status.Acks = acks
	record.status.LastError = lastError
	record.status.UpdatedAt = at
	switch {
	case failed:
		record.status.State = "failed"
	case record.status.PartsTotal > 0 && acks >= record.status.PartsTotal:
		record.status.State = "acked"
	default:
		record.status.State = "pending"
	}
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
