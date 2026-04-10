package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	kronosv1 "github.com/rahulmodugula/kronos/gen/kronos/v1"
	"github.com/rahulmodugula/kronos/internal/store"
	"github.com/rahulmodugula/kronos/internal/workflow"
)

// Server implements kronosv1.KronosServiceServer.
type Server struct {
	kronosv1.UnimplementedKronosServiceServer
	store            store.Store
	workflowStore    workflow.WorkflowStore
	workflowRegistry *workflow.Registry
	workflowEngine   *workflow.Engine
	log              zerolog.Logger
}

func New(s store.Store, wfStore workflow.WorkflowStore, wfReg *workflow.Registry, wfEngine *workflow.Engine, log zerolog.Logger) *Server {
	return &Server{
		store:            s,
		workflowStore:    wfStore,
		workflowRegistry: wfReg,
		workflowEngine:   wfEngine,
		log:              log,
	}
}

func (srv *Server) SubmitJob(ctx context.Context, req *kronosv1.SubmitJobRequest) (*kronosv1.SubmitJobResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.Type == "" {
		return nil, status.Error(codes.InvalidArgument, "type is required")
	}
	maxRetries := int(req.MaxRetries)
	if maxRetries == 0 {
		maxRetries = 3
	}
	priority := int(req.Priority)
	if priority == 0 {
		priority = 5
	}

	// Deduplication: if an idempotency key is provided, return the existing
	// active job rather than inserting a duplicate.
	if req.IdempotencyKey != "" {
		existing, err := srv.store.GetJobByIdempotencyKey(ctx, req.IdempotencyKey)
		if err != nil {
			srv.log.Error().Err(err).Msg("GetJobByIdempotencyKey failed")
			return nil, status.Error(codes.Internal, "idempotency check failed")
		}
		if existing != nil {
			return &kronosv1.SubmitJobResponse{JobId: existing.ID.String()}, nil
		}
	}

	j := &store.Job{
		Name:           req.Name,
		Type:           req.Type,
		Payload:        req.Payload,
		MaxRetries:     maxRetries,
		Priority:       priority,
		IdempotencyKey: req.IdempotencyKey,
	}
	if req.ScheduledAt != nil {
		j.ScheduledAt = req.ScheduledAt.AsTime()
	}
	if len(j.Payload) == 0 {
		j.Payload = []byte("{}")
	}

	created, err := srv.store.CreateJob(ctx, j)
	if err != nil {
		srv.log.Error().Err(err).Msg("CreateJob failed")
		return nil, status.Error(codes.Internal, "failed to create job")
	}
	return &kronosv1.SubmitJobResponse{JobId: created.ID.String()}, nil
}

func (srv *Server) GetJob(ctx context.Context, req *kronosv1.GetJobRequest) (*kronosv1.GetJobResponse, error) {
	id, err := uuid.Parse(req.JobId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid job_id")
	}
	j, err := srv.store.GetJob(ctx, id)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &kronosv1.GetJobResponse{Job: toProto(j)}, nil
}

func (srv *Server) ListJobs(ctx context.Context, req *kronosv1.ListJobsRequest) (*kronosv1.ListJobsResponse, error) {
	f := store.ListFilter{
		PageSize:  int(req.PageSize),
		PageToken: req.PageToken,
	}
	if req.Status != kronosv1.JobStatus_JOB_STATUS_UNSPECIFIED {
		s := protoStatusToStore(req.Status)
		f.Status = &s
	}

	jobs, nextToken, err := srv.store.ListJobs(ctx, f)
	if err != nil {
		return nil, status.Error(codes.Internal, "list failed")
	}

	resp := &kronosv1.ListJobsResponse{NextPageToken: nextToken}
	for _, j := range jobs {
		resp.Jobs = append(resp.Jobs, toProto(j))
	}
	return resp, nil
}

func (srv *Server) CancelJob(ctx context.Context, req *kronosv1.CancelJobRequest) (*kronosv1.CancelJobResponse, error) {
	id, err := uuid.Parse(req.JobId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid job_id")
	}
	if err := srv.store.CancelJob(ctx, id); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &kronosv1.CancelJobResponse{}, nil
}

// toProto converts a store.Job to its protobuf representation.
func toProto(j *store.Job) *kronosv1.Job {
	return &kronosv1.Job{
		Id:          j.ID.String(),
		Name:        j.Name,
		Type:        j.Type,
		Payload:     j.Payload,
		Status:      storeStatusToProto(j.Status),
		MaxRetries:  int32(j.MaxRetries),
		RetryCount:  int32(j.RetryCount),
		Error:       j.Error,
		Priority:    int32(j.Priority),
		ScheduledAt: timestamppb.New(j.ScheduledAt),
		CreatedAt:   timestamppb.New(j.CreatedAt),
		UpdatedAt:   timestamppb.New(j.UpdatedAt),
	}
}

func storeStatusToProto(s store.Status) kronosv1.JobStatus {
	switch s {
	case store.StatusPending:
		return kronosv1.JobStatus_JOB_STATUS_PENDING
	case store.StatusRunning:
		return kronosv1.JobStatus_JOB_STATUS_RUNNING
	case store.StatusCompleted:
		return kronosv1.JobStatus_JOB_STATUS_COMPLETED
	case store.StatusFailed:
		return kronosv1.JobStatus_JOB_STATUS_FAILED
	case store.StatusCancelled:
		return kronosv1.JobStatus_JOB_STATUS_CANCELLED
	case store.StatusDead:
		return kronosv1.JobStatus_JOB_STATUS_DEAD
	default:
		return kronosv1.JobStatus_JOB_STATUS_UNSPECIFIED
	}
}

func protoStatusToStore(s kronosv1.JobStatus) store.Status {
	switch s {
	case kronosv1.JobStatus_JOB_STATUS_PENDING:
		return store.StatusPending
	case kronosv1.JobStatus_JOB_STATUS_RUNNING:
		return store.StatusRunning
	case kronosv1.JobStatus_JOB_STATUS_COMPLETED:
		return store.StatusCompleted
	case kronosv1.JobStatus_JOB_STATUS_FAILED:
		return store.StatusFailed
	case kronosv1.JobStatus_JOB_STATUS_CANCELLED:
		return store.StatusCancelled
	case kronosv1.JobStatus_JOB_STATUS_DEAD:
		return store.StatusDead
	case kronosv1.JobStatus_JOB_STATUS_UNSPECIFIED:
		return store.StatusPending
	}
	return store.StatusPending
}

// Ensure unused import is used — timestamppb uses time.
var _ = fmt.Sprintf
var _ = time.Now

// ==================== Workflow RPC Methods ====================

// StartWorkflow creates and starts a new workflow run.
func (srv *Server) StartWorkflow(ctx context.Context, req *kronosv1.StartWorkflowRequest) (*kronosv1.StartWorkflowResponse, error) {
	if req.WorkflowName == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow_name is required")
	}

	// Get the latest workflow definition
	wf := srv.workflowRegistry.Get(req.WorkflowName)
	if wf == nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("workflow %q not registered", req.WorkflowName))
	}

	// Create a workflow run
	runID := uuid.New().String()
	input := req.Input
	if input == nil {
		input = json.RawMessage(`{}`)
	}

	run := &workflow.WorkflowRun{
		ID:         runID,
		WorkflowID: wf.Name,
		Status:     "pending",
		Input:      input,
	}

	createdRun, err := srv.workflowStore.CreateRun(ctx, run)
	if err != nil {
		srv.log.Error().Err(err).Str("run_id", runID).Msg("CreateRun failed")
		return nil, status.Error(codes.Internal, "failed to create workflow run")
	}

	// Record RunStarted event
	event, err := workflow.NewEvent(createdRun.ID, 0, workflow.EventRunStarted, workflow.RunStartedPayload{
		WorkflowName:    wf.Name,
		WorkflowVersion: wf.Version,
		Input:           input,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to create event")
	}
	_, err = srv.workflowStore.AppendEvent(ctx, event)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to record event")
	}

	// Update run status to running
	now := time.Now()
	if err := srv.workflowStore.UpdateRunStatus(ctx, createdRun.ID, "running", nil, &now); err != nil {
		return nil, status.Error(codes.Internal, "failed to update run status")
	}

	srv.log.Info().Str("run_id", createdRun.ID).Str("workflow_name", wf.Name).Msg("workflow run started")
	return &kronosv1.StartWorkflowResponse{RunId: createdRun.ID}, nil
}

// GetRun retrieves a workflow run by ID.
func (srv *Server) GetRun(ctx context.Context, req *kronosv1.GetRunRequest) (*kronosv1.GetRunResponse, error) {
	if req.RunId == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}

	run, err := srv.workflowStore.GetRun(ctx, req.RunId)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if run == nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("run %s not found", req.RunId))
	}

	return &kronosv1.GetRunResponse{Run: toWorkflowRunProto(run)}, nil
}

// ListRuns lists workflow runs.
func (srv *Server) ListRuns(ctx context.Context, req *kronosv1.ListRunsRequest) (*kronosv1.ListRunsResponse, error) {
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	// Convert proto status to store status
	var statusFilter *string
	if req.Status != kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_UNSPECIFIED {
		s := protoWorkflowStatusToStore(req.Status)
		statusFilter = &s
	}

	workflowName := (*string)(nil)
	if req.WorkflowName != "" {
		workflowName = &req.WorkflowName
	}

	runs, nextToken, err := srv.workflowStore.ListRuns(ctx, workflowName, statusFilter, pageSize, req.PageToken)
	if err != nil {
		return nil, status.Error(codes.Internal, "list failed")
	}

	resp := &kronosv1.ListRunsResponse{NextPageToken: nextToken}
	for _, run := range runs {
		resp.Runs = append(resp.Runs, toWorkflowRunProto(run))
	}
	return resp, nil
}

// CancelRun cancels a pending workflow run.
func (srv *Server) CancelRun(ctx context.Context, req *kronosv1.CancelRunRequest) (*kronosv1.CancelRunResponse, error) {
	if req.RunId == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}

	if err := srv.workflowStore.CancelRun(ctx, req.RunId); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &kronosv1.CancelRunResponse{}, nil
}

// GetRunEvents retrieves all events for a workflow run.
func (srv *Server) GetRunEvents(ctx context.Context, req *kronosv1.GetRunEventsRequest) (*kronosv1.GetRunEventsResponse, error) {
	if req.RunId == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}

	events, err := srv.workflowStore.GetEvents(ctx, req.RunId)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to get events")
	}

	resp := &kronosv1.GetRunEventsResponse{}
	for _, event := range events {
		resp.Events = append(resp.Events, &kronosv1.WorkflowEvent{
			Id:        event.ID,
			RunId:     event.RunID,
			Seq:       int32(event.Seq),
			Ts:        timestamppb.New(event.Ts),
			EventType: string(event.EventType),
			Payload:   event.Payload,
		})
	}
	return resp, nil
}

// StreamRunHistory streams all events for a workflow run (for replay).
func (srv *Server) StreamRunHistory(req *kronosv1.StreamRunHistoryRequest, stream grpc.ServerStreamingServer[kronosv1.WorkflowEvent]) error {
	if req.RunId == "" {
		return status.Error(codes.InvalidArgument, "run_id is required")
	}

	events, err := srv.workflowStore.GetEvents(stream.Context(), req.RunId)
	if err != nil {
		return status.Error(codes.Internal, "failed to get events")
	}

	for _, event := range events {
		evt := kronosv1.WorkflowEvent{
			Id:        event.ID,
			RunId:     event.RunID,
			Seq:       int32(event.Seq),
			Ts:        timestamppb.New(event.Ts),
			EventType: string(event.EventType),
			Payload:   event.Payload,
		}
		if err := stream.Send(&evt); err != nil {
			return err
		}
	}
	return nil
}

// toWorkflowRunProto converts a store.WorkflowRun to its protobuf representation.
func toWorkflowRunProto(run *workflow.WorkflowRun) *kronosv1.WorkflowRun {
	parentRunID := ""
	if run.ParentRunID != nil {
		parentRunID = *run.ParentRunID
	}

	proto := &kronosv1.WorkflowRun{
		Id:          run.ID,
		WorkflowId:  run.WorkflowID,
		Status:      storeWorkflowStatusToProto(run.Status),
		Input:       run.Input,
		Output:      run.Output,
		Error:       run.Error,
		ParentRunId: parentRunID,
		CreatedAt:   timestamppb.New(run.CreatedAt),
		UpdatedAt:   timestamppb.New(run.UpdatedAt),
	}
	if run.StartedAt != nil {
		proto.StartedAt = timestamppb.New(*run.StartedAt)
	}
	if run.FinishedAt != nil {
		proto.FinishedAt = timestamppb.New(*run.FinishedAt)
	}
	return proto
}

func storeWorkflowStatusToProto(s string) kronosv1.WorkflowRunStatus {
	switch s {
	case "pending":
		return kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PENDING
	case "running":
		return kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING
	case "completed":
		return kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED
	case "failed":
		return kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_FAILED
	case "cancelled":
		return kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_CANCELLED
	case "forked":
		return kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_FORKED
	default:
		return kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_UNSPECIFIED
	}
}

func protoWorkflowStatusToStore(s kronosv1.WorkflowRunStatus) string {
	switch s {
	case kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PENDING:
		return "pending"
	case kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING:
		return "running"
	case kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED:
		return "completed"
	case kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_FAILED:
		return "failed"
	case kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_CANCELLED:
		return "cancelled"
	case kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_FORKED:
		return "forked"
	case kronosv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_UNSPECIFIED:
		return "pending"
	}
	return "pending"
}

func (srv *Server) ForkRun(ctx context.Context, req *kronosv1.ForkRunRequest) (*kronosv1.ForkRunResponse, error) {
	if req.GetRunId() == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	if req.GetFromStep() == "" {
		return nil, status.Error(codes.InvalidArgument, "from_step is required")
	}

	forkedID, err := srv.workflowEngine.ForkFromStep(ctx, req.GetRunId(), req.GetFromStep())
	if err != nil {
		srv.log.Error().Err(err).Str("run_id", req.GetRunId()).Str("from_step", req.GetFromStep()).Msg("fork failed")
		return nil, status.Errorf(codes.Internal, "fork failed: %v", err)
	}

	return &kronosv1.ForkRunResponse{ForkedRunId: forkedID}, nil
}
