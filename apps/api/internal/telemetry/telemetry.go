// Package telemetry carries live request outcomes from API replicas to the
// control plane: exact counters, fixed-bucket latency histograms (mergeable
// across replicas), and a stratified sample of individual buyer actions.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"time"
)

// Bounds are histogram bucket upper bounds in milliseconds. A final implicit
// bucket holds everything slower.
var Bounds = []float64{1, 2, 3, 5, 8, 12, 20, 30, 50, 80, 120, 200, 300, 500, 800, 1200, 2000, 5000}

// Sample is one buyer action shown in the live feed.
type Sample struct {
	T      int64  `json:"t"`
	Actor  string `json:"a"`
	Kind   string `json:"k"`
	Status int    `json:"s"`
	Detail string `json:"d"`
}

// Report is one replica's activity for one flush window.
type Report struct {
	Replica  string           `json:"replica"`
	Event    string           `json:"event"`
	Counts   map[string]int64 `json:"counts"`
	Hold     []int64          `json:"hold"`
	Checkout []int64          `json:"checkout"`
	Samples  []Sample         `json:"samples"`
}

func NewHistogram() []int64 { return make([]int64, len(Bounds)+1) }

func bucket(ms float64) int {
	for i, b := range Bounds {
		if ms <= b {
			return i
		}
	}
	return len(Bounds)
}

// Merge adds src into dst.
func Merge(dst, src []int64) {
	for i := range min(len(dst), len(src)) {
		dst[i] += src[i]
	}
}

// Quantile estimates a quantile in milliseconds by interpolating inside the
// bucket that contains it.
func Quantile(h []int64, q float64) float64 {
	var total int64
	for _, c := range h {
		total += c
	}
	if total == 0 {
		return 0
	}
	rank := q * float64(total)
	var seen float64
	for i, c := range h {
		if c == 0 {
			continue
		}
		if seen+float64(c) >= rank {
			lo := 0.0
			if i > 0 {
				lo = Bounds[i-1]
			}
			hi := lo * 2
			if i < len(Bounds) {
				hi = Bounds[i]
			}
			return lo + (hi-lo)*math.Max(0, rank-seen)/float64(c)
		}
		seen += float64(c)
	}
	return Bounds[len(Bounds)-1]
}

const samplesPerClass = 4

type window struct {
	counts         map[string]int64
	hold, checkout []int64
	samples        []Sample
	perClass       map[string]int
}

func newWindow() *window {
	return &window{counts: map[string]int64{}, hold: NewHistogram(), checkout: NewHistogram(), perClass: map[string]int{}}
}

// Recorder accumulates one replica's activity between flushes.
type Recorder struct {
	mu      sync.Mutex
	events  map[string]*window
	replica string
	url     string
	client  *http.Client
}

func NewRecorder(replica, controlURL string) *Recorder {
	return &Recorder{events: map[string]*window{}, replica: replica, url: controlURL, client: &http.Client{Timeout: 500 * time.Millisecond}}
}

func (r *Recorder) win(event string) *window {
	w, ok := r.events[event]
	if !ok {
		w = newWindow()
		r.events[event] = w
	}
	return w
}

// Record counts one outcome, adds its latency to the histogram for kind
// ("hold" or "checkout"), and keeps it as a feed sample if its class (kind +
// status) has room this window.
func (r *Recorder) Record(event, key, kind string, latency time.Duration, s Sample) {
	if r == nil || r.url == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.win(event)
	w.counts[key]++
	ms := float64(latency.Microseconds()) / 1000
	switch kind {
	case "hold":
		w.hold[bucket(ms)]++
	case "checkout":
		w.checkout[bucket(ms)]++
	}
	class := key
	if w.perClass[class] < samplesPerClass {
		w.perClass[class]++
		s.T = time.Now().UnixMilli()
		w.samples = append(w.samples, s)
	}
}

// Run flushes every interval until ctx ends.
func (r *Recorder) Run(ctx context.Context, interval time.Duration) {
	if r == nil || r.url == "" {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.flush(ctx)
		}
	}
}

func (r *Recorder) flush(ctx context.Context) {
	r.mu.Lock()
	events := r.events
	r.events = map[string]*window{}
	r.mu.Unlock()
	for event, w := range events {
		body, _ := json.Marshal(Report{Replica: r.replica, Event: event, Counts: w.counts, Hold: w.hold, Checkout: w.checkout, Samples: w.samples})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+"/internal/telemetry", bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if resp, err := r.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
}
