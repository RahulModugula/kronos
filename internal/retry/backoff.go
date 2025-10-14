package retry

import (
	"math"
	"math/rand"
	"time"
)

// Config controls the backoff behavior.
type Config struct {
	Base       time.Duration // initial delay (default 1s)
	Max        time.Duration // maximum delay (default 5m)
	Multiplier float64       // growth factor (default 2.0)
	Jitter     float64       // fraction of delay to randomize, 0–1 (default 0.2)
}

var DefaultConfig = Config{
	Base:       time.Second,
	Max:        5 * time.Minute,
	Multiplier: 2.0,
	Jitter:     0.2,
}

// Next returns the next retry delay for the given attempt (0-indexed).
func (c Config) Next(attempt int) time.Duration {
	if c.Base == 0 {
		c = DefaultConfig
	}
	delay := float64(c.Base) * math.Pow(c.Multiplier, float64(attempt))
	if c.Max > 0 && time.Duration(delay) > c.Max {
		delay = float64(c.Max)
	}
	// Add ±jitter
	if c.Jitter > 0 {
		jitter := delay * c.Jitter
		delay += jitter*rand.Float64()*2 - jitter
	}
	if delay < 0 {
		delay = float64(c.Base)
	}
	return time.Duration(delay)
}

// NextTime returns the absolute time for the next retry.
func (c Config) NextTime(attempt int) time.Time {
	return time.Now().UTC().Add(c.Next(attempt))
}
