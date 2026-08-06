package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/config"
	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type API struct {
	cfg       config.Config
	store     *store.Store
	leases    *reservation.Leases
	requests  *prometheus.CounterVec
	latency   *prometheus.HistogramVec
	conflicts prometheus.Counter
	live      sync.Map
}

func New(cfg config.Config, s *store.Store, leases *reservation.Leases) *API {
	a := &API{cfg: cfg, store: s, leases: leases,
		requests:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "flashseat_http_requests_total", Help: "HTTP requests."}, []string{"method", "route", "status"}),
		latency:   prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "flashseat_http_duration_seconds", Help: "HTTP latency.", Buckets: prometheus.DefBuckets}, []string{"method", "route"}),
		conflicts: prometheus.NewCounter(prometheus.CounterOpts{Name: "flashseat_hold_conflicts_total", Help: "Rejected hold attempts."}),
	}
	prometheus.MustRegister(a.requests, a.latency, a.conflicts)
	return a
}
func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(a.requestID, a.cors, a.measure)
	r.Get("/health/live", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/health/ready", a.ready)
	r.Handle("/metrics", promhttp.Handler())
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/sessions/demo", a.session)
		r.Get("/events", a.events)
		r.Get("/events/{eventID}", a.event)
		r.Get("/events/{eventID}/seats", a.seats)
		r.Get("/events/{eventID}/stream", a.stream)
		r.With(a.observeBenchmarkHold).Post("/holds", a.createHold)
		r.Get("/holds/{holdID}", a.hold)
		r.Delete("/holds/{holdID}", a.release)
		r.Post("/holds/{holdID}/checkout", a.checkout)
		r.Get("/orders/{orderID}", a.order)
		r.Post("/benchmarks", a.createBenchmark)
		r.Get("/benchmarks/results", a.benchmarkResults)
		r.Get("/benchmarks/{benchmarkID}/live", a.benchmarkLive)
		r.Get("/benchmarks/{benchmarkID}", a.benchmark)
	})
	return r
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (a *API) measure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		route := "unmatched"
		if c := chi.RouteContext(r.Context()); c != nil && c.RoutePattern() != "" {
			route = c.RoutePattern()
		}
		a.requests.WithLabelValues(r.Method, route, fmt.Sprint(sw.status)).Inc()
		a.latency.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}
func (a *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

type requestIDKey struct{}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", a.cfg.WebOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-Request-ID")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Pool.Ping(r.Context()); err != nil {
		problem(w, 503, "not_ready", "database unavailable")
		return
	}
	if err := a.leases.Ping(r.Context()); err != nil {
		problem(w, 503, "not_ready", "redis unavailable")
		return
	}
	jsonOut(w, 200, map[string]string{"status": "ready"})
}
func (a *API) session(w http.ResponseWriter, r *http.Request) {
	id := userID(r)
	if id == "" {
		id = uuid.NewString()
	}
	http.SetCookie(w, &http.Cookie{Name: "flashseat_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 7})
	jsonOut(w, 200, map[string]string{"userId": id})
}
func userID(r *http.Request) string {
	c, err := r.Cookie("flashseat_session")
	if err != nil {
		return ""
	}
	if _, err = uuid.Parse(c.Value); err != nil {
		return ""
	}
	return c.Value
}
func requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := userID(r)
	if id == "" {
		problem(w, 401, "session_required", "create a demo session first")
		return "", false
	}
	return id, true
}
func key(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(v) < 8 || len(v) > 100 {
		problem(w, 422, "invalid_idempotency_key", "Idempotency-Key must be 8–100 characters")
		return "", false
	}
	return v, true
}
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	v, err := a.store.Events(r.Context())
	if err != nil {
		internal(w, r, err)
		return
	}
	jsonOut(w, 200, map[string]any{"events": v})
}
func (a *API) event(w http.ResponseWriter, r *http.Request) {
	v, err := a.store.Event(r.Context(), chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	jsonOut(w, 200, v)
}
func (a *API) seats(w http.ResponseWriter, r *http.Request) {
	v, err := a.store.Seats(r.Context(), chi.URLParam(r, "eventID"), userID(r))
	if err != nil {
		internal(w, r, err)
		return
	}
	jsonOut(w, 200, map[string]any{"seats": v, "serverTime": time.Now().UTC()})
}

type holdInput struct {
	EventID string   `json:"eventId"`
	SeatIDs []string `json:"seatIds"`
}

func (a *API) createHold(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	idem, ok := key(w, r)
	if !ok {
		return
	}
	if existing, err := a.store.HoldByKey(r.Context(), user, idem); err == nil {
		jsonOut(w, 200, existing)
		return
	}
	var in holdInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		problem(w, 422, "invalid_json", "invalid request body")
		return
	}
	ids, err := reservation.NormalizeSeatIDs(in.SeatIDs)
	if err != nil {
		problem(w, 422, "invalid_seats", err.Error())
		return
	}
	token := uuid.NewString()
	won, err := a.leases.Acquire(r.Context(), in.EventID, ids, token, a.cfg.HoldTTL)
	if err != nil {
		internal(w, r, err)
		return
	}
	if !won {
		a.conflicts.Inc()
		problem(w, 409, "seat_unavailable", reservation.ErrConflict.Error())
		return
	}
	h, err := a.store.CreateHold(r.Context(), in.EventID, user, idem, token, ids, a.cfg.HoldTTL)
	if err != nil {
		_ = a.leases.Release(r.Context(), in.EventID, ids, token)
		if existing, replayErr := a.store.HoldByKey(r.Context(), user, idem); replayErr == nil {
			jsonOut(w, 200, existing)
			return
		}
		if errors.Is(err, reservation.ErrConflict) {
			a.conflicts.Inc()
		}
		writeErr(w, r, err)
		return
	}
	a.leases.Publish(r.Context(), in.EventID)
	jsonOut(w, 201, h)
}
func (a *API) hold(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	h, err := a.store.Hold(r.Context(), chi.URLParam(r, "holdID"), user)
	if err == nil && h.Status == "active" && reservation.IsExpired(h.ExpiresAt, time.Now()) {
		err = reservation.ErrExpired
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	jsonOut(w, 200, h)
}
func (a *API) release(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	event, token, ids, err := a.store.Release(r.Context(), chi.URLParam(r, "holdID"), user, "released")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	_ = a.leases.Release(r.Context(), event, ids, token)
	a.leases.Publish(r.Context(), event)
	w.WriteHeader(204)
}
func (a *API) checkout(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	idem, ok := key(w, r)
	if !ok {
		return
	}
	o, token, ids, err := a.store.Checkout(r.Context(), chi.URLParam(r, "holdID"), user, idem)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if token != "" {
		h, _ := a.store.Hold(r.Context(), o.HoldID, user)
		_ = a.leases.Release(r.Context(), h.EventID, ids, token)
		a.leases.Publish(r.Context(), h.EventID)
	}
	jsonOut(w, 200, o)
}
func (a *API) order(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	o, err := a.store.Order(r.Context(), chi.URLParam(r, "orderID"), user)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	jsonOut(w, 200, o)
}
func (a *API) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		problem(w, 500, "stream_unsupported", "streaming unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	pubsub := a.leases.Subscribe(r.Context(), chi.URLParam(r, "eventID"))
	defer pubsub.Close()
	fmt.Fprintf(w, "event: ready\ndata: {\"serverTime\":%q}\n\n", time.Now().UTC().Format(time.RFC3339Nano))
	flusher.Flush()
	ch := pubsub.Channel()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprint(w, "event: inventory\ndata: {}\n\n")
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code, message string) {
	jsonOut(w, status, map[string]any{"code": code, "message": message, "status": status})
}
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, reservation.ErrConflict):
		problem(w, 409, "seat_unavailable", err.Error())
	case errors.Is(err, reservation.ErrExpired):
		problem(w, 410, "hold_expired", err.Error())
	case errors.Is(err, reservation.ErrForbidden):
		problem(w, 403, "forbidden", err.Error())
	case errors.Is(err, reservation.ErrNotFound):
		problem(w, 404, "not_found", err.Error())
	default:
		internal(w, r, err)
	}
}
func internal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "request_id", r.Context().Value(requestIDKey{}), "error", err)
	problem(w, 500, "internal_error", "the request could not be completed")
}
