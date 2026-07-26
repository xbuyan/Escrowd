package escrow

import (
	"errors"
	"fmt"
	"github.com/xbuyan/Escrowd/internal/crypto"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusLocked   Status = "locked"
	StatusClaimed  Status = "claimed"
	StatusRefunded Status = "refunded"
	StatusDisputed Status = "disputed"
	StatusResolved Status = "resolved"
)

type Dispute struct {
	ID           string
	Reason       string
	Evidence     []string
	RaisedByID   string
	RaisedByName string
	RaisedAt     time.Time
	ResolvedAt   time.Time
	Resolution   string
	Priority     bool
	PayReference string
}

type Escrow struct {
	ID                  string
	Title               string
	Description         string
	Currency            string
	SenderID            string
	SenderName          string
	ReceiverID          string
	ReceiverName        string
	ReceiverStellarAddr string
	Amount              int
	Status              Status
	HashLock            string
	CreatedAt           time.Time
	ExpiresAt           time.Time
	Dispute             *Dispute
	Signature           string
	StellarWallet       string
	StellarWalletSecret string
	StellarFunded       bool
	StellarTxHash       string
	StellarBalanceID    string

	// Invitation fields — support deal-sharing between web users
	InviteToken    string // unique token shared with counterparty
	ReceiverJoined bool   // true once counterparty has accepted the invite
	ReceiverEmail  string // counterparty's email, for sending the invite
}

// New creates a new escrow deal. The receiverID/receiverName may be a
// placeholder (e.g. an email or display name) until the counterparty
// joins via the invite link, at which point ReceiverJoined becomes true
// and ReceiverID is updated to their real user ID.
func New(senderID string, senderName string, receiverID string, receiverName string, amount int, secret string) Escrow {
	now := time.Now()
	id := uuid.NewString()
	deal := Escrow{
		ID:           id,
		SenderID:     senderID,
		SenderName:   senderName,
		ReceiverID:   receiverID,
		ReceiverName: receiverName,
		Amount:       amount,
		Status:       StatusLocked,
		HashLock:     crypto.HashSecret(secret),
		CreatedAt:    now,
		ExpiresAt:    now.Add(48 * time.Hour),
		InviteToken:  "inv-" + uuid.NewString(),
	}
	deal.Signature = generateSignature(id, senderID, receiverID, amount, now)
	return deal
}

func Claim(deal *Escrow, guess string) error {
	if deal.Status == StatusDisputed {
		return errors.New("escrow is under dispute — cannot claim")
	}
	if deal.Status != StatusLocked {
		return errors.New("escrow is not locked")
	}
	if !crypto.CheckSecret(deal.HashLock, guess) {
		return errors.New("wrong secret")
	}
	deal.Status = StatusClaimed
	return nil
}

// the refund function gets called only on a locked deal and once its executed,
// one cannot revert since a state machine only moves forward.
func Refund(deal *Escrow) error {
	if deal.Status == StatusDisputed {
		return errors.New("escrow is under dispute — cannot refund directly")
	}
	if deal.Status != StatusLocked {
		return errors.New("escrow is not locked")
	}
	deal.Status = StatusRefunded
	return nil
}

func RaiseDispute(deal *Escrow, raisedByID string, raisedByName string, reason string) error {
	if deal.Status != StatusLocked {
		return errors.New("can only dispute a locked escrow")
	}
	if deal.Dispute != nil {
		return errors.New("dispute already exists for this escrow")
	}
	deal.Dispute = &Dispute{
		ID:           "d-" + uuid.NewString()[:8],
		Reason:       reason,
		RaisedByID:   raisedByID,
		RaisedByName: raisedByName,
		RaisedAt:     time.Now(),
		Evidence:     []string{},
	}
	deal.Status = StatusDisputed
	return nil
}

func AddEvidence(deal *Escrow, submittedByID string, submittedByName string, link string) error {
	if deal.Status != StatusDisputed {
		return errors.New("no active dispute on this escrow")
	}
	entry := submittedByName + " (" + submittedByID + "): " + link
	deal.Dispute.Evidence = append(deal.Dispute.Evidence, entry)
	return nil
}

func ResolveDispute(deal *Escrow, resolution string) error {
	if deal.Status != StatusDisputed {
		return errors.New("no active dispute on this escrow")
	}
	deal.Dispute.Resolution = resolution
	deal.Dispute.ResolvedAt = time.Now()
	deal.Status = StatusResolved
	if resolution == "refund" {
		deal.Status = StatusRefunded
	} else if resolution == "release" {
		deal.Status = StatusClaimed
	}
	return nil
}

// JoinDeal marks the counterparty as having accepted the invitation.
// Updates ReceiverID/ReceiverName to the joining user's real identity
// if the deal was created with a placeholder receiver.
func JoinDeal(deal *Escrow, receiverID, receiverName string) error {
	if deal.ReceiverJoined {
		return errors.New("counterparty has already joined this deal")
	}
	if deal.SenderID == receiverID {
		return errors.New("you cannot join your own deal")
	}
	deal.ReceiverID = receiverID
	deal.ReceiverName = receiverName
	deal.ReceiverJoined = true
	return nil
}

func IsExpired(deal Escrow) bool {
	return time.Now().After(deal.ExpiresAt)
}

func generateSignature(id string, senderID string, receiverID string, amount int, createdAt time.Time) string {
	data := fmt.Sprintf("%s:%s:%s:%d:%d", id, senderID, receiverID, amount, createdAt.Unix())
	return crypto.HashSecret(data)
}

func VerifySignature(deal Escrow) bool {
	expected := generateSignature(deal.ID, deal.SenderID, deal.ReceiverID, deal.Amount, deal.CreatedAt)
	return expected == deal.Signature
}
