package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Chaos injects real faults. API replicas reach Redis and PostgreSQL through
// Toxiproxy, so disabling a proxy is a genuine network outage from their point
// of view. The control plane connects directly and keeps observing.
type Chaos struct {
	toxiproxy string
	apiURL    string
	redis     *redis.Client
	hub       *Hub
	client    *http.Client
	mu        sync.Mutex
	active    map[string]bool
}

// ChaosAction describes one fault the simulator can inject.
type ChaosAction struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Seconds     int    `json:"seconds"`
}

var chaosActions = []ChaosAction{
	{ID: "redis-outage", Label: "Cut Redis", Description: "API replicas lose their Redis connection for 6 seconds. Holds must fail closed, not fall back to unprotected writes.", Seconds: 6},
	{ID: "redis-wipe", Label: "Wipe Redis", Description: "Redis loses every key. The API must rebuild the seat bitmap from PostgreSQL before admitting holds again.", Seconds: 0},
	{ID: "postgres-slow", Label: "Slow Postgres", Description: "Adds 80 ms ± 20 ms to every PostgreSQL response for 8 seconds. Locks are held longer and contention rises.", Seconds: 8},
	{ID: "api-crash", Label: "Kill an API replica", Description: "One API process exits mid-request. The gateway routes around it while Docker restarts it.", Seconds: 0},
}

func NewChaos(toxiproxy, apiURL string, r *redis.Client, h *Hub) *Chaos {
	return &Chaos{toxiproxy: toxiproxy, apiURL: apiURL, redis: r, hub: h, client: &http.Client{Timeout: 3 * time.Second}, active: map[string]bool{}}
}

var ErrChaosBusy = errors.New("that fault is already active")
var ErrChaosUnavailable = errors.New("fault injection is not configured")

func (c *Chaos) toxi(ctx context.Context, method, path, body string) error {
	if c.toxiproxy == "" {
		return ErrChaosUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, method, c.toxiproxy+path, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("toxiproxy %s %s: %d %s", method, path, resp.StatusCode, b)
	}
	return nil
}

// Trigger starts a fault. Timed faults end on their own.
func (c *Chaos) Trigger(ctx context.Context, action string) (ChaosMark, error) {
	c.mu.Lock()
	if c.active[action] {
		c.mu.Unlock()
		return ChaosMark{}, ErrChaosBusy
	}
	c.active[action] = true
	c.mu.Unlock()
	done := func() { c.mu.Lock(); delete(c.active, action); c.mu.Unlock() }

	switch action {
	case "redis-outage":
		if err := c.toxi(ctx, http.MethodPost, "/proxies/redis", `{"enabled":false}`); err != nil {
			done()
			return ChaosMark{}, err
		}
		mark := c.hub.MarkChaos(action, "start", "Redis unreachable from API replicas")
		c.after(6*time.Second, func(ctx context.Context) error {
			return c.toxi(ctx, http.MethodPost, "/proxies/redis", `{"enabled":true}`)
		}, action, "Redis reachable again", done)
		return mark, nil
	case "postgres-slow":
		body := `{"name":"pg_latency","type":"latency","stream":"downstream","toxicity":1,"attributes":{"latency":80,"jitter":20}}`
		if err := c.toxi(ctx, http.MethodPost, "/proxies/postgres/toxics", body); err != nil {
			done()
			return ChaosMark{}, err
		}
		mark := c.hub.MarkChaos(action, "start", "PostgreSQL responses delayed 80 ms")
		c.after(8*time.Second, func(ctx context.Context) error {
			return c.toxi(ctx, http.MethodDelete, "/proxies/postgres/toxics/pg_latency", "")
		}, action, "PostgreSQL latency back to normal", done)
		return mark, nil
	case "redis-wipe":
		defer done()
		if err := c.redis.FlushAll(ctx).Err(); err != nil {
			return ChaosMark{}, err
		}
		return c.hub.MarkChaos(action, "start", "every Redis key deleted"), nil
	case "api-crash":
		defer done()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/internal/crash", nil)
		resp, err := c.client.Do(req)
		if err != nil {
			return ChaosMark{}, err
		}
		defer resp.Body.Close()
		var body bytes.Buffer
		_, _ = io.Copy(&body, io.LimitReader(resp.Body, 256))
		return c.hub.MarkChaos(action, "start", "API replica terminated: "+body.String()), nil
	}
	done()
	return ChaosMark{}, fmt.Errorf("unknown fault %q", action)
}

func (c *Chaos) after(d time.Duration, undo func(context.Context) error, action, detail string, done func()) {
	go func() {
		defer done()
		time.Sleep(d)
		for attempt := 0; attempt < 5; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := undo(ctx)
			cancel()
			if err == nil {
				c.hub.MarkChaos(action, "end", detail)
				return
			}
			slog.Error("could not end fault; retrying", "action", action, "error", err)
			time.Sleep(time.Second)
		}
	}()
}

// Heal removes every fault. Called before each run so a run never starts
// against a degraded system.
func (c *Chaos) Heal(ctx context.Context) {
	if c.toxiproxy == "" {
		return
	}
	_ = c.toxi(ctx, http.MethodPost, "/proxies/redis", `{"enabled":true}`)
	_ = c.toxi(ctx, http.MethodDelete, "/proxies/postgres/toxics/pg_latency", "")
}
