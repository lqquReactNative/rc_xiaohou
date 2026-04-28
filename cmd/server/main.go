package main

import (
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
	"github.com/lqquReactNative/rc_xiaohou/internal/vendor"
)

func main() {
	dataFile := envOrDefault("VENDOR_DATA_FILE", "data/vendors.json")
	addr := envOrDefault("ADDR", ":8080")

	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	vendorStore, err := vendor.NewJSONStore(dataFile)
	if err != nil {
		log.Fatalf("failed to init vendor store: %v", err)
	}

	notifDBPath := envOrDefault("NOTIFICATION_DB_PATH", "data/notifications.db")
	notifStore, err := notification.NewSQLiteStore(notifDBPath)
	if err != nil {
		log.Fatalf("failed to init notification store: %v", err)
	}
	defer notifStore.Close()

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.Mount("/vendors", vendor.NewHandler(vendorStore).Routes())
	r.Mount("/notifications", notification.NewHandler(vendorStore, notification.NewPersistingEnqueuer(notifStore)).Routes())

	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
