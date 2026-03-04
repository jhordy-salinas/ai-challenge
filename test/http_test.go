package test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yuno/ai-challenge/internal/handler"
	"github.com/yuno/ai-challenge/internal/health"
	"github.com/yuno/ai-challenge/internal/model"
	"github.com/yuno/ai-challenge/internal/processor"
	"github.com/yuno/ai-challenge/internal/retry"
	"github.com/yuno/ai-challenge/internal/router"
	"github.com/yuno/ai-challenge/internal/store"
)

func setupTestServer(sim processor.Simulator) *httptest.Server {
	s := store.New()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	tracker := health.NewTracker(5 * time.Minute)
	engine := retry.NewEngine(s, sim, tracker, noopSleep, logger)
	h := handler.New(engine, s, tracker, nil)
	r := router.New(h, logger)
	return httptest.NewServer(r)
}

func TestHTTPPostApproved(t *testing.T) {
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {{Approved: true}},
	})
	srv := setupTestServer(sim)
	defer srv.Close()

	body := `{"amount":100,"currency":"MXN","payment_method":"card_visa_4242","merchant_id":"m1","processor_order":["StripeLatam"]}`
	resp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var result model.TransactionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Status != "approved" {
		t.Errorf("status = %q, want approved", result.Status)
	}
	if result.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", result.Attempts)
	}
	if result.TransactionID == "" {
		t.Error("transaction_id is empty")
	}
}

func TestHTTPPostDeclined(t *testing.T) {
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {{ErrorType: model.ErrInsufficientFunds, ErrorMessage: "insufficient funds"}},
	})
	srv := setupTestServer(sim)
	defer srv.Close()

	body := `{"amount":100,"currency":"MXN","payment_method":"card_visa_4242","merchant_id":"m1","processor_order":["StripeLatam"]}`
	resp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}

func TestHTTPPostFailed(t *testing.T) {
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {{ErrorType: model.ErrTimeout, ErrorMessage: "timeout"}},
	})
	srv := setupTestServer(sim)
	defer srv.Close()

	body := `{"amount":100,"currency":"MXN","payment_method":"card_visa_4242","merchant_id":"m1","processor_order":["StripeLatam"]}`
	resp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestHTTPGetTransaction(t *testing.T) {
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {{Approved: true}},
	})
	srv := setupTestServer(sim)
	defer srv.Close()

	// Create a transaction first.
	body := `{"amount":250.50,"currency":"COP","payment_method":"card_visa_4242","merchant_id":"m1","processor_order":["StripeLatam"]}`
	postResp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var postResult model.TransactionResponse
	json.NewDecoder(postResp.Body).Decode(&postResult)
	postResp.Body.Close()

	// Fetch it.
	getResp, err := http.Get(srv.URL + "/transactions/" + postResult.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET status = %d, want 200", getResp.StatusCode)
	}

	var txn model.Transaction
	if err := json.NewDecoder(getResp.Body).Decode(&txn); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if txn.Amount != 250.50 {
		t.Errorf("amount = %.2f, want 250.50", txn.Amount)
	}
	if txn.Currency != "COP" {
		t.Errorf("currency = %q, want COP", txn.Currency)
	}
	if txn.CompletedAt.IsZero() {
		t.Error("completed_at should be set")
	}
	if txn.TotalProcessingTimeMs <= 0 {
		t.Errorf("total_processing_time_ms = %.2f, want > 0", txn.TotalProcessingTimeMs)
	}
	if len(txn.Attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(txn.Attempts))
	}
	if txn.Attempts[0].ResponseType != "approved" {
		t.Errorf("response_type = %q, want approved", txn.Attempts[0].ResponseType)
	}
}

func TestHTTPGetNotFound(t *testing.T) {
	sim := processor.NewScriptedSimulator(nil)
	srv := setupTestServer(sim)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/transactions/txn_nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHTTPListTransactions(t *testing.T) {
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {{Approved: true}, {Approved: true}},
	})
	srv := setupTestServer(sim)
	defer srv.Close()

	// Create two transactions.
	body := `{"amount":100,"currency":"MXN","payment_method":"card_visa_4242","merchant_id":"m1","processor_order":["StripeLatam"]}`
	http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))

	resp, err := http.Get(srv.URL + "/transactions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var txns []model.Transaction
	if err := json.NewDecoder(resp.Body).Decode(&txns); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(txns) != 2 {
		t.Errorf("len = %d, want 2", len(txns))
	}
}

func TestHTTPProcessorHealth(t *testing.T) {
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {{Approved: true}},
	})
	srv := setupTestServer(sim)
	defer srv.Close()

	// Generate some health data.
	body := `{"amount":100,"currency":"MXN","payment_method":"card_visa_4242","merchant_id":"m1","processor_order":["StripeLatam"]}`
	http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))

	resp, err := http.Get(srv.URL + "/processors/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHTTPInvalidJSON(t *testing.T) {
	sim := processor.NewScriptedSimulator(nil)
	srv := setupTestServer(sim)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader("{bad json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPMissingRequiredFields(t *testing.T) {
	sim := processor.NewScriptedSimulator(nil)
	srv := setupTestServer(sim)
	defer srv.Close()

	tests := []struct {
		name string
		body string
	}{
		{"missing amount", `{"currency":"MXN","payment_method":"card","processor_order":["A"]}`},
		{"missing currency", `{"amount":100,"payment_method":"card","processor_order":["A"]}`},
		{"missing payment_method", `{"amount":100,"currency":"MXN","processor_order":["A"]}`},
		{"missing processor_order", `{"amount":100,"currency":"MXN","payment_method":"card"}`},
		{"missing merchant_id", `{"amount":100,"currency":"MXN","payment_method":"card","processor_order":["A"]}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestHTTPMissingMerchantID(t *testing.T) {
	sim := processor.NewScriptedSimulator(nil)
	srv := setupTestServer(sim)
	defer srv.Close()

	body := `{"amount":100,"currency":"MXN","payment_method":"card","processor_order":["A"]}`
	resp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["error"] != "merchant_id is required" {
		t.Errorf("error = %q, want %q", result["error"], "merchant_id is required")
	}
}

func TestHTTPUnknownProcessor(t *testing.T) {
	sim := processor.NewScriptedSimulator(nil)
	s := store.New()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	tracker := health.NewTracker(5 * time.Minute)
	engine := retry.NewEngine(s, sim, tracker, noopSleep, logger)
	h := handler.New(engine, s, tracker, []string{"StripeLatam", "PayUSouth", "EbanxBR"})
	r := router.New(h, logger)
	srv := httptest.NewServer(r)
	defer srv.Close()

	body := `{"amount":100,"currency":"MXN","payment_method":"card","merchant_id":"m1","processor_order":["FakeProcessor"]}`
	resp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["code"] != "UNKNOWN_PROCESSOR" {
		t.Errorf("code = %q, want UNKNOWN_PROCESSOR", result["code"])
	}
}

func TestHTTPPagination(t *testing.T) {
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {{Approved: true}, {Approved: true}, {Approved: true}},
	})
	srv := setupTestServer(sim)
	defer srv.Close()

	// Create 3 transactions.
	body := `{"amount":100,"currency":"MXN","payment_method":"card_visa_4242","merchant_id":"m1","processor_order":["StripeLatam"]}`
	http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))

	// Fetch with limit=2.
	resp, err := http.Get(srv.URL + "/transactions?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var txns []model.Transaction
	json.NewDecoder(resp.Body).Decode(&txns)
	if len(txns) != 2 {
		t.Errorf("with limit=2, got %d transactions, want 2", len(txns))
	}

	// Fetch with offset=1&limit=1.
	resp2, err := http.Get(srv.URL + "/transactions?offset=1&limit=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	var txns2 []model.Transaction
	json.NewDecoder(resp2.Body).Decode(&txns2)
	if len(txns2) != 1 {
		t.Errorf("with offset=1&limit=1, got %d transactions, want 1", len(txns2))
	}

	// Fetch all (no params) should return all 3.
	resp3, err := http.Get(srv.URL + "/transactions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()

	var txns3 []model.Transaction
	json.NewDecoder(resp3.Body).Decode(&txns3)
	if len(txns3) != 3 {
		t.Errorf("without pagination, got %d transactions, want 3", len(txns3))
	}
}

func TestHTTPErrorCodes(t *testing.T) {
	sim := processor.NewScriptedSimulator(nil)
	srv := setupTestServer(sim)
	defer srv.Close()

	// Missing amount should return INVALID_AMOUNT code.
	body := `{"currency":"MXN","payment_method":"card","merchant_id":"m1","processor_order":["A"]}`
	resp, err := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["code"] != "INVALID_AMOUNT" {
		t.Errorf("code = %q, want INVALID_AMOUNT", result["code"])
	}
	if result["error"] != "amount must be positive" {
		t.Errorf("error = %q, want 'amount must be positive'", result["error"])
	}
}

func TestHTTPResponseTypes(t *testing.T) {
	// Verify response_type fields in attempt history via GET.
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {
			{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit"},
			{Approved: true},
		},
	})
	srv := setupTestServer(sim)
	defer srv.Close()

	body := `{"amount":100,"currency":"USD","payment_method":"card_visa_4242","merchant_id":"m1","processor_order":["StripeLatam"]}`
	postResp, _ := http.Post(srv.URL+"/transactions", "application/json", strings.NewReader(body))
	var postResult model.TransactionResponse
	json.NewDecoder(postResp.Body).Decode(&postResult)
	postResp.Body.Close()

	getResp, _ := http.Get(srv.URL + "/transactions/" + postResult.TransactionID)
	var txn model.Transaction
	json.NewDecoder(getResp.Body).Decode(&txn)
	getResp.Body.Close()

	if len(txn.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(txn.Attempts))
	}
	if txn.Attempts[0].ResponseType != "soft_decline" {
		t.Errorf("attempt[0].response_type = %q, want soft_decline", txn.Attempts[0].ResponseType)
	}
	if txn.Attempts[1].ResponseType != "approved" {
		t.Errorf("attempt[1].response_type = %q, want approved", txn.Attempts[1].ResponseType)
	}
}
