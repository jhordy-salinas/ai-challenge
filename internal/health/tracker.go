package health

import (
	"sync"
	"time"
)

// event records a single processor outcome.
type event struct {
	timestamp time.Time
	success   bool
	timeout   bool
}

// ProcessorHealth holds computed health metrics for a processor.
type ProcessorHealth struct {
	Processor   string  `json:"processor"`
	TotalCalls  int     `json:"total_calls"`
	SuccessRate float64 `json:"success_rate"`
	TimeoutRate float64 `json:"timeout_rate"`
	HealthScore float64 `json:"health_score"`
}

// Tracker maintains a rolling window of processor outcomes.
type Tracker struct {
	mu     sync.RWMutex
	events map[string][]event
	window time.Duration
}

// NewTracker creates a health tracker with the given rolling window.
func NewTracker(window time.Duration) *Tracker {
	return &Tracker{
		events: make(map[string][]event),
		window: window,
	}
}

// Record adds a processor outcome to the tracker.
func (t *Tracker) Record(processor string, success, timeout bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events[processor] = append(t.events[processor], event{
		timestamp: time.Now(),
		success:   success,
		timeout:   timeout,
	})
}

// prune removes events outside the rolling window (must hold write lock).
func (t *Tracker) prune(processor string) {
	cutoff := time.Now().Add(-t.window)
	events := t.events[processor]
	i := 0
	for i < len(events) && events[i].timestamp.Before(cutoff) {
		i++
	}
	t.events[processor] = events[i:]
}

// GetHealth returns health metrics for a specific processor.
func (t *Tracker) GetHealth(processor string) ProcessorHealth {
	t.mu.Lock()
	t.prune(processor)
	events := make([]event, len(t.events[processor]))
	copy(events, t.events[processor])
	t.mu.Unlock()

	h := ProcessorHealth{Processor: processor, TotalCalls: len(events)}
	if len(events) == 0 {
		// No data defaults to healthy so new/unknown processors aren't penalized.
		h.HealthScore = 1.0
		return h
	}

	var successes, timeouts int
	for _, e := range events {
		if e.success {
			successes++
		}
		if e.timeout {
			timeouts++
		}
	}

	h.SuccessRate = float64(successes) / float64(len(events))
	h.TimeoutRate = float64(timeouts) / float64(len(events))
	// Health score = SuccessRate - (TimeoutRate * 0.5). Timeouts get extra penalty
	// because they also consume latency budget, not just failure budget.
	h.HealthScore = h.SuccessRate - (h.TimeoutRate * 0.5)
	if h.HealthScore < 0 {
		h.HealthScore = 0
	}
	return h
}

// GetAllHealth returns health metrics for all tracked processors.
func (t *Tracker) GetAllHealth() []ProcessorHealth {
	t.mu.Lock()
	processors := make([]string, 0, len(t.events))
	for p := range t.events {
		processors = append(processors, p)
	}
	t.mu.Unlock()

	results := make([]ProcessorHealth, 0, len(processors))
	for _, p := range processors {
		results = append(results, t.GetHealth(p))
	}
	return results
}

// ScoreProcessors returns a health-score-ordered copy of the processor list.
// Processors with higher health scores come first. Unknown processors
// are assumed healthy (score 1.0).
func (t *Tracker) ScoreProcessors(processors []string) []string {
	if t == nil || len(processors) <= 1 {
		return processors
	}

	type scored struct {
		name  string
		score float64
	}
	items := make([]scored, len(processors))
	for i, p := range processors {
		items[i] = scored{name: p, score: t.GetHealth(p).HealthScore}
	}

	// Simple insertion sort (small N).
	for i := 1; i < len(items); i++ {
		key := items[i]
		j := i - 1
		for j >= 0 && items[j].score < key.score {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = key
	}

	result := make([]string, len(items))
	for i, item := range items {
		result[i] = item.name
	}
	return result
}
