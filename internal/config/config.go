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
	WorkerConcurrency int
	PollInterval      time.Duration
	BatchSize         int
}

// Load reads config from environment variables.
// Returns an error if required fields are missing.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:       env("DATABASE_URL", ""),
		GRPCAddr:          env("GRPC_ADDR", ":50051"),
		MetricsAddr:       env("METRICS_ADDR", ":2112"),
		WorkerConcurrency: envInt("WORKER_CONCURRENCY", 10),
		PollInterval:      envDuration("SCHEDULER_POLL_INTERVAL", 2*time.Second),
		BatchSize:         envInt("SCHEDULER_BATCH_SIZE", 10),
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
