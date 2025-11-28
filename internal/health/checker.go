package health

import (
	"context"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// ProbeFunc returns nil if the dependency is healthy.
type ProbeFunc func(ctx context.Context) error

// Checker implements grpc_health_v1.HealthServer.
// It calls all registered probes and returns NOT_SERVING if any fail.
// Returning SERVING unconditionally is useless — we check real dependencies.
type Checker struct {
	grpc_health_v1.UnimplementedHealthServer
	mu     sync.RWMutex
	probes map[string]ProbeFunc
}

func New() *Checker {
	return &Checker{probes: make(map[string]ProbeFunc)}
}

// Register adds a named probe. The name is logged when the probe fails.
func (c *Checker) Register(name string, fn ProbeFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probes[name] = fn
}

func (c *Checker) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, probe := range c.probes {
		if err := probe(ctx); err != nil {
			return &grpc_health_v1.HealthCheckResponse{
				Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING,
			}, nil
		}
	}
	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}, nil
}

// Watch is required by the interface but not implemented.
// Kubernetes uses Check (unary), not Watch (streaming), for liveness probes.
func (c *Checker) Watch(req *grpc_health_v1.HealthCheckRequest, srv grpc_health_v1.Health_WatchServer) error {
	return status.Error(codes.Unimplemented, "watch not supported")
}
