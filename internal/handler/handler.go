package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/yuno/ai-challenge/internal/health"
	"github.com/yuno/ai-challenge/internal/model"
	"github.com/yuno/ai-challenge/internal/retry"
	"github.com/yuno/ai-challenge/internal/store"
)

// maxRequestBodySize limits the size of incoming request bodies (1 MB).
const maxRequestBodySize = 1 << 20

// ErrorResponse represents an API error with a machine-parseable code.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Handler holds HTTP handler dependencies.
type Handler struct {
	engine          *retry.Engine
	store           *store.Store
	health          *health.Tracker
	knownProcessors map[string]bool
}

// New creates a new Handler. knownProcessors can be nil to skip processor name validation.
func New(engine *retry.Engine, s *store.Store, h *health.Tracker, knownProcessors []string) *Handler {
	kp := make(map[string]bool, len(knownProcessors))
	for _, p := range knownProcessors {
		kp[p] = true
	}
	return &Handler{engine: engine, store: s, health: h, knownProcessors: kp}
}

// CreateTransaction handles POST /transactions.
func (h *Handler) CreateTransaction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

	var req model.TransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid JSON: " + err.Error(), Code: "INVALID_JSON"})
		return
	}

	// Validate required fields.
	if req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "amount must be positive", Code: "INVALID_AMOUNT"})
		return
	}
	if req.Currency == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "currency is required", Code: "MISSING_CURRENCY"})
		return
	}
	if req.PaymentMethod == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "payment_method is required", Code: "MISSING_PAYMENT_METHOD"})
		return
	}
	if len(req.ProcessorOrder) == 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "processor_order must have at least one processor", Code: "MISSING_PROCESSOR_ORDER"})
		return
	}
	if req.MerchantID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "merchant_id is required", Code: "MISSING_MERCHANT_ID"})
		return
	}

	// Validate processor names when known processors are configured.
	if len(h.knownProcessors) > 0 {
		for _, p := range req.ProcessorOrder {
			if !h.knownProcessors[p] {
				writeJSON(w, http.StatusBadRequest, ErrorResponse{
					Error: "unknown processor: " + p,
					Code:  "UNKNOWN_PROCESSOR",
				})
				return
			}
		}
	}

	txn, err := h.engine.Execute(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: "INTERNAL_ERROR"})
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
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error(), Code: "NOT_FOUND"})
		return
	}
	writeJSON(w, http.StatusOK, txn)
}

// ListTransactions handles GET /transactions with optional pagination.
// Query params: ?limit=N&offset=N (defaults to returning all transactions).
func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	all := h.store.List()

	// Sort by CreatedAt descending for consistent pagination order.
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	// Apply pagination.
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if offset < 0 {
		offset = 0
	}
	if offset > len(all) {
		offset = len(all)
	}

	result := all[offset:]
	if limit > 0 && limit < len(result) {
		result = result[:limit]
	}

	writeJSON(w, http.StatusOK, result)
}

// GetProcessorHealth handles GET /processors/health.
func (h *Handler) GetProcessorHealth(w http.ResponseWriter, r *http.Request) {
	if h.health == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "health tracking not enabled", Code: "HEALTH_DISABLED"})
		return
	}
	writeJSON(w, http.StatusOK, h.health.GetAllHealth())
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal encoding error","code":"ENCODING_ERROR"}` + "\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n"))
}
