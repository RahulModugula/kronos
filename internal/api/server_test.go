package api_test

import (
	"context"
	"encoding/json"
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
	"github.com/rahulmodugula/kronos/internal/workflow"
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

// mockWorkflowStore implements workflow.WorkflowStore with minimal stubs for API tests.
type mockWorkflowStore struct{}

func (m *mockWorkflowStore) CreateRun(_ context.Context, run *workflow.WorkflowRun) (*workflow.WorkflowRun, error) {
	run.ID = uuid.New().String()
	run.CreatedAt = time.Now()
	run.UpdatedAt = time.Now()
	return run, nil
}

func (m *mockWorkflowStore) GetRun(_ context.Context, id string) (*workflow.WorkflowRun, error) {
	return nil, fmt.Errorf("run %s not found", id)
}

func (m *mockWorkflowStore) ListRuns(_ context.Context, _ *string, _ *string, _ int, _ string) ([]*workflow.WorkflowRun, string, error) {
	return nil, "", nil
}

func (m *mockWorkflowStore) UpdateRunStatus(_ context.Context, _ string, _ string, _ *string, _ *time.Time) error {
	return nil
}

func (m *mockWorkflowStore) CancelRun(_ context.Context, _ string) error {
	return nil
}

func (m *mockWorkflowStore) AppendEvent(_ context.Context, _ *workflow.Event) (int, error) {
	return 0, nil
}

func (m *mockWorkflowStore) GetEvents(_ context.Context, _ string) ([]*workflow.Event, error) {
	return nil, nil
}

func (m *mockWorkflowStore) GetEventsAfterSeq(_ context.Context, _ string, _ int) ([]*workflow.Event, error) {
	return nil, nil
}

func (m *mockWorkflowStore) CreateWorkflow(_ context.Context, _ *workflow.Workflow) error {
	return nil
}

func (m *mockWorkflowStore) GetWorkflow(_ context.Context, _ string) (*workflow.Workflow, error) {
	return nil, nil
}

func (m *mockWorkflowStore) GetWorkflowByNameFingerprint(_ context.Context, _ string, _ string) (*workflow.Workflow, error) {
	return nil, nil
}

func (m *mockWorkflowStore) GetLatestWorkflow(_ context.Context, _ string) (*workflow.Workflow, error) {
	return nil, nil
}

func (m *mockWorkflowStore) UpdateRunOutput(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}

func (m *mockWorkflowStore) PutBlob(_ context.Context, _ string, _ []byte) error {
	return nil
}

func (m *mockWorkflowStore) GetBlob(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (m *mockWorkflowStore) DeleteBlob(_ context.Context, _ string) error {
	return nil
}

func (m *mockWorkflowStore) UpsertStepExecution(_ context.Context, _ *workflow.StepExecution) error {
	return nil
}

func (m *mockWorkflowStore) GetStepExecution(_ context.Context, _ string, _ string) (*workflow.StepExecution, error) {
	return nil, nil
}

func (m *mockWorkflowStore) ListStepExecutions(_ context.Context, _ string) ([]*workflow.StepExecution, error) {
	return nil, nil
}

const bufSize = 1024 * 1024

func newTestClient(t *testing.T) kronosv1.KronosServiceClient {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()

	// Create mock workflow dependencies
	wfStore := &mockWorkflowStore{}
	wfRegistry := workflow.NewRegistry()
	wfEngine := workflow.NewEngine(wfStore, wfRegistry, zerolog.Nop())

	kronosv1.RegisterKronosServiceServer(srv, api.New(&mockStore{}, wfStore, wfRegistry, wfEngine, zerolog.Nop()))

	go func() {
		_ = srv.Serve(lis) // server will stop gracefully
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
