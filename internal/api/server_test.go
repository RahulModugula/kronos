package api_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	kronosv1 "github.com/rahulmodugula/kronos/gen/kronos/v1"
	"github.com/rahulmodugula/kronos/internal/api"
	"github.com/rahulmodugula/kronos/internal/store"
)

// mockStore implements store.Store with minimal stubs for API tests.
type mockStore struct {
	store.Store // satisfy the interface; unused methods panic
}

func (m *mockStore) CreateJob(_ context.Context, j *store.Job) (*store.Job, error) {
	j.ID = uuid.New()
	j.CreatedAt = time.Now()
	j.UpdatedAt = time.Now()
	return j, nil
}

func (m *mockStore) GetJob(_ context.Context, id uuid.UUID) (*store.Job, error) {
	return nil, fmt.Errorf("job %s not found", id)
}

func (m *mockStore) GetJobByIdempotencyKey(_ context.Context, _ string) (*store.Job, error) {
	return nil, nil
}

const bufSize = 1024 * 1024

func newTestClient(t *testing.T) kronosv1.KronosServiceClient {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	kronosv1.RegisterKronosServiceServer(srv, api.New(&mockStore{}, zerolog.Nop()))

	go func() {
		if err := srv.Serve(lis); err != nil {
			// server stopped
		}
	}()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return kronosv1.NewKronosServiceClient(conn)
}

func TestSubmitJob_ErrorWhenNameEmpty(t *testing.T) {
	client := newTestClient(t)

	_, err := client.SubmitJob(context.Background(), &kronosv1.SubmitJobRequest{
		Name: "",
		Type: "email",
	})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", st.Code())
	}
}

func TestSubmitJob_ErrorWhenTypeEmpty(t *testing.T) {
	client := newTestClient(t)

	_, err := client.SubmitJob(context.Background(), &kronosv1.SubmitJobRequest{
		Name: "test-job",
		Type: "",
	})
	if err == nil {
		t.Fatal("expected error for empty type")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", st.Code())
	}
}

func TestGetJob_NotFoundForUnknownUUID(t *testing.T) {
	client := newTestClient(t)

	_, err := client.GetJob(context.Background(), &kronosv1.GetJobRequest{
		JobId: uuid.New().String(),
	})
	if err == nil {
		t.Fatal("expected error for unknown job ID")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", st.Code())
	}
}
