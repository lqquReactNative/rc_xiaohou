package notification_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
	"github.com/lqquReactNative/rc_xiaohou/internal/vendor"
)

func newTestHandler(t *testing.T) (http.Handler, vendor.Store) {
	t.Helper()
	f, _ := os.CreateTemp(t.TempDir(), "vendors-*.json")
	f.Close()
	os.Remove(f.Name())
	store, err := vendor.NewJSONStore(f.Name())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	h := notification.NewHandler(store, notification.NewSyncEnqueuer())
	return h.Routes(), store
}

func postJSON(handler http.Handler, path string, body interface{}) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// AC3: request for unknown vendor_id → 422 at intake.
func TestSubmit_UnknownVendor(t *testing.T) {
	handler, _ := newTestHandler(t)
	rec := postJSON(handler, "/", map[string]interface{}{
		"vendor_id": "does-not-exist",
		"payload":   map[string]interface{}{"user_id": "u-1"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["error"] == "" {
		t.Error("expected error message in response body")
	}
}

// AC1 + AC2: known vendor → 202 Accepted, template rendered with payload.
func TestSubmit_KnownVendor(t *testing.T) {
	handler, store := newTestHandler(t)
	profile, err := store.Create(vendor.CreateRequest{
		Name:         "Test Vendor",
		TargetURL:    "https://vendor.example.com/notify",
		Method:       "POST",
		AuthHeaders:  map[string]string{"Authorization": "Bearer tok"},
		BodyTemplate: `{"uid":"{{user_id}}"}`,
	})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}

	rec := postJSON(handler, "/", map[string]interface{}{
		"vendor_id": profile.ID,
		"payload":   map[string]interface{}{"user_id": "u-99"},
	})
	if rec.Code != http.StatusAccepted {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp notification.Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == "" {
		t.Error("expected non-empty notification ID")
	}
	if resp.Status != "queued" {
		t.Errorf("status: got %q, want %q", resp.Status, "queued")
	}
}

// AC3 variant: missing vendor_id field → 400.
func TestSubmit_MissingVendorID(t *testing.T) {
	handler, _ := newTestHandler(t)
	rec := postJSON(handler, "/", map[string]interface{}{
		"payload": map[string]interface{}{"user_id": "u-1"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// AC2 edge: template variable missing from payload → 400.
func TestSubmit_TemplateMissingVariable(t *testing.T) {
	handler, store := newTestHandler(t)
	profile, _ := store.Create(vendor.CreateRequest{
		Name:         "Template Vendor",
		TargetURL:    "https://v.example.com",
		Method:       "POST",
		BodyTemplate: `{"uid":"{{user_id}}","email":"{{email}}"}`,
	})

	rec := postJSON(handler, "/", map[string]interface{}{
		"vendor_id": profile.ID,
		"payload":   map[string]interface{}{"user_id": "u-1"},
		// "email" is missing
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
