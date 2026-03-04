package test

import (
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/yuno/ai-challenge/internal/model"
	"github.com/yuno/ai-challenge/internal/processor"
	"github.com/yuno/ai-challenge/internal/retry"
	"github.com/yuno/ai-challenge/internal/store"
)

func TestConcurrentTransactionIsolation(t *testing.T) {
	s := store.New()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Each transaction gets its own scripted simulator via the shared one.
	// Use a realistic simulator that mostly succeeds for this concurrency test.
	sim := processor.NewRealisticSimulator()
	engine := retry.NewEngine(s, sim, nil, noopSleep, logger)

	const numTxns = 10
	var wg sync.WaitGroup
	results := make([]*model.Transaction, numTxns)
	errors := make([]error, numTxns)

	wg.Add(numTxns)
	for i := 0; i < numTxns; i++ {
		go func(idx int) {
			defer wg.Done()
			req := model.TransactionRequest{
				Amount:         float64(100 + idx),
				Currency:       "MXN",
				PaymentMethod:  "card_visa_4242",
				MerchantID:     "cloudmarket_mx",
				ProcessorOrder: []string{"StripeLatam", "PayUSouth", "EbanxBR"},
			}
			results[idx], errors[idx] = engine.Execute(req)
		}(i)
	}
	wg.Wait()

	// Verify all completed without error.
	ids := make(map[string]bool)
	for i := 0; i < numTxns; i++ {
		if errors[i] != nil {
			t.Errorf("transaction %d error: %v", i, errors[i])
			continue
		}
		txn := results[i]
		if txn == nil {
			t.Errorf("transaction %d: nil result", i)
			continue
		}

		// Each must have a unique ID.
		if ids[txn.ID] {
			t.Errorf("duplicate transaction ID: %s", txn.ID)
		}
		ids[txn.ID] = true

		// Must have at least one attempt.
		if len(txn.Attempts) == 0 {
			t.Errorf("transaction %d has no attempts", i)
		}

		// Status must be terminal.
		switch txn.Status {
		case model.StatusApproved, model.StatusDeclined, model.StatusFailed:
			// ok
		default:
			t.Errorf("transaction %d has non-terminal status: %s", i, txn.Status)
		}

		// Verify amount matches.
		expectedAmount := float64(100 + i)
		if txn.Amount != expectedAmount {
			t.Errorf("transaction %d amount = %.2f, want %.2f", i, txn.Amount, expectedAmount)
		}

		// Verify CompletedAt is set.
		if txn.CompletedAt.IsZero() {
			t.Errorf("transaction %d CompletedAt is zero", i)
		}

		// Verify TotalProcessingTimeMs is positive.
		if txn.TotalProcessingTimeMs <= 0 {
			t.Errorf("transaction %d TotalProcessingTimeMs = %f, want > 0", i, txn.TotalProcessingTimeMs)
		}

		// Verify each attempt has ResponseType set.
		for ai, a := range txn.Attempts {
			if a.ResponseType == "" {
				t.Errorf("transaction %d attempt[%d].ResponseType is empty", i, ai)
			}
		}
	}

	// Verify we can fetch each from store.
	for i := 0; i < numTxns; i++ {
		if results[i] == nil {
			continue
		}
		fetched, err := s.Get(results[i].ID)
		if err != nil {
			t.Errorf("store.Get(%s) error: %v", results[i].ID, err)
			continue
		}
		if fetched.Amount != results[i].Amount {
			t.Errorf("store mismatch for %s: got amount %.2f, want %.2f",
				results[i].ID, fetched.Amount, results[i].Amount)
		}
	}
}

func TestConcurrentWithScriptedSimulator(t *testing.T) {
	// Run 10 concurrent transactions where all succeed on first try.
	s := store.New()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	sim := processor.NewScriptedSimulator(map[string][]processor.ScriptedResponse{
		"StripeLatam": {
			{Approved: true}, {Approved: true}, {Approved: true}, {Approved: true}, {Approved: true},
			{Approved: true}, {Approved: true}, {Approved: true}, {Approved: true}, {Approved: true},
		},
	})
	engine := retry.NewEngine(s, sim, nil, noopSleep, logger)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)

	approved := make([]bool, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			txn, err := engine.Execute(makeRequest("StripeLatam"))
			if err == nil && txn.Status == model.StatusApproved {
				approved[idx] = true
			}
		}(i)
	}
	wg.Wait()

	count := 0
	for _, ok := range approved {
		if ok {
			count++
		}
	}
	if count != n {
		t.Errorf("approved %d/%d transactions, want all %d", count, n, n)
	}
}

// Verify noopSleep doesn't cause issues (sanity check).
func TestNoopSleepDoesNotBlock(t *testing.T) {
	start := time.Now()
	noopSleep(10 * time.Second)
	if time.Since(start) > 100*time.Millisecond {
		t.Error("noopSleep should not actually sleep")
	}
}
