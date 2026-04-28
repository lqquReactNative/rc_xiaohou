package vendor_test

import (
	"errors"
	"os"
	"testing"

	"github.com/lqquReactNative/rc_xiaohou/internal/vendor"
)

func newTempStore(t *testing.T) vendor.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "vendors-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()
	os.Remove(f.Name()) // let the store create it fresh
	store, err := vendor.NewJSONStore(f.Name())
	if err != nil {
		t.Fatalf("new json store: %v", err)
	}
	return store
}

// AC1: registered vendor profile is retrieved correctly.
func TestStore_CreateAndGet(t *testing.T) {
	store := newTempStore(t)

	req := vendor.CreateRequest{
		Name:         "Acme Ad Network",
		TargetURL:    "https://acme.example.com/event",
		Method:       "POST",
		AuthHeaders:  map[string]string{"X-Api-Key": "secret"},
		BodyTemplate: `{"uid":"{{user_id}}"}`,
	}

	created, err := store.Create(req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected a non-empty ID")
	}
	if created.Name != req.Name {
		t.Errorf("Name: got %q, want %q", created.Name, req.Name)
	}
	if created.TargetURL != req.TargetURL {
		t.Errorf("TargetURL: got %q, want %q", created.TargetURL, req.TargetURL)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, created.ID)
	}
	if got.AuthHeaders["X-Api-Key"] != "secret" {
		t.Errorf("AuthHeaders: got %v, want {X-Api-Key: secret}", got.AuthHeaders)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	store := newTempStore(t)
	_, err := store.Get("nonexistent-id")
	if !errors.Is(err, vendor.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_Update(t *testing.T) {
	store := newTempStore(t)
	created, _ := store.Create(vendor.CreateRequest{
		Name: "Old Name", TargetURL: "https://old.example.com", Method: "POST",
	})

	newName := "New Name"
	updated, err := store.Update(created.ID, vendor.UpdateRequest{Name: &newName})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != newName {
		t.Errorf("Name: got %q, want %q", updated.Name, newName)
	}
	// TargetURL should be unchanged
	if updated.TargetURL != "https://old.example.com" {
		t.Errorf("TargetURL changed unexpectedly: %q", updated.TargetURL)
	}
}

func TestStore_PersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/vendors.json"

	s1, _ := vendor.NewJSONStore(path)
	created, _ := s1.Create(vendor.CreateRequest{
		Name: "Persist Test", TargetURL: "https://persist.example.com", Method: "PUT",
	})

	// reload from same file
	s2, err := vendor.NewJSONStore(path)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	got, err := s2.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.Name != "Persist Test" {
		t.Errorf("Name after reload: got %q", got.Name)
	}
}
