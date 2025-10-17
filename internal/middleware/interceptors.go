package middleware

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// contextKey is an unexported type for context keys in this package.
// Prevents collision with keys from other packages.
type contextKey int

const requestIDKey contextKey = iota

// RequestIDFromContext extracts the request ID from a context.
// Returns empty string if not present.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// UnaryRequestID injects a request ID into the context. It first checks
// incoming gRPC metadata for an "x-request-id" header (so callers can
// propagate their own IDs); otherwise it mints a new UUID v4.
func UnaryRequestID() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get("x-request-id"); len(vals) > 0 {
				id = vals[0]
			}
		}
		if id == "" {
			id = uuid.New().String()
		}
		ctx = context.WithValue(ctx, requestIDKey, id)
		// Echo the request ID back so clients can correlate logs.
		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", id))
		return handler(ctx, req)
	}
}

// UnaryLogger logs every RPC with method, request ID, duration, and status code.
// Uses zerolog structured fields — no string formatting on the hot path.
func UnaryLogger(log zerolog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)
		log.Info().
			Str("method", info.FullMethod).
			Str("request_id", RequestIDFromContext(ctx)).
			Dur("duration_ms", time.Since(start)).
			Str("code", code.String()).
			Err(err).
			Msg("grpc request")
		return resp, err
	}
}

// UnaryRecovery catches any panic in a handler and converts it to a gRPC
// Internal error, preventing the server from crashing. The full stack trace
// is logged so panics are never silently swallowed.
func UnaryRecovery(log zerolog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error().
					Str("method", info.FullMethod).
					Str("request_id", RequestIDFromContext(ctx)).
					Interface("panic", r).
					Bytes("stack", debug.Stack()).
					Msg("panic recovered in grpc handler")
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}
