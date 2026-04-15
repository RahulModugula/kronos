package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	kronosv1 "github.com/rahulmodugula/kronos/gen/kronos/v1"
	"github.com/rahulmodugula/kronos/internal/workflow"
)

type Client struct {
	conn   *grpc.ClientConn
	client kronosv1.KronosServiceClient
}

func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Client{
		conn:   conn,
		client: kronosv1.NewKronosServiceClient(conn),
	}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) FetchRun(ctx context.Context, runID string) (*workflow.WorkflowRun, error) {
	resp, err := c.client.GetRun(ctx, &kronosv1.GetRunRequest{RunId: runID})
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	if resp.Run == nil {
		return nil, fmt.Errorf("run %s not found", runID)
	}

	pbRun := resp.Run
	run := &workflow.WorkflowRun{
		ID:         pbRun.Id,
		WorkflowID: pbRun.WorkflowId,
		Status:     workflowStatusFromProto(pbRun.Status),
		Input:      pbRun.Input,
		Output:     pbRun.Output,
		Error:      pbRun.Error,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if pbRun.ParentRunId != "" {
		parentID := pbRun.ParentRunId
		run.ParentRunID = &parentID
	}
	return run, nil
}

func (c *Client) FetchEvents(ctx context.Context, runID string) ([]*workflow.Event, error) {
	resp, err := c.client.GetRunEvents(ctx, &kronosv1.GetRunEventsRequest{RunId: runID})
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}

	events := make([]*workflow.Event, 0, len(resp.Events))
	for _, evt := range resp.Events {
		events = append(events, &workflow.Event{
			ID:        evt.Id,
			RunID:     evt.RunId,
			Seq:       int(evt.Seq),
			Ts:        evt.Ts.AsTime(),
			EventType: workflow.EventType(evt.EventType),
			Payload:   evt.Payload,
		})
	}
	return events, nil
}

func (c *Client) StreamEvents(ctx context.Context, runID string) ([]*workflow.Event, error) {
	stream, err := c.client.StreamRunHistory(ctx, &kronosv1.StreamRunHistoryRequest{RunId: runID})
	if err != nil {
		return nil, fmt.Errorf("stream history: %w", err)
	}

	var events []*workflow.Event
	for {
		evt, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("recv: %w", err)
		}
		events = append(events, &workflow.Event{
			ID:        evt.Id,
			RunID:     evt.RunId,
			Seq:       int(evt.Seq),
			Ts:        evt.Ts.AsTime(),
			EventType: workflow.EventType(evt.EventType),
			Payload:   evt.Payload,
		})
	}
	return events, nil
}

func (c *Client) ForkRun(ctx context.Context, runID, fromStep string) (string, error) {
	resp, err := c.client.ForkRun(ctx, &kronosv1.ForkRunRequest{
		RunId:    runID,
		FromStep: fromStep,
	})
	if err != nil {
		return "", fmt.Errorf("fork: %w", err)
	}
	return resp.ForkedRunId, nil
}

func workflowStatusFromProto(s kronosv1.WorkflowRunStatus) string {
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
	default:
		return "pending"
	}
}

func RunReplay(args []string, registry *workflow.Registry, log zerolog.Logger) error {
	cfg, err := parseArgs(args)
	if err != nil {
		return err
	}

	ctx := context.Background()

	log.Info().Str("server", cfg.Server).Msg("connecting")
	client, err := NewClient(cfg.Server)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer client.Close()

	log.Info().Str("run_id", cfg.RunID).Msg("fetching run")
	run, err := client.FetchRun(ctx, cfg.RunID)
	if err != nil {
		return err
	}

	events, err := client.FetchEvents(ctx, cfg.RunID)
	if err != nil {
		return err
	}
	log.Info().Int("events", len(events)).Msg("fetched events")

	replayer := NewReplayer(registry, run, events, log)
	return replayer.Replay(ctx, cfg.BreakAt, cfg.ForkFrom)
}

type Config struct {
	RunID    string
	BreakAt  string
	ForkFrom string
	Server   string
}

func parseArgs(args []string) (*Config, error) {
	cfg := &Config{
		Server: "localhost:50051",
	}
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--break-at":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--break-at requires a step name")
			}
			cfg.BreakAt = args[i]
		case "--fork-from":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--fork-from requires a step name")
			}
			cfg.ForkFrom = args[i]
		case "--server":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--server requires an address")
			}
			cfg.Server = args[i]
		default:
			if cfg.RunID == "" {
				cfg.RunID = args[i]
			} else {
				return nil, fmt.Errorf("unexpected argument: %s", args[i])
			}
		}
		i++
	}

	if cfg.RunID == "" {
		return nil, fmt.Errorf("usage: kronos replay <run-id> [--break-at step] [--fork-from step] [--server addr]")
	}
	return cfg, nil
}

func prettyJSON(data json.RawMessage) string {
	if data == nil {
		return ""
	}
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out)
}
