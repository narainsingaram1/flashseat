package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/config"
	"github.com/flashseat/flashseat/apps/api/internal/httpapi"
	"github.com/flashseat/flashseat/apps/api/internal/inventory"
	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/flashseat/flashseat/apps/api/internal/telemetry"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck("http://127.0.0.1:8080/health/live"))
	}
	cfg := config.Load()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s, err := store.OpenWithRetry(ctx, cfg.DatabaseURL, int32(cfg.DBMaxConns), func(err error) { slog.Warn("waiting for database", "error", err) })
	if err != nil {
		slog.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer s.Close()
	inv := inventory.New(cfg.RedisAddr, cfg.RedisPoolSize)
	defer inv.Close()
	tel := telemetry.NewRecorder(cfg.Replica, cfg.ControlURL)
	go tel.Run(ctx, 250*time.Millisecond)
	api := httpapi.New(cfg, s, inv, tel)
	go api.Run(ctx)
	if addr := os.Getenv("PPROF_ADDR"); addr != "" {
		// Opt-in profiling on a separate listener; never exposed by default.
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		go func() { _ = http.ListenAndServe(addr, mux) }()
	}
	server := &http.Server{Addr: cfg.Addr, Handler: api.Router(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		slog.Info("api listening", "addr", cfg.Addr, "replica", cfg.Replica, "db_max_conns", cfg.DBMaxConns)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server stopped", "error", err)
			cancel()
		}
	}()
	<-ctx.Done()
	shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	_ = server.Shutdown(shutdown)
}

func healthcheck(url string) int {
	client := http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil || response.StatusCode != http.StatusOK {
		return 1
	}
	_ = response.Body.Close()
	return 0
}
