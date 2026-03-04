package model

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// ErrorType classifies processor response errors.
type ErrorType string

const (
	ErrNone                 ErrorType = ""
	ErrTimeout              ErrorType = "timeout"
	ErrProcessorUnavailable ErrorType = "processor_unavailable"
	ErrRateLimitExceeded    ErrorType = "rate_limit_exceeded"
	ErrServiceError         ErrorType = "service_error"
	ErrTryAgainLater        ErrorType = "try_again_later"
	ErrInsufficientFunds    ErrorType = "insufficient_funds"
	ErrCardExpired          ErrorType = "card_expired"
	ErrFraudulentCard       ErrorType = "fraudulent_card"
	ErrDoNotHonor           ErrorType = "do_not_honor"
)

// RetryAction determines what the engine does after a failure.
type RetryAction string

const (
	ActionAbort           RetryAction = "abort"
	ActionSwitchProcessor RetryAction = "switch_processor"
	ActionSameProcessor   RetryAction = "same_processor"
)

// RetryRules maps error types to retry actions. Each entry encodes a business
// decision about whether a failure is transient (retry) or permanent (abort).
var RetryRules = map[ErrorType]RetryAction{
	// Switch processor: the current processor is unreachable or too slow.
	ErrTimeout:              ActionSwitchProcessor, // processor didn't respond in time — try another
	ErrProcessorUnavailable: ActionSwitchProcessor, // processor is down — try another

	// Retry same processor: the error is transient and the processor will likely recover.
	ErrRateLimitExceeded: ActionSameProcessor, // back off and retry — rate limits are short-lived
	ErrServiceError:      ActionSameProcessor, // temporary internal issue — worth retrying
	ErrTryAgainLater:     ActionSameProcessor, // processor explicitly asked us to retry

	// Abort: the card or account has a permanent problem — retrying won't help.
	ErrInsufficientFunds: ActionAbort, // cardholder's balance is too low
	ErrCardExpired:       ActionAbort, // card is no longer valid
	ErrFraudulentCard:    ActionAbort, // issuer flagged the card as fraudulent
	ErrDoNotHonor:        ActionAbort, // issuer refused without specific reason — treat as permanent
}

// TransactionStatus represents the final state of a transaction.
type TransactionStatus string

const (
	StatusApproved TransactionStatus = "approved"
	StatusDeclined TransactionStatus = "declined"
	StatusFailed   TransactionStatus = "failed"
	StatusPending  TransactionStatus = "pending"
)

// Attempt records a single processor call.
type Attempt struct {
	Number       int           `json:"number"`
	Processor    string        `json:"processor"`
	Status       string        `json:"status"` // "approved" or "failed"
	ResponseType string        `json:"response_type"`
	ErrorType    ErrorType     `json:"error_type,omitempty"`
	ErrorMessage string        `json:"error_message,omitempty"`
	RetryAction  RetryAction   `json:"retry_action,omitempty"`
	Duration     time.Duration `json:"-"`
	DurationMs   float64       `json:"duration_ms"`
	Timestamp    time.Time     `json:"timestamp"`
}

// Transaction is the top-level domain object.
type Transaction struct {
	ID             string            `json:"transaction_id"`
	Amount         float64           `json:"amount"`
	Currency       string            `json:"currency"`
	PaymentMethod  string            `json:"payment_method"`
	MerchantID     string            `json:"merchant_id"`
	ProcessorOrder []string          `json:"processor_order"`
	Status         TransactionStatus `json:"status"`
	ProcessorUsed  string            `json:"processor_used,omitempty"`
	FinalError     string            `json:"final_error,omitempty"`
	Attempts              []Attempt         `json:"attempts"`
	CreatedAt             time.Time         `json:"created_at"`
	CompletedAt           time.Time         `json:"completed_at,omitempty"`
	TotalProcessingTimeMs float64           `json:"total_processing_time_ms,omitempty"`
}

// TransactionRequest is the API input.
type TransactionRequest struct {
	Amount         float64  `json:"amount"`
	Currency       string   `json:"currency"`
	PaymentMethod  string   `json:"payment_method"`
	MerchantID     string   `json:"merchant_id"`
	ProcessorOrder []string `json:"processor_order"`
}

// TransactionResponse is the API output for POST.
type TransactionResponse struct {
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
	ProcessorUsed string `json:"processor_used,omitempty"`
	Attempts      int    `json:"attempts"`
	FinalError    string `json:"final_error,omitempty"`
}

// ProcessorResult is returned by the simulator.
type ProcessorResult struct {
	Approved     bool
	ErrorType    ErrorType
	ErrorMessage string
	Duration     time.Duration
}

// NewTransactionID generates a txn_ prefixed random ID.
func NewTransactionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "txn_" + hex.EncodeToString(b)
}
