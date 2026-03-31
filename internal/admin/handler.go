package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/store"
)

// Handler exposes admin endpoints for dead-letter queue management.
//
// Routes:
//
//	GET  /admin/jobs/dead          — list dead jobs (paginated)
//	POST /admin/jobs/{id}/retry    — transition a dead job back to pending
type Handler struct {
	store store.Store
	log   zerolog.Logger
}

func NewHandler(s store.Store, log zerolog.Logger) *Handler {
	return &Handler{store: s, log: log}
}

// Register mounts the admin routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/admin/jobs/dead", h.listDeadJobs)
	mux.HandleFunc("/admin/jobs/", h.retryDeadJob)
}

func (h *Handler) listDeadJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pageSize := 50
	if ps := r.URL.Query().Get("page_size"); ps != "" {
		if n, err := strconv.Atoi(ps); err == nil && n > 0 {
			pageSize = n
		}
	}
	pageToken := r.URL.Query().Get("page_token")

	jobs, nextToken, err := h.store.ListDeadJobs(r.Context(), pageSize, pageToken)
	if err != nil {
		h.log.Error().Err(err).Msg("ListDeadJobs failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type jobSummary struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		Error       string `json:"error,omitempty"`
		RetryCount  int    `json:"retry_count"`
		MaxRetries  int    `json:"max_retries"`
		ScheduledAt string `json:"scheduled_at"`
		CreatedAt   string `json:"created_at"`
	}
	summaries := make([]jobSummary, 0, len(jobs))
	for _, j := range jobs {
		summaries = append(summaries, jobSummary{
			ID:          j.ID.String(),
			Name:        j.Name,
			Type:        j.Type,
			Error:       j.Error,
			RetryCount:  j.RetryCount,
			MaxRetries:  j.MaxRetries,
			ScheduledAt: j.ScheduledAt.String(),
			CreatedAt:   j.CreatedAt.String(),
		})
	}

	resp := struct {
		Jobs          []jobSummary `json:"jobs"`
		NextPageToken string       `json:"next_page_token,omitempty"`
	}{
		Jobs:          summaries,
		NextPageToken: nextToken,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (h *Handler) retryDeadJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /admin/jobs/{id}/retry
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/admin/jobs/"), "/")
	if len(parts) != 2 || parts[1] != "retry" {
		http.NotFound(w, r)
		return
	}

	id, err := uuid.Parse(parts[0])
	if err != nil {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}

	if err := h.store.RetryDeadJob(r.Context(), id); err != nil {
		h.log.Error().Err(err).Str("job_id", id.String()).Msg("RetryDeadJob failed")
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
}
