package audit

import (
	"context"
	"fmt"
	"github.com/xbuyan/Escrowd/internal/store"
	"time"
)

type EventType string

const (
	EventLocked   EventType = "ESCROW_LOCKED"
	EventClaimed  EventType = "ESCROW_CLAIMED"
	EventRefunded EventType = "ESCROW_REFUNDED"
	EventDisputed EventType = "DISPUTE_RAISED"
	EventEvidence EventType = "EVIDENCE_SUBMITTED"
	EventResolved EventType = "DISPUTE_RESOLVED"
	EventExpired  EventType = "ESCROW_EXPIRED"
)

type Entry struct {
	ID        string
	EscrowID  string
	Event     EventType
	ActorID   string
	ActorName string
	Detail    string
	Timestamp time.Time
}

type Log struct {
	auditStore *store.AuditStore
}

func New(auditStore *store.AuditStore) *Log {
	return &Log{auditStore: auditStore}
}

func (l *Log) Record(escrowID string, event EventType, actorID string, actorName string, detail string) error {
	return l.auditStore.Record(escrowID, string(event), actorID, actorName, detail)
}

func (l *Log) GetByEscrow(escrowID string) ([]Entry, error) {
	rows, err := l.auditStore.GetByEscrow(escrowID)
	if err != nil {
		return nil, err
	}

	var entries []Entry
	for _, r := range rows {
		entries = append(entries, Entry{
			ID:        r.ID,
			EscrowID:  r.EscrowID,
			Event:     EventType(r.Event),
			ActorID:   r.ActorID,
			ActorName: r.ActorName,
			Detail:    r.Detail,
			Timestamp: r.Timestamp,
		})
	}

	return entries, nil
}

// Ensure unused import is used
var _ = fmt.Sprintf
var _ = context.Background
var _ = time.Now
