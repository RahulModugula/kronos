package debugger

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/workflow"
)

type Handler struct {
	store     workflow.WorkflowStore
	templates *template.Template
	staticFS  http.Handler
	log       zerolog.Logger
}

func NewHandler(store workflow.WorkflowStore, log zerolog.Logger) *Handler {
	staticFS, _ := fs.Sub(Assets, "static")

	tmpl := template.New("").Delims("[[", "]]").Funcs(template.FuncMap{
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n]
		},
	})

	template.Must(tmpl.ParseFS(Assets,
		"templates/layout.html",
		"templates/runs.html",
		"templates/run_detail.html",
		"templates/partials/step_tree.html",
		"templates/partials/timeline.html",
		"templates/partials/step_detail.html",
	))

	return &Handler{
		store:     store,
		templates: tmpl,
		staticFS:  http.FileServer(http.FS(staticFS)),
		log:       log,
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/debugger/", h.handleRoot)
	mux.HandleFunc("/debugger/runs", h.listRuns)
	mux.HandleFunc("/debugger/runs/", h.runRoutes)
	mux.HandleFunc("/debugger/static/", h.serveStatic)
}

func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/debugger/" || r.URL.Path == "/debugger" {
		h.listRuns(w, r)
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	h.staticFS.ServeHTTP(w, r)
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workflowName := r.URL.Query().Get("workflow")
	status := r.URL.Query().Get("status")

	var statusPtr *string
	if status != "" {
		statusPtr = &status
	}
	var wfNamePtr *string
	if workflowName != "" {
		wfNamePtr = &workflowName
	}

	if r.Header.Get("Accept") == "application/json" {
		h.listRunsJSON(w, r, wfNamePtr, statusPtr)
		return
	}

	runs, _, _ := h.store.ListRuns(ctx, wfNamePtr, statusPtr, 50, "")

	type runView struct {
		ID           string
		WorkflowName string
		Status       string
		Duration     string
		TimeAgo      string
	}

	var views []runView
	for _, run := range runs {
		view := runView{
			ID:           run.ID,
			WorkflowName: extractWorkflowName(run.WorkflowID),
			Status:       run.Status,
			TimeAgo:      timeAgo(run.CreatedAt),
		}
		if run.StartedAt != nil && run.FinishedAt != nil {
			d := run.FinishedAt.Sub(*run.StartedAt)
			view.Duration = fmt.Sprintf("%dms", d.Milliseconds())
		}
		views = append(views, view)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.ExecuteTemplate(w, "runs.html", map[string]interface{}{
		"ActivePage":    "runs",
		"Runs":          views,
		"WorkflowNames": h.getWorkflowNames(ctx),
	})
}

func (h *Handler) listRunsJSON(w http.ResponseWriter, r *http.Request, wfName, status *string) {
	ctx := r.Context()
	runs, _, _ := h.store.ListRuns(ctx, wfName, status, 50, "")

	type jsonRun struct {
		ID           string `json:"id"`
		WorkflowName string `json:"workflow_name"`
		Status       string `json:"status"`
		TimeAgo      string `json:"time_ago"`
	}

	result := make([]jsonRun, 0, len(runs))
	for _, run := range runs {
		result = append(result, jsonRun{
			ID:           run.ID,
			WorkflowName: extractWorkflowName(run.WorkflowID),
			Status:       run.Status,
			TimeAgo:      timeAgo(run.CreatedAt),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) runRoutes(w http.ResponseWriter, r *http.Request) {
	path := cutPath(r.URL.Path, "/debugger/runs/")
	if path == "" {
		h.listRuns(w, r)
		return
	}

	parts := splitPath(path, 2)
	runID := parts[0]

	if len(parts) == 1 {
		h.getRunDetail(w, r, runID)
		return
	}

	switch parts[1] {
	case "events":
		h.getRunEventsJSON(w, r, runID)
	case "live":
		h.streamLive(w, r, runID)
	case "fork":
		if r.Method == http.MethodPost {
			h.forkRun(w, r, runID)
		}
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) getRunDetail(w http.ResponseWriter, r *http.Request, runID string) {
	ctx := r.Context()

	run, err := h.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		http.Error(w, "Run not found", http.StatusNotFound)
		return
	}

	events, _ := h.store.GetEvents(ctx, runID)

	type stepView struct {
		Name       string
		Status     string
		DurationMs *int
	}

	stepMap := make(map[string]*stepView)
	for _, evt := range events {
		var payload map[string]interface{}
		json.Unmarshal(evt.Payload, &payload)
		stepName, _ := payload["step_name"].(string)
		if stepName == "" {
			continue
		}
		if stepMap[stepName] == nil {
			stepMap[stepName] = &stepView{Name: stepName, Status: "pending"}
		}
		switch string(evt.EventType) {
		case "step_started":
			stepMap[stepName].Status = "running"
		case "step_completed":
			stepMap[stepName].Status = "completed"
		case "step_failed":
			stepMap[stepName].Status = "failed"
		case "step_output_recorded":
			if dur, ok := payload["duration_ms"]; ok {
				if d, ok := dur.(float64); ok {
					ms := int(d)
					stepMap[stepName].DurationMs = &ms
				}
			}
		}
	}

	steps := make([]stepView, 0, len(stepMap))
	for _, s := range stepMap {
		steps = append(steps, *s)
	}

	timeAgoStr := timeAgo(run.CreatedAt)
	var durationMs *int
	if run.StartedAt != nil && run.FinishedAt != nil {
		d := int(run.FinishedAt.Sub(*run.StartedAt).Milliseconds())
		durationMs = &d
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.ExecuteTemplate(w, "run_detail.html", map[string]interface{}{
		"ActivePage": "runs",
		"Run": map[string]interface{}{
			"ID":           run.ID,
			"WorkflowName": extractWorkflowName(run.WorkflowID),
			"Status":       run.Status,
			"TimeAgo":      timeAgoStr,
			"DurationMs":   durationMs,
		},
		"Steps": steps,
	})
}

func (h *Handler) getRunEventsJSON(w http.ResponseWriter, r *http.Request, runID string) {
	ctx := r.Context()
	events, err := h.store.GetEvents(ctx, runID)
	if err != nil {
		http.Error(w, "Failed to get events", http.StatusInternalServerError)
		return
	}

	type jsonEvent struct {
		Seq       int             `json:"seq"`
		Ts        string          `json:"ts"`
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
	}

	result := make([]jsonEvent, 0, len(events))
	for _, evt := range events {
		result = append(result, jsonEvent{
			Seq:       evt.Seq,
			Ts:        evt.Ts.Format(time.RFC3339Nano),
			EventType: string(evt.EventType),
			Payload:   evt.Payload,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) streamLive(w http.ResponseWriter, r *http.Request, runID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ctx := r.Context()
	lastSeq := 0

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			events, err := h.store.GetEventsAfterSeq(ctx, runID, lastSeq)
			if err != nil {
				continue
			}
			for _, evt := range events {
				type jsonEvent struct {
					Seq       int             `json:"seq"`
					Ts        string          `json:"ts"`
					EventType string          `json:"event_type"`
					Payload   json.RawMessage `json:"payload"`
				}
				data, _ := json.Marshal(jsonEvent{
					Seq:       evt.Seq,
					Ts:        evt.Ts.Format(time.RFC3339Nano),
					EventType: string(evt.EventType),
					Payload:   evt.Payload,
				})
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				lastSeq = evt.Seq
			}

			run, _ := h.store.GetRun(ctx, runID)
			if run != nil && run.Status != "running" {
				return
			}
		}
	}
}

func (h *Handler) forkRun(w http.ResponseWriter, r *http.Request, runID string) {
	ctx := r.Context()

	var req struct {
		FromStep string `json:"from_step"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if req.FromStep == "" {
		http.Error(w, "from_step is required", http.StatusBadRequest)
		return
	}

	run, err := h.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		http.Error(w, "Run not found", http.StatusNotFound)
		return
	}

	wfID := run.WorkflowID
	wfName := extractWorkflowName(wfID)

	now := time.Now().UTC()
	forkedRun := &workflow.WorkflowRun{
		WorkflowID:  wfID,
		Status:      "running",
		Input:       run.Input,
		ParentRunID: &runID,
	}
	created, err := h.store.CreateRun(ctx, forkedRun)
	if err != nil {
		http.Error(w, "Failed to create forked run", http.StatusInternalServerError)
		return
	}

	runStarted, _ := workflow.NewEvent(created.ID, 0, workflow.EventRunStarted, workflow.RunStartedPayload{
		WorkflowName:    wfName,
		WorkflowVersion: "v1",
		Input:           run.Input,
	})
	h.store.AppendEvent(ctx, runStarted)

	forkEvt, _ := workflow.NewEvent(created.ID, 1, workflow.EventRunForked, workflow.RunForkedPayload{
		ForkedRunID:  created.ID,
		FromStepName: req.FromStep,
	})
	h.store.AppendEvent(ctx, forkEvt)

	h.store.UpdateRunStatus(ctx, created.ID, "running", nil, &now)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"forked_run_id": created.ID,
	})
}

func (h *Handler) getWorkflowNames(ctx context.Context) []string {
	runs, _, _ := h.store.ListRuns(ctx, nil, nil, 1000, "")
	names := make(map[string]bool)
	for _, run := range runs {
		names[extractWorkflowName(run.WorkflowID)] = true
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	return result
}

func extractWorkflowName(workflowID string) string {
	return workflowID
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func cutPath(path, prefix string) string {
	if len(path) <= len(prefix) {
		return ""
	}
	return path[len(prefix):]
}

func splitPath(path string, n int) []string {
	parts := make([]string, 0, n)
	for len(path) > 0 && len(parts) < n {
		if path[0] == '/' {
			path = path[1:]
			continue
		}
		idx := -1
		for i := range path {
			if path[i] == '/' {
				idx = i
				break
			}
		}
		if idx == -1 {
			parts = append(parts, path)
			break
		}
		parts = append(parts, path[:idx])
		path = path[idx:]
	}
	return parts
}
