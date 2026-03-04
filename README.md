# CloudMarket Retry Orchestrator

A payment retry orchestration service that intelligently retries failed transactions across multiple processors, reducing revenue loss from transient processor errors.

## Problem

CloudMarket loses $15-20K/month from transactions that fail due to transient processor errors (timeouts, rate limits, temporary outages). This service automatically retries failed transactions — switching processors or retrying with backoff — and exposes full attempt history for analytics.

## Quick Start

**Prerequisites:** Go 1.24+

```bash
go run main.go
# Server starts on :8080 (override with PORT env var)
# PORT=3000 go run main.go

# Submit a transaction
curl -X POST http://localhost:8080/transactions \
  -H "Content-Type: application/json" \
  -d '{
    "amount": 99.99,
    "currency": "MXN",
    "payment_method": "card_visa_4242",
    "merchant_id": "cloudmarket_mx",
    "processor_order": ["StripeLatam", "PayUSouth", "EbanxBR"]
  }'

# Run the full demo (20+ diverse requests)
chmod +x scripts/demo.sh && ./scripts/demo.sh
```

## API

### POST /transactions
Creates a transaction and runs the retry orchestration loop synchronously.

**Request:**
```json
{
  "amount": 99.99,
  "currency": "MXN",
  "payment_method": "card_visa_4242",
  "merchant_id": "cloudmarket_mx",
  "processor_order": ["StripeLatam", "PayUSouth", "EbanxBR"]
}
```

| Field | Description |
|-------|-------------|
| `amount` | Transaction amount (must be positive) |
| `currency` | ISO currency code (e.g., `MXN`, `COP`, `CLP`, `USD`) |
| `payment_method` | Card or payment token identifier |
| `merchant_id` | Identifies the merchant for logging and analytics (e.g., `cloudmarket_mx`) |
| `processor_order` | Ordered fallback list of processors: `StripeLatam`, `PayUSouth`, `EbanxBR` |

**Response (200 / 422 / 502):**
```json
{
  "transaction_id": "txn_a1b2c3d4e5f6",
  "status": "approved",
  "processor_used": "PayUSouth",
  "attempts": 2,
  "final_error": ""
}
```

**Validated fields:** `amount` (must be positive), `currency` (required), `payment_method` (required), `merchant_id` (required), `processor_order` (at least one, must be known processors). Returns `400 Bad Request` with error message and machine-parseable `code` field if any validation fails.

**Error response format:**
```json
{"error": "amount must be positive", "code": "INVALID_AMOUNT"}
```

Error codes: `INVALID_JSON`, `INVALID_AMOUNT`, `MISSING_CURRENCY`, `MISSING_PAYMENT_METHOD`, `MISSING_PROCESSOR_ORDER`, `MISSING_MERCHANT_ID`, `UNKNOWN_PROCESSOR`, `INTERNAL_ERROR`.

| Status | Meaning |
|--------|---------|
| 200    | Transaction approved |
| 400    | Validation error (missing/invalid fields, unknown processor) |
| 422    | Hard decline (card expired, insufficient funds, etc.) |
| 502    | All retries exhausted |

### GET /transactions/{id}
Returns full transaction with attempt history — each attempt includes processor, response type, error type, retry action taken, duration, and timestamp.

**Example:**
```bash
curl http://localhost:8080/transactions/txn_a1b2c3d4e5f6
```

**Response:**
```json
{
  "transaction_id": "txn_a1b2c3d4e5f6",
  "amount": 99.99,
  "currency": "MXN",
  "payment_method": "card_visa_4242",
  "merchant_id": "cloudmarket_mx",
  "processor_order": ["StripeLatam", "PayUSouth", "EbanxBR"],
  "status": "approved",
  "processor_used": "PayUSouth",
  "attempts": [
    {
      "number": 1,
      "processor": "StripeLatam",
      "status": "failed",
      "response_type": "soft_decline",
      "error_type": "timeout",
      "error_message": "StripeLatam timed out",
      "retry_action": "switch_processor",
      "duration_ms": 2150.5,
      "timestamp": "2026-03-04T12:00:00Z"
    },
    {
      "number": 2,
      "processor": "PayUSouth",
      "status": "approved",
      "response_type": "approved",
      "duration_ms": 85.3,
      "timestamp": "2026-03-04T12:00:01Z"
    }
  ],
  "created_at": "2026-03-04T12:00:00Z",
  "completed_at": "2026-03-04T12:00:01Z",
  "total_processing_time_ms": 1235.7
}
```

### GET /transactions
Returns a list of all transactions with full attempt history. Supports optional pagination.

```bash
curl http://localhost:8080/transactions
curl http://localhost:8080/transactions?limit=10&offset=0
```

| Param | Description |
|-------|-------------|
| `limit` | Maximum number of transactions to return (default: all) |
| `offset` | Number of transactions to skip (default: 0) |

Results are sorted by creation time (newest first).

**Response (200):**
```json
[
  {
    "transaction_id": "txn_a1b2c3d4e5f6",
    "amount": 99.99,
    "currency": "MXN",
    "payment_method": "card_visa_4242",
    "merchant_id": "cloudmarket_mx",
    "processor_order": ["StripeLatam", "PayUSouth", "EbanxBR"],
    "status": "approved",
    "processor_used": "PayUSouth",
    "attempts": [ ... ],
    "created_at": "2026-03-04T12:00:00Z",
    "completed_at": "2026-03-04T12:00:01Z",
    "total_processing_time_ms": 1235.7
  }
]
```

### GET /processors/health
Returns per-processor health metrics from a rolling window: success rate, timeout rate, and composite health score.

```bash
curl http://localhost:8080/processors/health
```

**Response (200):**
```json
[
  {
    "processor": "StripeLatam",
    "total_calls": 42,
    "success_rate": 0.90,
    "timeout_rate": 0.03,
    "health_score": 0.885
  },
  {
    "processor": "PayUSouth",
    "total_calls": 38,
    "success_rate": 0.84,
    "timeout_rate": 0.05,
    "health_score": 0.815
  }
]
```

## Retry Strategy

| Error Type | Action | Rationale |
|---|---|---|
| `timeout`, `processor_unavailable` | Switch to next processor | Processor is down or slow |
| `rate_limit_exceeded` | Retry same processor after delay | Transient, will recover |
| `service_error`, `try_again_later` | Retry same processor | Temporary issue |
| `insufficient_funds`, `card_expired`, `fraudulent_card`, `do_not_honor` | Abort immediately | Will never succeed |

- **Max attempts:** 4 (initial + 3 retries) — hardcoded constants in `retry/engine.go`
- **Exponential backoff:** 500ms → 1s → 2s (with 0-25% random jitter to prevent thundering herd)
- **Processor fallback:** Ordered list, advances on switch-processor errors
- **Health-aware ordering:** Processors reordered by health score when tracker is enabled

## Architecture

```
main.go                        Entrypoint, dependency wiring
internal/
├── model/model.go             Domain types, retry rules, ID generation
├── store/store.go             Thread-safe in-memory store (sync.RWMutex)
├── processor/simulator.go     ScriptedSimulator (tests) + RealisticSimulator (demo)
├── retry/engine.go            Core retry orchestration engine
├── health/tracker.go          Rolling-window processor health scoring
├── handler/handler.go         HTTP handlers with validation
├── middleware/logging.go      Structured JSON request logging (slog)
└── router/router.go           Chi router setup
test/
├── scenarios_test.go          12 table-driven retry behavior tests
├── concurrent_test.go         Concurrent transaction isolation tests
└── http_test.go               HTTP integration tests (handlers, status codes, JSON)
```

## Design Decisions

- **Synchronous API:** The retry loop blocks (max ~3.5s). Simpler than async + polling and sufficient for the use case.
- **Interface-based simulator:** `processor.Simulator` interface enables deterministic tests via `ScriptedSimulator` and realistic demos via `RealisticSimulator`.
- **Injectable sleep function:** Backoff delays are controlled via a `SleepFunc` parameter — tests pass a no-op to run instantly.
- **Nil-safe health tracker:** The engine works with or without the health tracker. Stretch goal doesn't break core functionality.
- **Config-driven retry rules:** `ErrorType -> RetryAction` map in `model.go` makes the strategy declarative and easy to extend.
- **Copy-on-read store:** `Get()` returns deep copies to prevent race conditions between the engine writing and handlers reading.
- **Internal duration hidden from JSON:** `Attempt.Duration` (Go `time.Duration`) is tagged `json:"-"` to keep it out of API responses; only the computed `duration_ms` float is serialized.

## Testing

```bash
# Run all tests with race detector
go test ./... -v -race

# Run with coverage
go test ./... -v -race -cover
```

Test scenarios cover: immediate success, timeout-then-switch, rate-limit-then-retry, hard decline abort (insufficient funds, card expired, fraudulent card, do not honor), all-processors-exhausted, processor unavailable cascade, service error retry, try-again-later retry, rate limit exhaustion, mixed strategy chains, multi-currency (MXN, COP, CLP, USD), exponential backoff value verification, health-based processor reordering, concurrent transaction isolation, pagination, processor name validation, error codes, and HTTP integration (status codes, JSON validation, error handling).

## Observability

All retry decisions are logged as structured JSON via `slog`:
- Transaction lifecycle: started, attempt succeeded/failed, backoff applied
- Each log entry includes: `transaction_id`, `attempt`, `processor`, `error_type`, `retry_action`, `response_type`, `duration_ms`
- HTTP request logging: method, path, status, duration, remote addr
- Each attempt records `response_type`: `"approved"`, `"hard_decline"`, or `"soft_decline"`
- Completed transactions include `completed_at` timestamp and `total_processing_time_ms`

## Troubleshooting

**Q: What does a 502 response mean?**
A: All retry attempts were exhausted without a successful approval. Use `GET /transactions/{id}` to see the full attempt history — each attempt shows which processor failed and why.

**Q: Why did I get a 422 instead of retrying?**
A: The processor returned a hard decline (`insufficient_funds`, `card_expired`, `fraudulent_card`, `do_not_honor`). These are permanent errors that won't succeed on retry, so the engine aborts immediately.

**Q: How do I use a custom port?**
A: Set the `PORT` environment variable: `PORT=3000 go run main.go`

**Q: How does health-based reordering work?**
A: Processors are reordered by a composite health score (`success_rate - timeout_rate * 0.5`) computed over a 5-minute rolling window. Healthier processors are tried first. Unknown processors default to score 1.0.

**Q: What if all processors are down?**
A: The engine tries each processor in order, switching after timeouts or unavailability errors, and returns `502` with `"all processors exhausted"` once all have been attempted or max attempts reached.
