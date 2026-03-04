package retry

import (
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/yuno/ai-challenge/internal/health"
	"github.com/yuno/ai-challenge/internal/model"
	"github.com/yuno/ai-challenge/internal/processor"
	"github.com/yuno/ai-challenge/internal/store"
)

const (
	MaxAttempts   = 4
	BaseBackoffMs = 500
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

// attemptOutcome captures the result of executing a single processor attempt.
type attemptOutcome struct {
	attempt   model.Attempt
	action    model.RetryAction
	approved  bool
	isTimeout bool
}

// runAttempt calls the processor and classifies the result.
func (e *Engine) runAttempt(processorName string, amount float64, attemptNum int) attemptOutcome {
	start := time.Now()
	result := e.simulator.Process(processorName, amount)
	elapsed := time.Since(start)

	attempt := model.Attempt{
		Number:     attemptNum,
		Processor:  processorName,
		Duration:   elapsed,
		DurationMs: float64(elapsed.Nanoseconds()) / 1e6,
		Timestamp:  time.Now(),
	}

	if result.Approved {
		attempt.Status = "approved"
		attempt.ResponseType = "approved"
		return attemptOutcome{attempt: attempt, approved: true}
	}

	attempt.Status = "failed"
	attempt.ErrorType = result.ErrorType
	attempt.ErrorMessage = result.ErrorMessage

	action, ok := model.RetryRules[result.ErrorType]
	if !ok {
		action = model.ActionAbort
	}
	attempt.RetryAction = action

	if action == model.ActionAbort {
		attempt.ResponseType = "hard_decline"
	} else {
		attempt.ResponseType = "soft_decline"
	}

	return attemptOutcome{
		attempt:   attempt,
		action:    action,
		isTimeout: result.ErrorType == model.ErrTimeout,
	}
}

// calcBackoff returns an exponential backoff duration with random jitter.
// Formula: 500ms * 2^(attemptNum-1) + rand(0, 25% of base).
func (e *Engine) calcBackoff(attemptNum int) time.Duration {
	backoff := time.Duration(BaseBackoffMs*(1<<(attemptNum-1))) * time.Millisecond
	jitter := time.Duration(rand.Int63n(int64(backoff / 4)))
	return backoff + jitter
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
		outcome := e.runAttempt(currentProcessor, txn.Amount, attemptNum)

		_ = e.store.AddAttempt(txn.ID, outcome.attempt)

		if outcome.approved {
			_ = e.store.UpdateStatus(txn.ID, model.StatusApproved, currentProcessor, "", time.Now())
			if e.health != nil {
				e.health.Record(currentProcessor, true, false)
			}
			e.logger.Info("attempt succeeded",
				"transaction_id", txn.ID,
				"attempt", attemptNum,
				"processor", currentProcessor,
				"duration_ms", outcome.attempt.DurationMs,
			)
			return e.store.Get(txn.ID)
		}

		lastError = outcome.attempt.ErrorMessage

		if e.health != nil {
			e.health.Record(currentProcessor, false, outcome.isTimeout)
		}

		e.logger.Info("attempt failed",
			"transaction_id", txn.ID,
			"attempt", attemptNum,
			"processor", currentProcessor,
			"error_type", outcome.attempt.ErrorType,
			"error_message", outcome.attempt.ErrorMessage,
			"retry_action", outcome.action,
			"response_type", outcome.attempt.ResponseType,
			"duration_ms", outcome.attempt.DurationMs,
		)

		switch outcome.action {
		case model.ActionAbort:
			// Hard decline — stop immediately. Record which processor declined.
			_ = e.store.UpdateStatus(txn.ID, model.StatusDeclined, currentProcessor, lastError, time.Now())
			return e.store.Get(txn.ID)

		case model.ActionSwitchProcessor:
			// Advance to the next processor in the ordered fallback list.
			processorIdx++
			if processorIdx >= len(processors) && attemptNum < MaxAttempts {
				lastError = "all processors exhausted"
			}

		case model.ActionSameProcessor:
			// Transient error — stay on current processor and retry after backoff.
		}

		// Exponential backoff with jitter.
		if attemptNum < MaxAttempts && processorIdx < len(processors) {
			backoff := e.calcBackoff(attemptNum)
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
