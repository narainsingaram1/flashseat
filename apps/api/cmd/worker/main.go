package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/config"
	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/store"
)

func main() {
	cfg := config.Load()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer s.Close()
	leases := reservation.NewLeases(cfg.RedisAddr)
	defer leases.Close()
	ticker := time.NewTicker(cfg.SweepInterval)
	defer ticker.Stop()
	for {
		expire(ctx, s, leases)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func expire(ctx context.Context, s *store.Store, l *reservation.Leases) {
	batch, err := s.ExpireBatch(ctx, 100)
	if err != nil {
		slog.Error("expiration sweep failed", "error", err)
		return
	}
	for _, item := range batch {
		event := item[0].(string)
		ids := item[1].([]string)
		token := item[2].(string)
		_ = l.Release(ctx, event, ids, token)
		l.Publish(ctx, event)
	}
	if len(batch) > 0 {
		slog.Info("expired holds", "count", len(batch))
	}
}
