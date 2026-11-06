// Command control runs the FlashSeat control plane: the expiry sweeper,
// simulation runner, live change-feed hub, invariant checker, and fault
// injector.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/config"
	"github.com/flashseat/flashseat/apps/api/internal/control"
	"github.com/flashseat/flashseat/apps/api/internal/inventory"
	"github.com/flashseat/flashseat/apps/api/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8081/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	cfg := config.Load()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s, err := store.OpenWithRetry(ctx, cfg.DatabaseURL, 12, func(err error) { slog.Warn("waiting for database", "error", err) })
	if err != nil {
		slog.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer s.Close()
	inv := inventory.New(cfg.DirectRedisAddr, 16)
	defer inv.Close()
	svc := control.NewService(cfg, s, inv)
	server := &http.Server{Addr: cfg.ControlAddr, Handler: svc.Router(cfg.WebOrigin), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("control plane listening", "addr", cfg.ControlAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server stopped", "error", err)
			cancel()
		}
	}()
	svc.Run(ctx)
	shutdown, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	_ = server.Shutdown(shutdown)
}
