package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)


// Config is the single source of truth for runtime configuration.
// All values are loaded from environment variables with documented defaults.
type Config struct {
	DatabaseURL       string
	GRPCAddr          string
	MetricsAddr       string
	AdminAddr         string
	WorkerConcurrency int
	PollInterval      time.Duration
	BatchSize         int
	JobTimeout        time.Duration

	// Backoff configuration
	RetryBaseDelay   time.Duration
	RetryMaxDelay    time.Duration
	RetryMultiplier  float64
	RetryJitter      float64
}

// Load reads config from environment variables.
// Returns an error if required fields are missing.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:       env("DATABASE_URL", ""),
		GRPCAddr:          env("GRPC_ADDR", ":50051"),
		MetricsAddr:       env("METRICS_ADDR", ":2112"),
		AdminAddr:         env("ADMIN_ADDR", ":8080"),
		WorkerConcurrency: envInt("WORKER_CONCURRENCY", 10),
		PollInterval:      envDuration("SCHEDULER_POLL_INTERVAL", 2*time.Second),
		BatchSize:         envInt("SCHEDULER_BATCH_SIZE", 10),
		JobTimeout:        envDuration("JOB_TIMEOUT", 30*time.Minute),
		RetryBaseDelay:    envDuration("RETRY_BASE_DELAY", time.Second),
		RetryMaxDelay:     envDuration("RETRY_MAX_DELAY", 5*time.Minute),
		RetryMultiplier:   envFloat("RETRY_MULTIPLIER", 2.0),
		RetryJitter:       envFloat("RETRY_JITTER", 0.2),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.WorkerConcurrency < 1 {
		return nil, fmt.Errorf("WORKER_CONCURRENCY must be >= 1")
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
