package retry

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/yuno/ai-challenge/internal/health"
	"github.com/yuno/ai-challenge/internal/model"
	"github.com/yuno/ai-challenge/internal/processor"
	"github.com/yuno/ai-challenge/internal/store"
)

const (
	MaxAttempts    = 4
	BaseBackoffMs  = 500
)

// SleepFunc is the delay function, injectable for testing.
type SleepFunc func(time.Duration)

// Engine orchestrates payment retries.
type Engine struct {
	store     *store.Store
	simulator processor.Simulator
	health    *health.Tracker // nil-safe: works without health tracking
	sleep     SleepFunc
	logger    *slog.Logger
}

// NewEngine creates a retry engine.
func NewEngine(s *store.Store, sim processor.Simulator, h *health.Tracker, sleep SleepFunc, logger *slog.Logger) *Engine {
	if sleep == nil {
		sleep = time.Sleep
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		store:     s,
		simulator: sim,
		health:    h,
		sleep:     sleep,
		logger:    logger,
	}
}

// Execute runs the full retry orchestration for a transaction request.
func (e *Engine) Execute(req model.TransactionRequest) (*model.Transaction, error) {
	txn := &model.Transaction{
		ID:             model.NewTransactionID(),
		Amount:         req.Amount,
		Currency:       req.Currency,
		PaymentMethod:  req.PaymentMethod,
		MerchantID:     req.MerchantID,
		ProcessorOrder: req.ProcessorOrder,
		Status:         model.StatusPending,
		CreatedAt:      time.Now(),
	}

	if err := e.store.Create(txn); err != nil {
		return nil, fmt.Errorf("create transaction: %w", err)
	}

	// Reorder processors by health score if tracker is available.
	processors := make([]string, len(req.ProcessorOrder))
	copy(processors, req.ProcessorOrder)
	if e.health != nil {
		processors = e.health.ScoreProcessors(processors)
	}

	e.logger.Info("transaction started",
		"transaction_id", txn.ID,
		"amount", txn.Amount,
		"currency", txn.Currency,
		"processors", processors,
	)

	processorIdx := 0
	var lastError string

	for attemptNum := 1; attemptNum <= MaxAttempts; attemptNum++ {
		// Guard: stop if all processors in the ordered list have been tried.
		if processorIdx >= len(processors) {
			lastError = "all processors exhausted"
			break
		}

		currentProcessor := processors[processorIdx]

		// Call processor simulator.
		start := time.Now()
		result := e.simulator.Process(currentProcessor, txn.Amount)
		elapsed := time.Since(start)

		attempt := model.Attempt{
			Number:    attemptNum,
			Processor: currentProcessor,
			Duration:  elapsed,
			DurationMs: float64(elapsed.Nanoseconds()) / 1e6,
			Timestamp: time.Now(),
		}

		if result.Approved {
			attempt.Status = "approved"
			attempt.ResponseType = "approved"
			_ = e.store.AddAttempt(txn.ID, attempt)
			_ = e.store.UpdateStatus(txn.ID, model.StatusApproved, currentProcessor, "", time.Now())

			if e.health != nil {
				e.health.Record(currentProcessor, true, false)
			}

			e.logger.Info("attempt succeeded",
				"transaction_id", txn.ID,
				"attempt", attemptNum,
				"processor", currentProcessor,
				"duration_ms", attempt.DurationMs,
			)

			return e.store.Get(txn.ID)
		}

		// Failed attempt.
		attempt.Status = "failed"
		attempt.ErrorType = result.ErrorType
		attempt.ErrorMessage = result.ErrorMessage
		lastError = result.ErrorMessage

		// Determine retry action. Unknown error types default to abort (safe fallback).
		action, ok := model.RetryRules[result.ErrorType]
		if !ok {
			action = model.ActionAbort
		}
		attempt.RetryAction = action

		// Classify response: hard_decline for abort errors, soft_decline for retryable errors.
		if action == model.ActionAbort {
			attempt.ResponseType = "hard_decline"
		} else {
			attempt.ResponseType = "soft_decline"
		}

		_ = e.store.AddAttempt(txn.ID, attempt)

		if e.health != nil {
			e.health.Record(currentProcessor, false, result.ErrorType == model.ErrTimeout)
		}

		e.logger.Info("attempt failed",
			"transaction_id", txn.ID,
			"attempt", attemptNum,
			"processor", currentProcessor,
			"error_type", result.ErrorType,
			"error_message", result.ErrorMessage,
			"retry_action", action,
			"response_type", attempt.ResponseType,
			"duration_ms", attempt.DurationMs,
		)

		switch action {
		case model.ActionAbort:
			// Hard decline — the card/account has a permanent problem, stop immediately.
			_ = e.store.UpdateStatus(txn.ID, model.StatusDeclined, "", lastError, time.Now())
			return e.store.Get(txn.ID)

		case model.ActionSwitchProcessor:
			// Advance to the next processor in the ordered fallback list.
			processorIdx++
			// Edge case: we've run through all processors but still have retry budget.
			// Mark exhausted so the top-of-loop guard breaks on next iteration.
			if processorIdx >= len(processors) && attemptNum < MaxAttempts {
				lastError = "all processors exhausted"
			}

		case model.ActionSameProcessor:
			// Transient error — stay on current processor and retry after backoff.
		}

		// Exponential backoff: 500ms * 2^(attempt-1) → 500ms, 1s, 2s
		if attemptNum < MaxAttempts && processorIdx < len(processors) {
			backoff := time.Duration(BaseBackoffMs*(1<<(attemptNum-1))) * time.Millisecond
			e.logger.Info("backoff",
				"transaction_id", txn.ID,
				"delay_ms", backoff.Milliseconds(),
			)
			e.sleep(backoff)
		}
	}

	// All attempts exhausted.
	_ = e.store.UpdateStatus(txn.ID, model.StatusFailed, "", lastError, time.Now())
	return e.store.Get(txn.ID)
}
