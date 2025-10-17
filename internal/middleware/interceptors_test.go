package middleware_test

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"

	"github.com/rahulmodugula/kronos/internal/middleware"
)

func TestRequestIDPropagated(t *testing.T) {
	interceptor := middleware.UnaryRequestID()

	var capturedID string
	handler := func(ctx context.Context, req any) (any, error) {
		capturedID = middleware.RequestIDFromContext(ctx)
		return nil, nil
	}

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test"}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if capturedID == "" {
		t.Error("expected request ID to be set in context")
	}
}

func TestRecoveryConvertsParicToError(t *testing.T) {
	log := zerolog.Nop()
	interceptor := middleware.UnaryRecovery(log)

	panicHandler := func(ctx context.Context, req any) (any, error) {
		panic("something went wrong")
	}

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test"}, panicHandler)
	if err == nil {
		t.Error("expected error after panic, got nil")
	}
}
