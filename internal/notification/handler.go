package notification

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lqquReactNative/rc_xiaohou/internal/vendor"
)

// Enqueuer is responsible for accepting a validated notification for delivery.
// In v1 this is synchronous; future stories replace it with a durable queue.
type Enqueuer interface {
	Enqueue(vendorID string, renderedBody string, headers map[string]string, targetURL, method string) (string, error)
}

type Handler struct {
	vendors  vendor.Store
	enqueuer Enqueuer
}

func NewHandler(vendors vendor.Store, enqueuer Enqueuer) *Handler {
	return &Handler{vendors: vendors, enqueuer: enqueuer}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.submit)
	return r
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.VendorID == "" {
		writeError(w, http.StatusBadRequest, "vendor_id is required")
		return
	}

	// AC3: fail at intake if vendor is unknown
	profile, err := h.vendors.Get(req.VendorID)
	if errors.Is(err, vendor.ErrNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "unknown vendor_id: no profile configured")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// AC2: render body template with event payload
	body, err := vendor.RenderTemplate(profile.BodyTemplate, req.Payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "template rendering failed: "+err.Error())
		return
	}

	id, err := h.enqueuer.Enqueue(profile.ID, body, profile.AuthHeaders, profile.TargetURL, profile.Method)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue notification: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, Response{
		ID:       id,
		VendorID: profile.ID,
		Status:   "queued",
	})
}

// SyncEnqueuer is a simple synchronous enqueuer used in v1 before the durable queue story lands.
type SyncEnqueuer struct{}

func NewSyncEnqueuer() *SyncEnqueuer { return &SyncEnqueuer{} }

func (e *SyncEnqueuer) Enqueue(vendorID, renderedBody string, headers map[string]string, targetURL, method string) (string, error) {
	// In v1, we accept and immediately "queue" by returning an ID.
	// WOR-13 will replace this with durable persistence + async delivery.
	return uuid.New().String(), nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
