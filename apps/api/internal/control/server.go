package control

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/flashseat/flashseat/apps/api/internal/telemetry"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
	"github.com/go-chi/chi/v5"
)

// Router serves the control API. The gateway exposes /control under
// /api/v1/control; /internal is reachable only inside the cluster network.
func (s *Service) Router(webOrigin string) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", webOrigin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(204)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { out(w, 200, map[string]string{"status": "ok"}) })
	r.Post("/internal/telemetry", s.telemetry)
	r.Route("/control", func(r chi.Router) {
		r.Get("/stream", s.stream)
		r.Get("/state", func(w http.ResponseWriter, r *http.Request) { out(w, 200, s.Hub.Snapshot()) })
		r.Get("/options", s.options)
		r.Post("/simulations", s.createRun)
		r.Get("/simulations", s.listRuns)
		r.Get("/simulations/{id}", s.getRun)
		r.Get("/simulations/{id}/timeline", s.timeline)
		r.Post("/chaos/{action}", s.chaos)
	})
	return r
}

func out(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	out(w, status, map[string]any{"code": code, "message": msg, "status": status})
}

func (s *Service) telemetry(w http.ResponseWriter, r *http.Request) {
	var rep telemetry.Report
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&rep); err != nil {
		fail(w, 422, "invalid_report", err.Error())
		return
	}
	s.Hub.AddReport(rep)
	w.WriteHeader(204)
}

func (s *Service) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "stream_unsupported", "streaming unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := s.Hub.Subscribe()
	defer cancel()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, open := <-ch:
			if !open {
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

type presetView struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Seats    int                 `json:"seats"`
	Sections []venue.SectionSpec `json:"sections"`
	Defaults RunInput            `json:"defaults"`
}

func (s *Service) options(w http.ResponseWriter, r *http.Request) {
	defaults := map[string]RunInput{
		"club":  {Mode: "safe", Preset: "club", Buyers: 900, ArrivalSeconds: 10, AdmitRate: 120, HoldTTLSeconds: 8},
		"arena": {Mode: "safe", Preset: "arena", Buyers: 5000, ArrivalSeconds: 20, AdmitRate: 300, HoldTTLSeconds: 10},
	}
	var presets []presetView
	for id, p := range venue.Presets {
		presets = append(presets, presetView{ID: id, Name: p.Name, Seats: p.Seats(), Sections: p.Sections, Defaults: defaults[id]})
	}
	sort.Slice(presets, func(a, b int) bool { return presets[a].Seats < presets[b].Seats })
	out(w, 200, map[string]any{"presets": presets, "chaos": chaosActions})
}

func (s *Service) createRun(w http.ResponseWriter, r *http.Request) {
	var in RunInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		fail(w, 422, "invalid_json", "invalid request body")
		return
	}
	if err := in.Validate(); err != nil {
		fail(w, 422, "invalid_simulation", err.Error())
		return
	}
	run, err := s.store.CreateRun(r.Context(), store.Run{Mode: in.Mode, Preset: in.Preset, Seats: venue.Presets[in.Preset].Seats(), Buyers: in.Buyers,
		ArrivalSeconds: in.ArrivalSeconds, AdmitRate: in.AdmitRate, HoldTTLSeconds: in.HoldTTLSeconds})
	if errors.Is(err, store.ErrRunActive) {
		fail(w, 409, "simulation_running", err.Error())
		return
	}
	if err != nil {
		slog.Error("create run", "error", err)
		fail(w, 500, "internal_error", "could not queue the simulation")
		return
	}
	out(w, 202, run)
}

func (s *Service) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.Runs(r.Context(), 50)
	if err != nil {
		slog.Error("list runs", "error", err)
		fail(w, 500, "internal_error", "could not list runs")
		return
	}
	out(w, 200, map[string]any{"runs": runs})
}

func (s *Service) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.Run(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, reservation.ErrNotFound) {
		fail(w, 404, "not_found", "no such run")
		return
	}
	if err != nil {
		fail(w, 404, "not_found", "no such run")
		return
	}
	out(w, 200, run)
}

func (s *Service) timeline(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Timeline(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, 404, "not_found", "this run has no recorded timeline")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(b)
}

func (s *Service) chaos(w http.ResponseWriter, r *http.Request) {
	if s.Hub.Phase() != PhaseSelling {
		fail(w, 409, "no_active_sale", "faults can only be injected while a sale is running")
		return
	}
	mark, err := s.Chaos.Trigger(r.Context(), chi.URLParam(r, "action"))
	switch {
	case errors.Is(err, ErrChaosBusy):
		fail(w, 409, "fault_active", err.Error())
	case errors.Is(err, ErrChaosUnavailable):
		fail(w, 501, "chaos_unavailable", err.Error())
	case err != nil:
		slog.Error("fault injection failed", "error", err)
		fail(w, 502, "chaos_failed", err.Error())
	default:
		out(w, 202, mark)
	}
}
