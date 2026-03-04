package processor

import (
	"math/rand"
	"sync"
	"time"

	"github.com/yuno/ai-challenge/internal/model"
)

// Simulator defines the processor call interface.
type Simulator interface {
	Process(processor string, amount float64) model.ProcessorResult
}

// ScriptedResponse is a pre-configured response for testing.
type ScriptedResponse struct {
	Approved     bool
	ErrorType    model.ErrorType
	ErrorMessage string
}

// ScriptedSimulator returns deterministic responses per processor in order.
type ScriptedSimulator struct {
	mu        sync.Mutex
	responses map[string][]ScriptedResponse
	index     map[string]int
}

// NewScriptedSimulator creates a simulator with scripted responses.
func NewScriptedSimulator(responses map[string][]ScriptedResponse) *ScriptedSimulator {
	return &ScriptedSimulator{
		responses: responses,
		index:     make(map[string]int),
	}
}

// Process returns the next scripted response for the processor.
func (s *ScriptedSimulator) Process(processor string, _ float64) model.ProcessorResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	resps, ok := s.responses[processor]
	if !ok || len(resps) == 0 {
		return model.ProcessorResult{
			Approved:     false,
			ErrorType:    model.ErrProcessorUnavailable,
			ErrorMessage: "no scripted responses for " + processor,
			Duration:     5 * time.Millisecond,
		}
	}

	idx := s.index[processor]
	if idx >= len(resps) {
		// Repeat last scripted response — allows tests to define a steady-state
		// behavior (e.g., "always timeout") without listing N identical entries.
		idx = len(resps) - 1
	}
	resp := resps[idx]
	s.index[processor] = idx + 1

	return model.ProcessorResult{
		Approved:     resp.Approved,
		ErrorType:    resp.ErrorType,
		ErrorMessage: resp.ErrorMessage,
		Duration:     2 * time.Millisecond,
	}
}

// processorProfile defines weighted outcomes for a processor.
type processorProfile struct {
	SuccessRate float64
	TimeoutRate float64
	RateLimit   float64
	// Remainder is split between service_error and hard declines.
}

// RealisticSimulator uses weighted random outcomes per processor.
type RealisticSimulator struct {
	profiles map[string]processorProfile
	rng      *rand.Rand
	mu       sync.Mutex
}

// NewRealisticSimulator creates a simulator with default processor profiles.
func NewRealisticSimulator() *RealisticSimulator {
	return &RealisticSimulator{
		profiles: map[string]processorProfile{
			"StripeLatam": {SuccessRate: 0.90, TimeoutRate: 0.03, RateLimit: 0.02},
			"PayUSouth":   {SuccessRate: 0.85, TimeoutRate: 0.05, RateLimit: 0.03},
			"EbanxBR":     {SuccessRate: 0.80, TimeoutRate: 0.04, RateLimit: 0.03},
		},
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Process simulates a processor call with weighted random outcome.
func (r *RealisticSimulator) Process(processor string, _ float64) model.ProcessorResult {
	profile, ok := r.profiles[processor]
	if !ok {
		return model.ProcessorResult{
			Approved:     false,
			ErrorType:    model.ErrProcessorUnavailable,
			ErrorMessage: "unknown processor: " + processor,
			Duration:     10 * time.Millisecond,
		}
	}

	r.mu.Lock()
	roll := r.rng.Float64()
	latency := time.Duration(50+r.rng.Intn(200)) * time.Millisecond
	timeoutLatency := time.Duration(2000+r.rng.Intn(1000)) * time.Millisecond
	declineIdx := r.rng.Intn(4)
	r.mu.Unlock()

	switch {
	case roll < profile.SuccessRate:
		return model.ProcessorResult{Approved: true, Duration: latency}
	case roll < profile.SuccessRate+profile.TimeoutRate:
		return model.ProcessorResult{
			ErrorType:    model.ErrTimeout,
			ErrorMessage: processor + " timed out",
			Duration:     timeoutLatency,
		}
	case roll < profile.SuccessRate+profile.TimeoutRate+profile.RateLimit:
		return model.ProcessorResult{
			ErrorType:    model.ErrRateLimitExceeded,
			ErrorMessage: processor + " rate limit exceeded",
			Duration:     latency,
		}
	default:
		// Split remaining between service_error and hard declines
		if roll < 0.97 {
			return model.ProcessorResult{
				ErrorType:    model.ErrServiceError,
				ErrorMessage: processor + " internal error",
				Duration:     latency,
			}
		}
		declines := []model.ErrorType{model.ErrInsufficientFunds, model.ErrCardExpired, model.ErrFraudulentCard, model.ErrDoNotHonor}
		return model.ProcessorResult{
			ErrorType:    declines[declineIdx],
			ErrorMessage: "hard decline from " + processor,
			Duration:     latency,
		}
	}
}
