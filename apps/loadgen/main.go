package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type runRequest struct {
	JobID, EventID, SeatID, TargetURL string
	Users                             int
}

type k6Summary struct {
	Requests         int64   `json:"requests"`
	Winners          int64   `json:"winners"`
	Conflicts        int64   `json:"conflicts"`
	UnexpectedErrors int64   `json:"unexpectedErrors"`
	BarrierMisses    int64   `json:"barrierMisses"`
	ThroughputRPS    float64 `json:"throughputRps"`
	P50MS            float64 `json:"p50Ms"`
	P95MS            float64 `json:"p95Ms"`
	P99MS            float64 `json:"p99Ms"`
}

type runResult struct {
	k6Summary
	K6Version  string          `json:"k6Version"`
	RawSummary json.RawMessage `json:"rawSummary"`
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9-]{1,100}$`)

func main() {
	version := strings.TrimSpace(commandOutput("k6", "version"))
	h := http.NewServeMux()
	h.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ready", "k6Version": version})
	})
	h.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var in runRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || in.Users < 2 || in.Users > 10000 || !safeID.MatchString(in.JobID) || !safeID.MatchString(in.EventID) || !safeID.MatchString(in.SeatID) {
			writeJSON(w, 422, map[string]string{"error": "invalid allowlisted benchmark request"})
			return
		}
		allowedTarget := env("ALLOWED_TARGET_URL", "http://api:8080/api/v1")
		if in.TargetURL != allowedTarget {
			writeJSON(w, 422, map[string]string{"error": "target URL is not allowlisted"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		startAt := time.Now().Add(8 * time.Second).UnixMilli()
		cmd := exec.CommandContext(ctx, "k6", "run", "--quiet", "--no-color", "-e", "BASE_URL="+in.TargetURL, "-e", "USERS="+strconv.Itoa(in.Users), "-e", "EVENT_ID="+in.EventID, "-e", "SEAT_ID="+in.SeatID, "-e", "JOB_ID="+in.JobID, "-e", fmt.Sprintf("START_AT_MS=%d", startAt), "/scripts/same-seat-http.js")
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			writeJSON(w, 500, map[string]string{"error": "k6 failed", "detail": tail(output.String(), 3000)})
			return
		}
		summary, raw, err := parseK6Output(output.String())
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "invalid k6 summary", "detail": err.Error()})
			return
		}
		writeJSON(w, 200, runResult{k6Summary: summary, K6Version: version, RawSummary: raw})
	})
	log.Printf("load generator listening on :8090 (%s)", version)
	log.Fatal(http.ListenAndServe(":8090", h))
}

func parseK6Output(output string) (k6Summary, json.RawMessage, error) {
	const marker = "FLASHSEAT_RESULT="
	idx := strings.LastIndex(output, marker)
	if idx < 0 {
		return k6Summary{}, nil, fmt.Errorf("k6 result marker missing")
	}
	raw := json.RawMessage(strings.TrimSpace(output[idx+len(marker):]))
	var summary k6Summary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return summary, nil, err
	}
	return summary, raw, nil
}

func commandOutput(name string, args ...string) string {
	out, _ := exec.Command(name, args...).CombinedOutput()
	return string(out)
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func tail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
