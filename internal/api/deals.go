package api

import (
	"escrowd/internal/escrow"
	"escrowd/internal/validator"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type DealResponse struct {
	ID               string         `json:"id"`
	Title            string         `json:"title"`
	Role             string         `json:"role"`
	Counterparty     string         `json:"counterparty"`
	Amount           string         `json:"amount"`
	Currency         string         `json:"currency"`
	Status           string         `json:"status"`
	Expires          string         `json:"expires"`
	StellarBalanceID string         `json:"stellar_balance_id,omitempty"`
	StellarTxHash    string         `json:"stellar_tx_hash,omitempty"`
	Timeline         []TimelineStep `json:"timeline"`
	Evidence         []EvidenceItem `json:"evidence,omitempty"`
}

type TimelineStep struct {
	Label string `json:"label"`
	Sub   string `json:"sub"`
	State string `json:"state"`
}

type EvidenceItem struct {
	Actor     string `json:"actor"`
	Role      string `json:"role"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

func handleDeals(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		listDeals(w, r)
	case http.MethodPost:
		createDeal(w, r)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleDealByID(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	id := parts[2]
	action := ""
	if len(parts) >= 4 {
		action = parts[3]
	}

	switch {
	case r.Method == http.MethodGet && action == "":
		getDealHandler(w, r, id)
	case r.Method == http.MethodPost && action == "claim":
		claimDeal(w, r, id)
	case r.Method == http.MethodPost && action == "refund":
		refundDeal(w, r, id)
	case r.Method == http.MethodPost && action == "dispute":
		raiseDispute(w, r, id)
	case r.Method == http.MethodPost && action == "evidence":
		submitEvidence(w, r, id)
	default:
		jsonError(w, "not found", http.StatusNotFound)
	}
}

func listDeals(w http.ResponseWriter, r *http.Request) {
	userID, _ := userFromRequest(r)
	ids, err := db.ListIDs()
	if err != nil {
		jsonError(w, "could not list deals", http.StatusInternalServerError)
		return
	}
	var deals []DealResponse
	for _, id := range ids {
		deal, err := db.Get(id)
		if err != nil {
			continue
		}
		if deal.SenderID != userID && deal.ReceiverID != userID {
			continue
		}
		deals = append(deals, toResponse(deal, userID))
	}
	if deals == nil {
		deals = []DealResponse{}
	}
	jsonOK(w, deals)
}

func getDealHandler(w http.ResponseWriter, r *http.Request, id string) {
	userID, _ := userFromRequest(r)

	// Validate deal ID format
	if err := validator.ValidateID(id); err != nil {
		jsonError(w, "invalid deal ID", http.StatusBadRequest)
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		jsonError(w, "deal not found", http.StatusNotFound)
		return
	}
	entries, _ := db.AuditDB.GetByEscrow(id)
	var evidence []EvidenceItem
	for _, e := range entries {
		if e.Event == "evidence_submitted" {
			role := "seller"
			if e.ActorID == deal.SenderID {
				role = "buyer"
			}
			evidence = append(evidence, EvidenceItem{
				Actor:     e.ActorName,
				Role:      role,
				Text:      e.Detail,
				CreatedAt: e.Timestamp.Format("Jan 2, 2006 · 15:04"),
			})
		}
	}
	resp := toResponse(deal, userID)
	resp.Evidence = evidence
	jsonOK(w, resp)
}

func createDeal(w http.ResponseWriter, r *http.Request) {
	userID, userName := userFromRequest(r)

	var body struct {
		Role         string `json:"role"`
		Counterparty string `json:"counterparty"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		Amount       string `json:"amount"`
		Currency     string `json:"currency"`
		Expiry       string `json:"expiry"`
	}
	if err := decode(r, &body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Sanitise
	body.Title = strings.TrimSpace(body.Title)
	body.Counterparty = strings.TrimSpace(body.Counterparty)
	body.Description = strings.TrimSpace(body.Description)
	body.Currency = strings.TrimSpace(body.Currency)

	// Validate title
	if body.Title == "" {
		jsonError(w, "title is required", http.StatusBadRequest)
		return
	}
	if len(body.Title) > 100 {
		jsonError(w, "title too long — maximum 100 characters", http.StatusBadRequest)
		return
	}

	// Validate description
	if len(body.Description) > 1000 {
		jsonError(w, "description too long — maximum 1000 characters", http.StatusBadRequest)
		return
	}

	// Validate counterparty
	if err := validator.ValidateName(body.Counterparty); err != nil {
		jsonError(w, "invalid counterparty: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate currency
	allowed := map[string]bool{"XLM": true, "KES": true, "USD": true}
	if !allowed[body.Currency] {
		jsonError(w, "currency must be XLM, KES, or USD", http.StatusBadRequest)
		return
	}

	// Validate role
	if body.Role != "buyer" && body.Role != "seller" {
		jsonError(w, "role must be buyer or seller", http.StatusBadRequest)
		return
	}

	// Validate amount
	var amountInt int
	fmt.Sscanf(body.Amount, "%d", &amountInt)
	if err := validator.ValidateAmount(amountInt); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Rate limit check
	if !limiter.Allow(userID) {
		jsonError(w, "rate limit exceeded — maximum 10 operations per hour", http.StatusTooManyRequests)
		return
	}

	senderID, senderName := userID, userName
	receiverID, receiverName := body.Counterparty, body.Counterparty
	if body.Role == "seller" {
		senderID, senderName = body.Counterparty, body.Counterparty
		receiverID, receiverName = userID, userName
	}

	deal := escrow.New(senderID, senderName, receiverID, receiverName, amountInt, "web-init")
	if err := db.Save(deal); err != nil {
		jsonError(w, "could not save deal", http.StatusInternalServerError)
		return
	}

	auditLog.Record(deal.ID, "deal_created", userID, userName,
		fmt.Sprintf("Created via web: %s %s %s", body.Title, body.Amount, body.Currency))

	jsonOK(w, toResponse(deal, userID))
}

func claimDeal(w http.ResponseWriter, r *http.Request, id string) {
	userID, userName := userFromRequest(r)

	if err := validator.ValidateID(id); err != nil {
		jsonError(w, "invalid deal ID", http.StatusBadRequest)
		return
	}

	if !limiter.Allow(userID) {
		jsonError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		jsonError(w, "deal not found", http.StatusNotFound)
		return
	}

	// Only the receiver (buyer) can mark complete
	if deal.ReceiverID != userID {
		jsonError(w, "only the buyer can mark a deal complete", http.StatusForbidden)
		return
	}

	if err := escrow.Claim(&deal, ""); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := db.Save(deal); err != nil {
		jsonError(w, "could not save deal", http.StatusInternalServerError)
		return
	}

	auditLog.Record(id, "deal_claimed", userID, userName, "Marked complete via web")
	jsonOK(w, toResponse(deal, userID))
}

func refundDeal(w http.ResponseWriter, r *http.Request, id string) {
	userID, userName := userFromRequest(r)

	if err := validator.ValidateID(id); err != nil {
		jsonError(w, "invalid deal ID", http.StatusBadRequest)
		return
	}

	if !limiter.Allow(userID) {
		jsonError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		jsonError(w, "deal not found", http.StatusNotFound)
		return
	}

	// Only the sender (buyer) can request refund
	if deal.SenderID != userID {
		jsonError(w, "only the buyer can request a refund", http.StatusForbidden)
		return
	}

	if err := escrow.Refund(&deal); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := db.Save(deal); err != nil {
		jsonError(w, "could not save deal", http.StatusInternalServerError)
		return
	}

	auditLog.Record(id, "deal_refunded", userID, userName, "Refund via web")
	jsonOK(w, toResponse(deal, userID))
}

func raiseDispute(w http.ResponseWriter, r *http.Request, id string) {
	userID, userName := userFromRequest(r)

	if err := validator.ValidateID(id); err != nil {
		jsonError(w, "invalid deal ID", http.StatusBadRequest)
		return
	}

	if !limiter.Allow(userID) {
		jsonError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	decode(r, &body)
	body.Reason = strings.TrimSpace(body.Reason)

	if err := validator.ValidateReason(body.Reason); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		jsonError(w, "deal not found", http.StatusNotFound)
		return
	}

	// Only participants can raise a dispute
	if deal.SenderID != userID && deal.ReceiverID != userID {
		jsonError(w, "you are not a participant in this deal", http.StatusForbidden)
		return
	}

	if err := escrow.RaiseDispute(&deal, userID, userName, body.Reason); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := db.Save(deal); err != nil {
		jsonError(w, "could not save deal", http.StatusInternalServerError)
		return
	}

	auditLog.Record(id, "dispute_raised", userID, userName, body.Reason)
	jsonOK(w, toResponse(deal, userID))
}

func submitEvidence(w http.ResponseWriter, r *http.Request, id string) {
	userID, userName := userFromRequest(r)

	if err := validator.ValidateID(id); err != nil {
		jsonError(w, "invalid deal ID", http.StatusBadRequest)
		return
	}

	if !limiter.Allow(userID) {
		jsonError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var body struct {
		Text string `json:"text"`
	}
	if err := decode(r, &body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)

	if err := validator.ValidateReason(body.Text); err != nil {
		jsonError(w, "evidence text: "+err.Error(), http.StatusBadRequest)
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		jsonError(w, "deal not found", http.StatusNotFound)
		return
	}

	// Only participants can submit evidence
	if deal.SenderID != userID && deal.ReceiverID != userID {
		jsonError(w, "you are not a participant in this deal", http.StatusForbidden)
		return
	}

	if deal.Status != escrow.StatusDisputed {
		jsonError(w, "deal is not disputed", http.StatusBadRequest)
		return
	}

	auditLog.Record(id, "evidence_submitted", userID, userName, body.Text)
	jsonOK(w, map[string]string{"status": "evidence recorded"})
}

func toResponse(deal escrow.Escrow, callerID string) DealResponse {
	role := "Buyer"
	counterparty := deal.ReceiverName
	if deal.ReceiverID == callerID {
		role = "Seller"
		counterparty = deal.SenderName
	}
	expires := "Expired"
	if time.Now().Before(deal.ExpiresAt) {
		remaining := time.Until(deal.ExpiresAt)
		hours := int(remaining.Hours())
		if hours >= 24 {
			expires = fmt.Sprintf("%dd remaining", hours/24)
		} else {
			expires = fmt.Sprintf("%dh remaining", hours)
		}
	}
	return DealResponse{
		ID:               deal.ID,
		Title:            fmt.Sprintf("Deal #%s", deal.ID[:8]),
		Role:             role,
		Counterparty:     counterparty,
		Amount:           fmt.Sprintf("%d", deal.Amount),
		Currency:         "XLM",
		Status:           string(deal.Status),
		Expires:          expires,
		StellarBalanceID: deal.StellarTxHash,
		StellarTxHash:    deal.StellarTxHash,
		Timeline:         buildTimeline(deal),
	}
}

func buildTimeline(deal escrow.Escrow) []TimelineStep {
	steps := []TimelineStep{
		{Label: "Deal created", Sub: deal.CreatedAt.Format("Jan 2, 2006 · 15:04"), State: "done"},
	}
	if deal.StellarFunded {
		steps = append(steps, TimelineStep{Label: "Funds locked on Stellar", Sub: "Claimable balance active", State: "done"})
	} else {
		steps = append(steps, TimelineStep{Label: "Awaiting funding", Sub: "Not yet locked on Stellar", State: "active"})
	}
	switch deal.Status {
	case escrow.StatusLocked:
		steps = append(steps, TimelineStep{Label: "Awaiting delivery", Sub: "Seller to deliver goods", State: "active"})
		steps = append(steps, TimelineStep{Label: "Confirm receipt", Sub: "Mark complete to release funds", State: "waiting"})
	case escrow.StatusClaimed:
		steps = append(steps, TimelineStep{Label: "Delivery confirmed", Sub: "Buyer marked complete", State: "done"})
		steps = append(steps, TimelineStep{Label: "Funds released", Sub: "Payment sent to seller", State: "done"})
	case escrow.StatusDisputed:
		steps = append(steps, TimelineStep{Label: "Dispute raised", Sub: "Under admin review", State: "warn"})
		steps = append(steps, TimelineStep{Label: "Admin review", Sub: "Evidence being reviewed", State: "active"})
	case escrow.StatusRefunded:
		steps = append(steps, TimelineStep{Label: "Refunded", Sub: "Funds returned to buyer", State: "done"})
	case escrow.StatusResolved:
		steps = append(steps, TimelineStep{Label: "Resolved", Sub: "Deal resolved by admin", State: "done"})
	}
	return steps
}
