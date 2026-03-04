package test

import (
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/yuno/ai-challenge/internal/health"
	"github.com/yuno/ai-challenge/internal/model"
	"github.com/yuno/ai-challenge/internal/processor"
	"github.com/yuno/ai-challenge/internal/retry"
	"github.com/yuno/ai-challenge/internal/store"
)

// noopSleep skips backoff delays in tests.
func noopSleep(_ time.Duration) {}

func makeRequest(processors ...string) model.TransactionRequest {
	return model.TransactionRequest{
		Amount:         99.99,
		Currency:       "MXN",
		PaymentMethod:  "card_visa_4242",
		MerchantID:     "cloudmarket_mx",
		ProcessorOrder: processors,
	}
}

func makeRequestWithCurrency(currency string, processors ...string) model.TransactionRequest {
	return model.TransactionRequest{
		Amount:         99.99,
		Currency:       currency,
		PaymentMethod:  "card_visa_4242",
		MerchantID:     "cloudmarket_mx",
		ProcessorOrder: processors,
	}
}

func newEngine(sim processor.Simulator) (*retry.Engine, *store.Store) {
	s := store.New()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e := retry.NewEngine(s, sim, nil, noopSleep, logger)
	return e, s
}

func TestRetryScenarios(t *testing.T) {
	tests := []struct {
		name              string
		request           model.TransactionRequest
		responses         map[string][]processor.ScriptedResponse
		expectedStatus    model.TransactionStatus
		expectedAttempts  int
		expectedProcessor string // processor that handled the final attempt (approved or last)
	}{
		{
			name:    "1. Immediate success",
			request: makeRequest("StripeLatam"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {{Approved: true}},
			},
			expectedStatus:    model.StatusApproved,
			expectedAttempts:  1,
			expectedProcessor: "StripeLatam",
		},
		{
			name:    "2. Timeout -> switch processor -> success",
			request: makeRequest("StripeLatam", "PayUSouth"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {{ErrorType: model.ErrTimeout, ErrorMessage: "timeout"}},
				"PayUSouth":   {{Approved: true}},
			},
			expectedStatus:    model.StatusApproved,
			expectedAttempts:  2,
			expectedProcessor: "PayUSouth",
		},
		{
			name:    "3. Rate limit -> retry same -> success",
			request: makeRequest("StripeLatam"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {
					{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit"},
					{Approved: true},
				},
			},
			expectedStatus:    model.StatusApproved,
			expectedAttempts:  2,
			expectedProcessor: "StripeLatam",
		},
		{
			name:    "4. Hard decline -> abort immediately",
			request: makeRequest("StripeLatam", "PayUSouth"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {{ErrorType: model.ErrInsufficientFunds, ErrorMessage: "insufficient funds"}},
			},
			expectedStatus:   model.StatusDeclined,
			expectedAttempts: 1,
		},
		{
			name:    "5. All processors timeout -> exhausted",
			request: makeRequest("StripeLatam", "PayUSouth", "EbanxBR"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {{ErrorType: model.ErrTimeout, ErrorMessage: "timeout"}},
				"PayUSouth":   {{ErrorType: model.ErrTimeout, ErrorMessage: "timeout"}},
				"EbanxBR":     {{ErrorType: model.ErrTimeout, ErrorMessage: "timeout"}},
			},
			expectedStatus:   model.StatusFailed,
			expectedAttempts: 3,
		},
		{
			name:    "6. Rate limit -> timeout -> switch -> success",
			request: makeRequest("StripeLatam", "PayUSouth"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {
					{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit"},
					{ErrorType: model.ErrTimeout, ErrorMessage: "timeout"},
				},
				"PayUSouth": {{Approved: true}},
			},
			expectedStatus:    model.StatusApproved,
			expectedAttempts:  3,
			expectedProcessor: "PayUSouth",
		},
		{
			name:    "7. Processor unavailable cascade -> fail",
			request: makeRequest("StripeLatam", "PayUSouth", "EbanxBR"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {{ErrorType: model.ErrProcessorUnavailable, ErrorMessage: "unavailable"}},
				"PayUSouth":   {{ErrorType: model.ErrProcessorUnavailable, ErrorMessage: "unavailable"}},
				"EbanxBR":     {{ErrorType: model.ErrProcessorUnavailable, ErrorMessage: "unavailable"}},
			},
			expectedStatus:   model.StatusFailed,
			expectedAttempts: 3,
		},
		// --- New scenarios (8-12) ---
		{
			name:    "8. try_again_later -> retry same processor -> success (COP)",
			request: makeRequestWithCurrency("COP", "PayUSouth"),
			responses: map[string][]processor.ScriptedResponse{
				"PayUSouth": {
					{ErrorType: model.ErrTryAgainLater, ErrorMessage: "try again later"},
					{Approved: true},
				},
			},
			expectedStatus:    model.StatusApproved,
			expectedAttempts:  2,
			expectedProcessor: "PayUSouth",
		},
		{
			name:    "9. Fraudulent card -> abort immediately (CLP)",
			request: makeRequestWithCurrency("CLP", "StripeLatam", "PayUSouth"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {{ErrorType: model.ErrFraudulentCard, ErrorMessage: "fraudulent card"}},
			},
			expectedStatus:   model.StatusDeclined,
			expectedAttempts: 1,
		},
		{
			name:    "10. Do not honor -> abort immediately (USD)",
			request: makeRequestWithCurrency("USD", "EbanxBR", "StripeLatam"),
			responses: map[string][]processor.ScriptedResponse{
				"EbanxBR": {{ErrorType: model.ErrDoNotHonor, ErrorMessage: "do not honor"}},
			},
			expectedStatus:   model.StatusDeclined,
			expectedAttempts: 1,
		},
		{
			name:    "11. Service error -> retry same processor -> success (COP)",
			request: makeRequestWithCurrency("COP", "EbanxBR"),
			responses: map[string][]processor.ScriptedResponse{
				"EbanxBR": {
					{ErrorType: model.ErrServiceError, ErrorMessage: "service error"},
					{Approved: true},
				},
			},
			expectedStatus:    model.StatusApproved,
			expectedAttempts:  2,
			expectedProcessor: "EbanxBR",
		},
		{
			name:    "12. Rate limit exhaustion -> all retries spent -> failed (CLP)",
			request: makeRequestWithCurrency("CLP", "StripeLatam"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {
					{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit 1"},
					{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit 2"},
					{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit 3"},
					{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit 4"},
				},
			},
			expectedStatus:   model.StatusFailed,
			expectedAttempts: 4,
		},
		{
			name:    "13. Card expired -> abort immediately",
			request: makeRequest("StripeLatam", "PayUSouth"),
			responses: map[string][]processor.ScriptedResponse{
				"StripeLatam": {{ErrorType: model.ErrCardExpired, ErrorMessage: "card expired"}},
			},
			expectedStatus:   model.StatusDeclined,
			expectedAttempts: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sim := processor.NewScriptedSimulator(tc.responses)
			engine, s := newEngine(sim)

			txn, err := engine.Execute(tc.request)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}

			if txn.Status != tc.expectedStatus {
				t.Errorf("status = %q, want %q", txn.Status, tc.expectedStatus)
			}
			if len(txn.Attempts) != tc.expectedAttempts {
				t.Errorf("attempts = %d, want %d", len(txn.Attempts), tc.expectedAttempts)
			}
			if tc.expectedProcessor != "" && txn.ProcessorUsed != tc.expectedProcessor {
				t.Errorf("processor_used = %q, want %q", txn.ProcessorUsed, tc.expectedProcessor)
			}

			// Verify transaction ID format.
			if len(txn.ID) < 5 || txn.ID[:4] != "txn_" {
				t.Errorf("invalid transaction ID format: %q", txn.ID)
			}

			// Verify CompletedAt is set for all terminal transactions.
			if txn.CompletedAt.IsZero() {
				t.Errorf("CompletedAt is zero; expected non-zero for terminal status %q", txn.Status)
			}

			// Verify TotalProcessingTimeMs is positive.
			if txn.TotalProcessingTimeMs <= 0 {
				t.Errorf("TotalProcessingTimeMs = %f, want > 0", txn.TotalProcessingTimeMs)
			}

			// Verify each attempt has required fields.
			for i, a := range txn.Attempts {
				if a.Number != i+1 {
					t.Errorf("attempt[%d].Number = %d, want %d", i, a.Number, i+1)
				}
				if a.Processor == "" {
					t.Errorf("attempt[%d].Processor is empty", i)
				}
				if a.Timestamp.IsZero() {
					t.Errorf("attempt[%d].Timestamp is zero", i)
				}
				if a.ResponseType == "" {
					t.Errorf("attempt[%d].ResponseType is empty", i)
				}
			}

			// Verify the transaction is persisted in the store.
			stored, err := s.Get(txn.ID)
			if err != nil {
				t.Fatalf("store.Get(%s) error: %v", txn.ID, err)
			}
			if stored.Status != tc.expectedStatus {
				t.Errorf("stored status = %q, want %q", stored.Status, tc.expectedStatus)
			}
			if len(stored.Attempts) != tc.expectedAttempts {
				t.Errorf("stored attempts = %d, want %d", len(stored.Attempts), tc.expectedAttempts)
			}
		})
	}
}

func TestExponentialBackoffValues(t *testing.T) {
	var mu sync.Mutex
	var delays []time.Duration
	capturingSleep := func(d time.Duration) {
		mu.Lock()
		delays = append(delays, d)
		mu.Unlock()
	}

	s := store.New()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {
			{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit"},
			{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit"},
			{ErrorType: model.ErrRateLimitExceeded, ErrorMessage: "rate limit"},
			{Approved: true},
		},
	})
	engine := retry.NewEngine(s, sim, nil, capturingSleep, logger)

	txn, err := engine.Execute(makeRequest("StripeLatam"))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if txn.Status != model.StatusApproved {
		t.Errorf("status = %q, want approved", txn.Status)
	}
	if len(delays) != 3 {
		t.Fatalf("got %d backoff delays, want 3", len(delays))
	}

	// Verify exponential progression with jitter tolerance (0-25% above base).
	expectedBase := []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond, 2000 * time.Millisecond}
	for i, base := range expectedBase {
		maxJitter := base / 4
		if delays[i] < base || delays[i] > base+maxJitter {
			t.Errorf("delay[%d] = %v, want between %v and %v", i, delays[i], base, base+maxJitter)
		}
	}
}

func TestHealthBasedProcessorReordering(t *testing.T) {
	s := store.New()
	tracker := health.NewTracker(5 * time.Minute)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Degrade StripeLatam's health: 5 timeouts.
	for i := 0; i < 5; i++ {
		tracker.Record("StripeLatam", false, true)
	}
	// PayUSouth is healthy: 5 successes.
	for i := 0; i < 5; i++ {
		tracker.Record("PayUSouth", true, false)
	}

	// Script: both processors succeed on first call.
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"PayUSouth":   {{Approved: true}},
		"StripeLatam": {{Approved: true}},
	})

	engine := retry.NewEngine(s, sim, tracker, noopSleep, logger)

	// Request with StripeLatam first, but health should reorder to PayUSouth first.
	txn, err := engine.Execute(makeRequest("StripeLatam", "PayUSouth"))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if txn.Status != model.StatusApproved {
		t.Errorf("status = %q, want approved", txn.Status)
	}
	if txn.ProcessorUsed != "PayUSouth" {
		t.Errorf("processor_used = %q, want PayUSouth (healthier)", txn.ProcessorUsed)
	}
	if len(txn.Attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(txn.Attempts))
	}
}

func TestHealthTrackerIntegration(t *testing.T) {
	s := store.New()
	tracker := health.NewTracker(5 * time.Minute)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Script: StripeLatam always times out, PayUSouth succeeds.
	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {{ErrorType: model.ErrTimeout, ErrorMessage: "timeout"}},
		"PayUSouth":   {{Approved: true}},
	})

	engine := retry.NewEngine(s, sim, tracker, noopSleep, logger)

	txn, err := engine.Execute(makeRequest("StripeLatam", "PayUSouth"))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if txn.Status != model.StatusApproved {
		t.Errorf("status = %q, want approved", txn.Status)
	}

	// Verify health was recorded.
	stripeHealth := tracker.GetHealth("StripeLatam")
	if stripeHealth.TotalCalls != 1 || stripeHealth.SuccessRate != 0 {
		t.Errorf("StripeLatam health: calls=%d success=%.2f, want calls=1 success=0",
			stripeHealth.TotalCalls, stripeHealth.SuccessRate)
	}

	payuHealth := tracker.GetHealth("PayUSouth")
	if payuHealth.TotalCalls != 1 || payuHealth.SuccessRate != 1.0 {
		t.Errorf("PayUSouth health: calls=%d success=%.2f, want calls=1 success=1.0",
			payuHealth.TotalCalls, payuHealth.SuccessRate)
	}
}
