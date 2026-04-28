package notification_test

import (
	"path/filepath"
	"testing"

	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
)

// TestPersistingEnqueuer_SavesBeforeReturningID verifies that the notification is
// durably written to the store before the caller receives the ID (AC: persisted before 202).
func TestPersistingEnqueuer_SavesBeforeReturningID(t *testing.T) {
	mem := &notification.InMemoryStore{}
	enqueuer := notification.NewPersistingEnqueuer(mem)

	id, err := enqueuer.Enqueue(
		"ad-system-1",
		`{"event":"user_registered"}`,
		map[string]string{"Authorization": "Bearer tok"},
		"https://ads.example.com/events",
		"POST",
	)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}
	if len(mem.Records) != 1 {
		t.Fatalf("expected 1 persisted record before ID was returned, got %d", len(mem.Records))
	}
	if mem.Records[0].ID != id {
		t.Errorf("persisted ID %q != returned ID %q", mem.Records[0].ID, id)
	}
	if mem.Records[0].Status != notification.StatusPending {
		t.Errorf("expected status pending, got %q", mem.Records[0].Status)
	}
}

func TestSQLiteStore_Save(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "notifications_test.db")
	store, err := notification.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	enqueuer := notification.NewPersistingEnqueuer(store)

	id, err := enqueuer.Enqueue(
		"crm-platform",
		`{"contact_id":"c-7"}`,
		map[string]string{},
		"https://crm.example.com/webhooks",
		"POST",
	)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID from SQLite-backed enqueuer")
	}
}
