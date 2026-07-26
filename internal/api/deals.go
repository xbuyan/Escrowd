package api

import (
	"fmt"
	"github.com/xbuyan/Escrowd/internal/email"
	"github.com/xbuyan/Escrowd/internal/escrow"
	"github.com/xbuyan/Escrowd/internal/stellar"
	"github.com/xbuyan/Escrowd/internal/validator"
	"net/http"
	"strings"
	"time"
)

type DealResponse struct {
	ID               string         `json:"id"`
	Title            string         `json:"title"`
	Description      string         `json:"description"`
	Role             string         `json:"role"`
	Counterparty     string         `json:"counterparty"`
	Amount           string         `json:"amount"`
	Currency         string         `json:"currency"`
	Status           string         `json:"status"`
	Expires          string         `json:"expires"`
	StellarBalanceID string         `json:"stellar_balance_id,omitempty"`
	StellarTxHash    string         `json:"stellar_tx_hash,omitempty"`
	InviteToken      string         `json:"invite_token,omitempty"`
	ReceiverJoined   bool           `json:"receiver_joined"`
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
	case r.Method == http.MethodPost && action == "fund":
		fundDeal(w, r, id)
	default:
		jsonError(w, "not found", http.StatusNotFound)
	}
}

// handleInvite handles GET /api/invites/:token (view invite) and
// POST /api/invites/:token/accept (join the deal).
func handleInvite(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// parts = ["api", "invites", "{token}"] or ["api", "invites", "{token}", "accept"]
	if len(parts) < 3 {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	token := parts[2]
	action := ""
	if len(parts) >= 4 {
		action = parts[3]
	}

	switch {
	case r.Method == http.MethodGet && action == "":
		viewInvite(w, r, token)
	case r.Method == http.MethodPost && action == "accept":
		acceptInvite(w, r, token)
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

	if err := validator.ValidateID(id); err != nil {
		jsonError(w, "invalid deal ID", http.StatusBadRequest)
		return
	}

	deal, err := db.Get(id)
	if err != nil {
		jsonError(w, "deal not found", http.StatusNotFound)
		return
	}

	// Only participants can view full deal details
	if deal.SenderID != userID && deal.ReceiverID != userID {
		jsonError(w, "you are not a participant in this deal", http.StatusForbidden)
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

// createDeal creates a new escrow deal. The actual title, description, and
// currency typed by the user are stored and returned — no placeholder titles.
//
// If counterparty looks like an email address, an invitation email is sent
// with a unique link. The deal's ReceiverID remains a placeholder until the
// counterparty clicks the link and joins via acceptInvite.
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

	body.Title = strings.TrimSpace(body.Title)
	body.Counterparty = strings.TrimSpace(body.Counterparty)
	body.Description = strings.TrimSpace(body.Description)
	body.Currency = strings.TrimSpace(strings.ToUpper(body.Currency))

	if body.Title == "" {
		jsonError(w, "title is required", http.StatusBadRequest)
		return
	}
	if len(body.Title) > 100 {
		jsonError(w, "title too long — maximum 100 characters", http.StatusBadRequest)
		return
	}
	if len(body.Description) > 1000 {
		jsonError(w, "description too long — maximum 1000 characters", http.StatusBadRequest)
		return
	}
	if body.Counterparty == "" {
		jsonError(w, "counterparty is required", http.StatusBadRequest)
		return
	}
	if len(body.Counterparty) > 100 {
		jsonError(w, "counterparty value too long", http.StatusBadRequest)
		return
	}

	allowedCurrency := map[string]bool{"XLM": true, "KES": true, "USD": true}
	// Normalise "KES (M-Pesa)" style input from the frontend
	if strings.HasPrefix(body.Currency, "KES") {
		body.Currency = "KES"
	}
	if !allowedCurrency[body.Currency] {
		jsonError(w, "currency must be XLM, KES, or USD", http.StatusBadRequest)
		return
	}

	if body.Role != "buyer" && body.Role != "seller" {
		jsonError(w, "role must be buyer or seller", http.StatusBadRequest)
		return
	}

	var amountInt int
	fmt.Sscanf(body.Amount, "%d", &amountInt)
	if err := validator.ValidateAmount(amountInt); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !limiter.Allow(userID) {
		jsonError(w, "rate limit exceeded — maximum 10 operations per hour", http.StatusTooManyRequests)
		return
	}

	// Determine sender/receiver. Counterparty is stored as a placeholder
	// (email or name) until they accept the invitation.
	senderID, senderName := userID, userName
	receiverID, receiverName := "pending:"+body.Counterparty, body.Counterparty
	if body.Role == "seller" {
		// The current user is the seller (receiver of funds);
		// counterparty is the buyer who must fund the deal.
		receiverID, receiverName = senderID, senderName
		senderID, senderName = "pending:"+body.Counterparty, body.Counterparty
	}

	deal := escrow.New(senderID, senderName, receiverID, receiverName, amountInt, "web-init")
	deal.Title = body.Title
	deal.Description = body.Description
	deal.Currency = body.Currency

	// If counterparty looks like an email, store it and send an invite
	counterpartyEmail := ""
	if emailRegex.MatchString(body.Counterparty) {
		counterpartyEmail = body.Counterparty
		deal.ReceiverEmail = counterpartyEmail
	}

	if err := db.Save(deal); err != nil {
		jsonError(w, "could not save deal", http.StatusInternalServerError)
		return
	}

	auditLog.Record(deal.ID, "deal_created", userID, userName,
		fmt.Sprintf("Created '%s' for %s %s", deal.Title, body.Amount, deal.Currency))

	// Send invitation email if we have a valid email address
	if counterpartyEmail != "" {
		inviteURL := fmt.Sprintf("%s/#/invite/%s", frontendURL(), deal.InviteToken)
		if err := email.SendDealInviteEmail(counterpartyEmail, userName, deal.Title, inviteURL); err != nil {
			fmt.Println("warning: could not send invite email:", err)
		}
	}

	jsonOK(w, toResponse(deal, userID))
}

// viewInvite returns basic deal info for someone who has not yet logged in
// or accepted the invite — used to render an invite preview page.
// Does not expose sensitive fields.
func viewInvite(w http.ResponseWriter, r *http.Request, token string) {
	if len(token) < 5 || len(token) > 100 {
		jsonError(w, "invalid invite token", http.StatusBadRequest)
		return
	}

	deal, err := db.GetByInviteToken(token)
	if err != nil {
		jsonError(w, "invite not found or already used", http.StatusNotFound)
		return
	}

	if deal.ReceiverJoined {
		jsonError(w, "this invitation has already been accepted", http.StatusConflict)
		return
	}

	jsonOK(w, map[string]any{
		"title":       deal.Title,
		"description": deal.Description,
		"amount":      fmt.Sprintf("%d", deal.Amount),
		"currency":    deal.Currency,
		"inviter":     deal.SenderName,
	})
}

// acceptInvite is called by an authenticated user who clicked an invite link.
// It binds their real user ID to the deal's placeholder receiver slot.
func acceptInvite(w http.ResponseWriter, r *http.Request, token string) {
	userID, userName := userFromRequest(r)

	if len(token) < 5 || len(token) > 100 {
		jsonError(w, "invalid invite token", http.StatusBadRequest)
		return
	}

	deal, err := db.GetByInviteToken(token)
	if err != nil {
		jsonError(w, "invite not found or already used", http.StatusNotFound)
		return
	}

	updated, err := db.UpdateWithLock(deal.ID, func(d *escrow.Escrow) error {
		// Whichever side is still a placeholder ("pending:...") gets bound
		// to the joining user.
		if strings.HasPrefix(d.SenderID, "pending:") {
			if err := escrow.JoinDeal(d, userID, userName); err != nil {
				return err
			}
			d.SenderID = userID
			d.SenderName = userName
			d.ReceiverJoined = true
			return nil
		}
		if strings.HasPrefix(d.ReceiverID, "pending:") {
			return escrow.JoinDeal(d, userID, userName)
		}
		return fmt.Errorf("this invitation has already been accepted")
	})
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	auditLog.Record(updated.ID, "deal_joined", userID, userName,
		fmt.Sprintf("%s joined the deal via invitation", userName))

	jsonOK(w, toResponse(updated, userID))
}

// fundDeal triggers the real Stellar claimable balance creation.
// Called by the buyer (sender) once both parties have joined and the
// buyer is ready to lock funds on-chain.
//
// Requires the buyer to provide their Stellar secret key — this is
// never stored; it is used only to sign the lock transaction and
// discarded immediately after.
func fundDeal(w http.ResponseWriter, r *http.Request, id string) {
	userID, userName := userFromRequest(r)

	if err := validator.ValidateID(id); err != nil {
		jsonError(w, "invalid deal ID", http.StatusBadRequest)
		return
	}

	var body struct {
		BuyerSecretKey string `json:"buyer_secret_key"`
	}
	if err := decode(r, &body); err != nil || body.BuyerSecretKey == "" {
		jsonError(w, "buyer_secret_key is required to fund on Stellar", http.StatusBadRequest)
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

	if deal.SenderID != userID {
		jsonError(w, "only the buyer can fund this deal", http.StatusForbidden)
		return
	}
	if !deal.ReceiverJoined {
		jsonError(w, "counterparty has not joined this deal yet", http.StatusBadRequest)
		return
	}
	if deal.StellarFunded {
		jsonError(w, "deal is already funded on Stellar", http.StatusBadRequest)
		return
	}
	if deal.Currency != "XLM" {
		jsonError(w, "Stellar funding is only available for XLM deals", http.StatusBadRequest)
		return
	}
	if deal.ReceiverStellarAddr == "" {
		jsonError(w, "counterparty has not set their Stellar address yet", http.StatusBadRequest)
		return
	}

	// expirySeconds = time remaining until deal.ExpiresAt
	expirySeconds := int64(time.Until(deal.ExpiresAt).Seconds())
	if expirySeconds <= 0 {
		jsonError(w, "deal has expired and cannot be funded", http.StatusBadRequest)
		return
	}

	result, err := stellar.CreateClaimableEscrow(
		body.BuyerSecretKey,
		deal.ReceiverStellarAddr,
		fmt.Sprintf("%d", deal.Amount),
		expirySeconds,
	)
	// Zero the secret key reference immediately — Go strings are immutable
	// so we cannot wipe memory, but we drop the reference and avoid
	// logging or storing it anywhere.
	body.BuyerSecretKey = ""

	if err != nil {
		jsonError(w, "could not create Stellar claimable balance: "+err.Error(), http.StatusBadGateway)
		return
	}

	updated, err := db.UpdateWithLock(id, func(d *escrow.Escrow) error {
		d.StellarFunded = true
		d.StellarBalanceID = result.BalanceID
		d.StellarTxHash = result.TxHash
		return nil
	})
	if err != nil {
		jsonError(w, "funded on-chain but could not update deal record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	auditLog.Record(id, "deal_funded", userID, userName,
		fmt.Sprintf("Funded on Stellar: balance_id=%s tx=%s", result.BalanceID, result.TxHash))

	jsonOK(w, toResponse(updated, userID))
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

	updated, err := db.UpdateWithLock(id, func(deal *escrow.Escrow) error {
		if deal.ReceiverID != userID {
			return fmt.Errorf("only the buyer can mark a deal complete")
		}
		return escrow.Claim(deal, "")
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "only the buyer") {
			status = http.StatusForbidden
		}
		jsonError(w, err.Error(), status)
		return
	}

	auditLog.Record(id, "deal_claimed", userID, userName, "Marked complete via web")
	jsonOK(w, toResponse(updated, userID))
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

	updated, err := db.UpdateWithLock(id, func(deal *escrow.Escrow) error {
		if deal.SenderID != userID {
			return fmt.Errorf("only the buyer can request a refund")
		}
		return escrow.Refund(deal)
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "only the buyer") {
			status = http.StatusForbidden
		}
		jsonError(w, err.Error(), status)
		return
	}

	auditLog.Record(id, "deal_refunded", userID, userName, "Refund via web")
	jsonOK(w, toResponse(updated, userID))
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

	updated, err := db.UpdateWithLock(id, func(deal *escrow.Escrow) error {
		if deal.SenderID != userID && deal.ReceiverID != userID {
			return fmt.Errorf("you are not a participant in this deal")
		}
		return escrow.RaiseDispute(deal, userID, userName, body.Reason)
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not a participant") {
			status = http.StatusForbidden
		}
		jsonError(w, err.Error(), status)
		return
	}

	auditLog.Record(id, "dispute_raised", userID, userName, body.Reason)
	jsonOK(w, toResponse(updated, userID))
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

	if strings.HasPrefix(counterparty, "pending:") {
		counterparty = strings.TrimPrefix(counterparty, "pending:") + " (invited)"
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

	title := deal.Title
	if title == "" {
		title = fmt.Sprintf("Deal #%s", deal.ID[:8])
	}

	resp := DealResponse{
		ID:               deal.ID,
		Title:            title,
		Description:      deal.Description,
		Role:             role,
		Counterparty:     counterparty,
		Amount:           fmt.Sprintf("%d", deal.Amount),
		Currency:         deal.Currency,
		Status:           string(deal.Status),
		Expires:          expires,
		StellarBalanceID: deal.StellarBalanceID,
		StellarTxHash:    deal.StellarTxHash,
		ReceiverJoined:   deal.ReceiverJoined,
		Timeline:         buildTimeline(deal),
	}
	if resp.Currency == "" {
		resp.Currency = "XLM"
	}

	// Only show the invite token to the deal creator (sender) — they're
	// the one who shares it with the counterparty.
	if deal.SenderID == callerID && !deal.ReceiverJoined {
		resp.InviteToken = deal.InviteToken
	}

	return resp
}

func buildTimeline(deal escrow.Escrow) []TimelineStep {
	steps := []TimelineStep{
		{Label: "Deal created", Sub: deal.CreatedAt.Format("Jan 2, 2006 · 15:04"), State: "done"},
	}

	if deal.ReceiverJoined {
		steps = append(steps, TimelineStep{Label: "Counterparty joined", Sub: "Both parties are now linked to this deal", State: "done"})
	} else {
		steps = append(steps, TimelineStep{Label: "Awaiting counterparty", Sub: "Invitation sent — waiting for them to join", State: "active"})
	}

	if deal.StellarFunded {
		steps = append(steps, TimelineStep{Label: "Funds locked on Stellar", Sub: "Claimable balance active on testnet", State: "done"})
	} else if deal.ReceiverJoined {
		steps = append(steps, TimelineStep{Label: "Awaiting funding", Sub: "Buyer to lock funds on Stellar", State: "active"})
	}

	switch deal.Status {
	case escrow.StatusLocked:
		if deal.StellarFunded {
			steps = append(steps, TimelineStep{Label: "Awaiting delivery", Sub: "Seller to deliver goods", State: "active"})
			steps = append(steps, TimelineStep{Label: "Confirm receipt", Sub: "Mark complete to release funds", State: "waiting"})
		}
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
