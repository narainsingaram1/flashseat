package httpapi

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type benchmarkLiveState struct {
	target    int64
	attempts  atomic.Int64
	winners   atomic.Int64
	conflicts atomic.Int64
	errors    atomic.Int64
	firstNS   atomic.Int64
	lastNS    atomic.Int64
}

type benchmarkLiveResponse struct {
	JobID       string  `json:"jobId"`
	Target      int64   `json:"target"`
	Attempts    int64   `json:"attempts"`
	Winners     int64   `json:"winners"`
	Conflicts   int64   `json:"conflicts"`
	Errors      int64   `json:"errors"`
	Remaining   int64   `json:"remaining"`
	Progress    float64 `json:"progress"`
	ObservedRPS float64 `json:"observedRps"`
	FirstAt     string  `json:"firstAt,omitempty"`
	LastAt      string  `json:"lastAt,omitempty"`
}

type benchmarkStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *benchmarkStatusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func benchmarkIDFromKey(key string) (string, bool) {
	const prefix = "bench-"
	if !strings.HasPrefix(key, prefix) || len(key) < len(prefix)+36+2 {
		return "", false
	}
	id := key[len(prefix) : len(prefix)+36]
	if key[len(prefix)+36] != '-' {
		return "", false
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", false
	}
	return id, true
}

func (a *API) observeBenchmarkHold(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jobID, tracked := benchmarkIDFromKey(strings.TrimSpace(r.Header.Get("Idempotency-Key")))
		if !tracked {
			next.ServeHTTP(w, r)
			return
		}
		sw := &benchmarkStatusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if value, ok := a.live.Load(jobID); ok {
			state := value.(*benchmarkLiveState)
			now := time.Now().UnixNano()
			state.firstNS.CompareAndSwap(0, now)
			state.lastNS.Store(now)
			state.attempts.Add(1)
			switch sw.status {
			case http.StatusCreated:
				state.winners.Add(1)
			case http.StatusConflict:
				state.conflicts.Add(1)
			default:
				state.errors.Add(1)
			}
		}
	})
}

func (a *API) benchmarkLive(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "benchmarkID")
	if value, ok := a.live.Load(jobID); ok {
		state := value.(*benchmarkLiveState)
		jsonOut(w, 200, liveSnapshot(jobID, state))
		return
	}
	job, err := a.store.Benchmark(r.Context(), jobID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	jsonOut(w, 200, benchmarkLiveResponse{JobID: job.ID, Target: int64(job.VirtualUsers), Attempts: job.Requests, Winners: job.Winners, Conflicts: job.Conflicts, Errors: job.UnexpectedErrors, Remaining: max(0, int64(job.VirtualUsers)-job.Requests), Progress: progress(job.Requests, int64(job.VirtualUsers)), ObservedRPS: job.ThroughputRPS})
}

func liveSnapshot(jobID string, state *benchmarkLiveState) benchmarkLiveResponse {
	attempts := state.attempts.Load()
	first, last := state.firstNS.Load(), state.lastNS.Load()
	rate := 0.0
	if first > 0 && last >= first {
		elapsed := max(float64(last-first)/float64(time.Second), 0.001)
		rate = float64(attempts) / elapsed
	}
	out := benchmarkLiveResponse{JobID: jobID, Target: state.target, Attempts: attempts, Winners: state.winners.Load(), Conflicts: state.conflicts.Load(), Errors: state.errors.Load(), Remaining: max(0, state.target-attempts), Progress: progress(attempts, state.target), ObservedRPS: rate}
	if first > 0 {
		out.FirstAt = time.Unix(0, first).UTC().Format(time.RFC3339Nano)
		out.LastAt = time.Unix(0, last).UTC().Format(time.RFC3339Nano)
	}
	return out
}

func progress(attempts, target int64) float64 {
	if target <= 0 {
		return 0
	}
	return min(100, float64(attempts)*100/float64(target))
}
