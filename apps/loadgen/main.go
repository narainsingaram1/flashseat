// Command loadgen runs the bundled k6 on-sale scenario on request. It accepts
// one fixed scenario against one allowlisted target, so it is not an open
// load-testing proxy.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type runRequest struct {
	RunID          string `json:"runId"`
	EventID        string `json:"eventId"`
	Buyers         int    `json:"buyers"`
	ArrivalSeconds int    `json:"arrivalSeconds"`
	TargetURL      string `json:"targetUrl"`
}

type runResult struct {
	K6Version string          `json:"k6Version"`
	Summary   json.RawMessage `json:"summary"`
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9-]{1,64}$`)

func (r runRequest) validate(allowedTarget string) error {
	switch {
	case !safeID.MatchString(r.RunID) || !safeID.MatchString(r.EventID):
		return errors.New("invalid run or event id")
	case r.Buyers < 10 || r.Buyers > 20000:
		return errors.New("buyers must be between 10 and 20,000")
	case r.ArrivalSeconds < 1 || r.ArrivalSeconds > 300:
		return errors.New("arrival window must be 1–300 seconds")
	case r.TargetURL != allowedTarget:
		return errors.New("target URL is not allowlisted")
	}
	return nil
}

func main() {
	version := strings.TrimSpace(commandOutput("k6", "version"))
	allowed := env("ALLOWED_TARGET_URL", "http://gateway:8080/api/v1")
	script := env("SCENARIO_PATH", "/scripts/onsale.js")
	var busy sync.Mutex
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
			writeJSON(w, 422, map[string]string{"error": "invalid request body"})
			return
		}
		if err := in.validate(allowed); err != nil {
			writeJSON(w, 422, map[string]string{"error": err.Error()})
			return
		}
		if !busy.TryLock() {
			writeJSON(w, 409, map[string]string{"error": "a load test is already running"})
			return
		}
		defer busy.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(in.ArrivalSeconds)*time.Second+6*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "k6", "run", "--quiet", "--no-color",
			"-e", "BASE_URL="+in.TargetURL, "-e", "EVENT_ID="+in.EventID, "-e", "RUN_ID="+in.RunID,
			"-e", "BUYERS="+strconv.Itoa(in.Buyers), "-e", "ARRIVAL_SECONDS="+strconv.Itoa(in.ArrivalSeconds), script)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		started := time.Now()
		err := cmd.Run()
		summary, parseErr := parseK6Output(output.String())
		log.Printf("run %s: %d buyers in %s (k6 err=%v)", in.RunID, in.Buyers, time.Since(started).Round(time.Millisecond), err)
		if parseErr != nil {
			detail := "k6 produced no summary"
			if err != nil {
				detail = err.Error()
			}
			writeJSON(w, 500, map[string]string{"error": detail, "output": tail(output.String(), 3000)})
			return
		}
		writeJSON(w, 200, runResult{K6Version: version, Summary: summary})
	})
	log.Printf("load generator listening on :8090 (%s)", version)
	log.Fatal(http.ListenAndServe(":8090", h))
}

func parseK6Output(output string) (json.RawMessage, error) {
	const marker = "FLASHSEAT_RESULT="
	idx := strings.LastIndex(output, marker)
	if idx < 0 {
		return nil, fmt.Errorf("k6 result marker missing")
	}
	line := output[idx+len(marker):]
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	raw := json.RawMessage(strings.TrimSpace(line))
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	return raw, nil
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
