package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/go-chi/chi/v5"
)

type benchmarkInput struct {
	EventID string `json:"eventId"`
	SeatID  string `json:"seatId"`
	Users   int    `json:"users"`
}

type loadgenRequest struct {
	JobID     string `json:"jobId"`
	Users     int    `json:"users"`
	EventID   string `json:"eventId"`
	SeatID    string `json:"seatId"`
	TargetURL string `json:"targetUrl"`
}

type loadgenResult struct {
	Requests         int64           `json:"requests"`
	Winners          int64           `json:"winners"`
	Conflicts        int64           `json:"conflicts"`
	UnexpectedErrors int64           `json:"unexpectedErrors"`
	BarrierMisses    int64           `json:"barrierMisses"`
	ThroughputRPS    float64         `json:"throughputRps"`
	P50MS            float64         `json:"p50Ms"`
	P95MS            float64         `json:"p95Ms"`
	P99MS            float64         `json:"p99Ms"`
	K6Version        string          `json:"k6Version"`
	RawSummary       json.RawMessage `json:"rawSummary"`
}

func (a *API) createBenchmark(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.LabEnabled {
		problem(w, http.StatusNotFound, "lab_disabled", "the benchmark lab is disabled")
		return
	}
	var in benchmarkInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		problem(w, 422, "invalid_json", "invalid request body")
		return
	}
	if in.EventID == "" || in.SeatID == "" || in.Users < 2 || in.Users > 10000 {
		problem(w, 422, "invalid_benchmark_input", "choose one seat and between 2 and 10,000 HTTP clients")
		return
	}
	job, err := a.store.CreateBenchmark(r.Context(), in.EventID, in.SeatID, in.Users)
	if errors.Is(err, reservation.ErrConflict) {
		problem(w, 409, "benchmark_running", "another HTTP benchmark is already running")
		return
	}
	if err != nil {
		internal(w, r, err)
		return
	}
	a.live.Store(job.ID, &benchmarkLiveState{target: int64(job.VirtualUsers)})
	go a.runBenchmark(job)
	jsonOut(w, http.StatusAccepted, job)
}

func (a *API) benchmark(w http.ResponseWriter, r *http.Request) {
	job, err := a.store.Benchmark(r.Context(), chi.URLParam(r, "benchmarkID"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	jsonOut(w, 200, job)
}

func (a *API) benchmarkResults(w http.ResponseWriter, r *http.Request) {
	results, err := a.store.BenchmarkResults(r.Context())
	if err != nil {
		internal(w, r, err)
		return
	}
	jsonOut(w, 200, results)
}

func (a *API) runBenchmark(job store.BenchmarkJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := a.store.StartBenchmark(ctx, job.ID, a.cfg.BenchmarkTargetURL); err != nil {
		slog.Error("benchmark could not start", "benchmark_id", job.ID, "error", err)
		return
	}
	fail := func(err error) {
		slog.Error("HTTP benchmark failed", "benchmark_id", job.ID, "error", err)
		_ = a.store.FailBenchmark(context.Background(), job.ID, err.Error())
		a.cleanupBenchmark(context.Background(), job.ID)
		time.AfterFunc(10*time.Minute, func() { a.live.Delete(job.ID) })
	}
	payload, _ := json.Marshal(loadgenRequest{JobID: job.ID, Users: job.VirtualUsers, EventID: job.EventID, SeatID: job.SeatID, TargetURL: a.cfg.BenchmarkTargetURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.LoadgenURL+"/run", bytes.NewReader(payload))
	if err != nil {
		fail(err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 3*time.Minute + 30*time.Second}).Do(req)
	if err != nil {
		fail(fmt.Errorf("load generator unavailable: %w", err))
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		fail(err)
		return
	}
	if response.StatusCode != http.StatusOK {
		fail(fmt.Errorf("load generator returned %d: %s", response.StatusCode, string(body)))
		return
	}
	var result loadgenResult
	if err = json.Unmarshal(body, &result); err != nil {
		fail(fmt.Errorf("invalid load-generator result: %w", err))
		return
	}
	claims, err := a.store.ActiveClaims(ctx, job.SeatID)
	if err != nil {
		fail(err)
		return
	}
	doubleBookings := max(0, claims-1)
	invariant := result.Requests == int64(job.VirtualUsers) && result.Winners == 1 && result.Conflicts == int64(job.VirtualUsers-1) && result.UnexpectedErrors == 0 && result.BarrierMisses == 0 && claims == 1
	metrics := store.BenchmarkMetrics{Requests: result.Requests, Winners: result.Winners, Conflicts: result.Conflicts, UnexpectedErrors: result.UnexpectedErrors, BarrierMisses: result.BarrierMisses, ThroughputRPS: result.ThroughputRPS, P50MS: result.P50MS, P95MS: result.P95MS, P99MS: result.P99MS, DatabaseActiveClaims: claims, DoubleBookings: doubleBookings, InvariantPassed: invariant, K6Version: result.K6Version, TargetURL: a.cfg.BenchmarkTargetURL, RawSummary: result.RawSummary}
	if err = a.store.CompleteBenchmark(ctx, job.ID, metrics); err != nil {
		fail(err)
		return
	}
	a.cleanupBenchmark(context.Background(), job.ID)
	time.AfterFunc(10*time.Minute, func() { a.live.Delete(job.ID) })
}

func (a *API) cleanupBenchmark(ctx context.Context, benchmarkID string) {
	leases, err := a.store.ReleaseBenchmarkHolds(ctx, benchmarkID)
	if err != nil {
		slog.Error("benchmark cleanup failed", "benchmark_id", benchmarkID, "error", err)
		return
	}
	for _, lease := range leases {
		_ = a.leases.Release(ctx, lease.EventID, lease.SeatIDs, lease.Token)
		a.leases.Publish(ctx, lease.EventID)
	}
}
