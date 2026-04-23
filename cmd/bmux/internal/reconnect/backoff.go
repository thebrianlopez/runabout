package reconnect

import (
	"math"
	"time"

	"github.com/blo-grindr/bmux/internal/config"
)

const minDelay = time.Second // floor: prevents accidental 0-delay tight loops

type backoffScheduler struct {
	cfg config.ReconnectConfig
}

// NewBackoffScheduler creates a scheduler from config.
// Formula: delay = min(InitialInterval × Multiplier^attempt, MaxInterval), floor 1s.
func NewBackoffScheduler(cfg config.ReconnectConfig) BackoffScheduler {
	return &backoffScheduler{cfg: cfg}
}

func (s *backoffScheduler) Next(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	initial := s.cfg.InitialInterval.Duration // config.Duration embeds time.Duration
	if initial <= 0 {
		initial = 2 * time.Second
	}
	maxInterval := s.cfg.MaxInterval.Duration
	if maxInterval <= 0 {
		maxInterval = 5 * time.Minute
	}
	multiplier := s.cfg.Multiplier
	if multiplier < 1.0 {
		multiplier = 2.0
	}

	delay := time.Duration(float64(initial) * math.Pow(multiplier, float64(attempt)))
	if delay > maxInterval {
		delay = maxInterval
	}
	if delay < minDelay {
		delay = minDelay
	}
	return delay
}

func (s *backoffScheduler) Config() config.ReconnectConfig { return s.cfg }
