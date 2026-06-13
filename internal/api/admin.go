package api

import (
	"escrowd/internal/escrow"
	"fmt"
	"net/http"
	"strings"
)

type AdminDealResponse struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Buyer    string `json:"buyer"`
	Seller   string `json:"seller"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Status   string `json:"status"`
}

func handleAdminDeals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ids, err := db.ListIDs()
	if err != nil {
		jsonError(w, "could not list deals", http.StatusInternalServerError)
		return
	}
	var deals []AdminDealResponse
	for _, id := range ids {
		deal, err := db.Get(id)
		if err != nil {
			continue
		}
		title := deal.Title
		if title == "" {
			title = fmt.Sprintf("Deal #%s", deal.ID[:8])
		}
		currency := deal.Currency
		if currency == "" {
			currency = "XLM"
		}
		deals = append(deals, AdminDealResponse{
			ID:       deal.ID,
			Title:    title,
			Buyer:    deal.SenderName,
			Seller:   deal.ReceiverName,
			Amount:   fmt.Sprintf("%d", deal.Amount),
			Currency: currency,
			Status:   string(deal.Status),
		})
	}
	if deals == nil {
		deals = []AdminDealResponse{}
	}
	jsonOK(w, deals)
}

func handleAdminResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		jsonError(w, "deal ID required", http.StatusBadRequest)
		return
	}
	id := parts[3]
	var body struct {
		Resolution string `json:"resolution"`
		Note       string `json:"note"`
	}
	if err := decode(r, &body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Resolution != "buyer" && body.Resolution != "seller" {
		jsonError(w, "resolution must be 'buyer' or 'seller'", http.StatusBadRequest)
		return
	}

	body.Note = strings.TrimSpace(body.Note)
	if len(body.Note) > 1000 {
		jsonError(w, "note too long — maximum 1000 characters", http.StatusBadRequest)
		return
	}

	userID, userName := userFromRequest(r)

	updated, err := db.UpdateWithLock(id, func(deal *escrow.Escrow) error {
		if deal.Status != escrow.StatusDisputed {
			return fmt.Errorf("deal is not disputed")
		}
		if body.Resolution == "buyer" {
			return escrow.Refund(deal)
		}
		return escrow.Claim(deal, "")
	})
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	detail := fmt.Sprintf("Resolved in favour of %s. Note: %s", body.Resolution, body.Note)
	auditLog.Record(id, "dispute_resolved", userID, userName, detail)
	jsonOK(w, map[string]string{"status": "resolved", "resolution": body.Resolution, "deal_id": updated.ID})
}

func handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type AuditResponse struct {
		CreatedAt string `json:"created_at"`
		Event     string `json:"event"`
		DealID    string `json:"deal_id"`
		Actor     string `json:"actor"`
		Detail    string `json:"detail"`
	}
	var results []AuditResponse
	ids, _ := db.ListIDs()
	for _, id := range ids {
		entries, err := db.AuditDB.GetByEscrow(id)
		if err != nil {
			continue
		}
		for _, e := range entries {
			results = append(results, AuditResponse{
				CreatedAt: e.Timestamp.Format("Jan 2, 2006 · 15:04"),
				Event:     e.Event,
				DealID:    e.EscrowID,
				Actor:     e.ActorName,
				Detail:    e.Detail,
			})
		}
	}
	if results == nil {
		results = []AuditResponse{}
	}
	jsonOK(w, results)
}
