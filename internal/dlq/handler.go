// Package dlq provides HTTP endpoints for inspecting and managing dead-letter
// queue entries — notifications that have exhausted all retry attempts.
package dlq

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
)

// Handler serves the DLQ inspection and resubmission endpoints.
type Handler struct {
	store notification.DLQStore
}

// NewHandler creates a Handler backed by the given DLQStore.
func NewHandler(store notification.DLQStore) *Handler { return &Handler{store: store} }

// Routes registers:
//
//	GET  /    — list all DLQ entries with original payload and full attempt history
//	POST /{id}/resubmit — reset a DLQ entry to pending for manual re-delivery
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Post("/{id}/resubmit", h.resubmit)
	return r
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	entries, err := h.store.ListDead()
	if err != nil {
		slog.Error("list DLQ", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// Always return a JSON array, never null.
	if entries == nil {
		entries = []*notification.DLQRecord{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) resubmit(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	found, err := h.store.ResubmitDead(id)
	if err != nil {
		slog.Error("resubmit DLQ entry", "id", id, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "dead-letter entry not found"})
		return
	}
	slog.Info("dlq entry requeued for delivery", "id", id)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id, "status": "requeued"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
