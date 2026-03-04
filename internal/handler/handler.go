package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/yuno/ai-challenge/internal/health"
	"github.com/yuno/ai-challenge/internal/model"
	"github.com/yuno/ai-challenge/internal/retry"
	"github.com/yuno/ai-challenge/internal/store"
)

// Handler holds HTTP handler dependencies.
type Handler struct {
	engine *retry.Engine
	store  *store.Store
	health *health.Tracker
}

// New creates a new Handler.
func New(engine *retry.Engine, s *store.Store, h *health.Tracker) *Handler {
	return &Handler{engine: engine, store: s, health: h}
}

// CreateTransaction handles POST /transactions.
func (h *Handler) CreateTransaction(w http.ResponseWriter, r *http.Request) {
	var req model.TransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	// Validate required fields.
	if req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount must be positive"})
		return
	}
	if req.Currency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "currency is required"})
		return
	}
	if req.PaymentMethod == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payment_method is required"})
		return
	}
	if len(req.ProcessorOrder) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "processor_order must have at least one processor"})
		return
	}
	if req.MerchantID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "merchant_id is required"})
		return
	}

	txn, err := h.engine.Execute(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	resp := model.TransactionResponse{
		TransactionID: txn.ID,
		Status:        string(txn.Status),
		ProcessorUsed: txn.ProcessorUsed,
		Attempts:      len(txn.Attempts),
		FinalError:    txn.FinalError,
	}

	status := http.StatusOK
	switch txn.Status {
	case model.StatusDeclined:
		status = http.StatusUnprocessableEntity
	case model.StatusFailed:
		status = http.StatusBadGateway
	}

	writeJSON(w, status, resp)
}

// GetTransaction handles GET /transactions/{id}.
func (h *Handler) GetTransaction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	txn, err := h.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, txn)
}

// ListTransactions handles GET /transactions.
func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.store.List())
}

// GetProcessorHealth handles GET /processors/health.
func (h *Handler) GetProcessorHealth(w http.ResponseWriter, r *http.Request) {
	if h.health == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "health tracking not enabled"})
		return
	}
	writeJSON(w, http.StatusOK, h.health.GetAllHealth())
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
