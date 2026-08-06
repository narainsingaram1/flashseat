package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBenchmarkIDFromKey(t *testing.T) {
	id := "97a506dc-664b-4254-8ec9-14454b58f4df"
	got, ok := benchmarkIDFromKey("bench-" + id + "-42")
	if !ok || got != id {
		t.Fatalf("got %q, %v", got, ok)
	}
	if _, ok = benchmarkIDFromKey("normal-checkout-key"); ok {
		t.Fatal("ordinary idempotency key must not be tracked")
	}
}

func TestObserveBenchmarkHoldRecordsRealStatus(t *testing.T) {
	id := "97a506dc-664b-4254-8ec9-14454b58f4df"
	a := &API{}
	state := &benchmarkLiveState{target: 100}
	a.live.Store(id, state)
	handler := a.observeBenchmarkHold(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/holds", nil)
	request.Header.Set("Idempotency-Key", "bench-"+id+"-7")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if state.attempts.Load() != 1 || state.conflicts.Load() != 1 || state.winners.Load() != 0 {
		t.Fatalf("unexpected live counters: %+v", liveSnapshot(id, state))
	}
}
