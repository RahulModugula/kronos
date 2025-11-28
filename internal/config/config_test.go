package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/rahulmodugula/kronos/internal/config"
)

func TestLoad_MissingDatabaseURL(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error when DATABASE_URL is missing")
	}
}

func TestLoad_Defaults(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://x:x@localhost/x")
	t.Cleanup(func() { os.Unsetenv("DATABASE_URL") })

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GRPCAddr != ":50051" {
		t.Errorf("expected :50051, got %s", cfg.GRPCAddr)
	}
	if cfg.WorkerConcurrency != 10 {
		t.Errorf("expected 10, got %d", cfg.WorkerConcurrency)
	}
	if cfg.PollInterval != 2*time.Second {
		t.Errorf("expected 2s, got %v", cfg.PollInterval)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://x:x@localhost/x")
	os.Setenv("GRPC_ADDR", ":9090")
	os.Setenv("WORKER_CONCURRENCY", "20")
	t.Cleanup(func() {
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("GRPC_ADDR")
		os.Unsetenv("WORKER_CONCURRENCY")
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GRPCAddr != ":9090" {
		t.Errorf("expected :9090, got %s", cfg.GRPCAddr)
	}
	if cfg.WorkerConcurrency != 20 {
		t.Errorf("expected 20, got %d", cfg.WorkerConcurrency)
	}
}
