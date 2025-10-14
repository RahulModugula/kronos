package retry_test

import (
	"testing"
	"time"

	"github.com/rahulmodugula/kronos/internal/retry"
)

func TestBackoff_Growth(t *testing.T) {
	cfg := retry.Config{
		Base:       time.Second,
		Max:        5 * time.Minute,
		Multiplier: 2.0,
		Jitter:     0, // no jitter for deterministic test
	}

	prev := time.Duration(0)
	for attempt := 0; attempt < 6; attempt++ {
		d := cfg.Next(attempt)
		if attempt > 0 && d <= prev {
			t.Errorf("attempt %d: delay %v did not grow beyond %v", attempt, d, prev)
		}
		prev = d
	}
}

func TestBackoff_MaxCap(t *testing.T) {
	cfg := retry.Config{
		Base:       time.Second,
		Max:        10 * time.Second,
		Multiplier: 10.0,
		Jitter:     0,
	}
	for attempt := 0; attempt < 10; attempt++ {
		d := cfg.Next(attempt)
		if d > cfg.Max {
			t.Errorf("attempt %d: delay %v exceeds max %v", attempt, d, cfg.Max)
		}
	}
}

func TestBackoff_DefaultConfig(t *testing.T) {
	d := retry.DefaultConfig.Next(0)
	if d <= 0 {
		t.Errorf("expected positive delay, got %v", d)
	}
}
