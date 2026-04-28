package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/lqquReactNative/rc_xiaohou/internal/delivery"
	"github.com/lqquReactNative/rc_xiaohou/internal/dlq"
	"github.com/lqquReactNative/rc_xiaohou/internal/notification"
	"github.com/lqquReactNative/rc_xiaohou/internal/vendor"
)

func main() {
	dataFile := envOrDefault("VENDOR_DATA_FILE", "data/vendors.json")
	addr := envOrDefault("ADDR", ":8080")
	notifDBPath := envOrDefault("NOTIFICATION_DB_PATH", "data/notifications.db")

	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	vendorStore, err := vendor.NewJSONStore(dataFile)
	if err != nil {
		log.Fatalf("failed to init vendor store: %v", err)
	}

	notifStore, err := notification.NewSQLiteStore(notifDBPath)
	if err != nil {
		log.Fatalf("failed to init notification store: %v", err)
	}
	defer notifStore.Close()

	// Start delivery worker with exponential backoff retry policy.
	policy := delivery.DefaultPolicy
	worker := delivery.NewWorker(notifStore, policy, 30*time.Second, 10*time.Second)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go worker.Run(ctx)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.Mount("/vendors", vendor.NewHandler(vendorStore).Routes())
	r.Mount("/notifications", notification.NewHandler(vendorStore, notification.NewPersistingEnqueuer(notifStore)).Routes())
	r.Mount("/dlq", dlq.NewHandler(notifStore).Routes())

	srv := &http.Server{Addr: addr, Handler: r}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	log.Printf("listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
