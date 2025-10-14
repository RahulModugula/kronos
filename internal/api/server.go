package api

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	kronosv1 "github.com/rahulmodugula/kronos/gen/kronos/v1"
	"github.com/rahulmodugula/kronos/internal/store"
)

// Server implements kronosv1.KronosServiceServer.
type Server struct {
	kronosv1.UnimplementedKronosServiceServer
	store store.Store
	log   zerolog.Logger
}

func New(s store.Store, log zerolog.Logger) *Server {
	return &Server{store: s, log: log}
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

	j := &store.Job{
		Name:       req.Name,
		Type:       req.Type,
		Payload:    req.Payload,
		MaxRetries: maxRetries,
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
	default:
		return store.StatusPending
	}
}

// ensure unused import is used — timestamppb uses time
var _ = fmt.Sprintf
var _ = time.Now
