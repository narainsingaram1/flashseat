package control

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"sync"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/flashseat/flashseat/apps/api/internal/telemetry"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
)

// Run phases shown to viewers.
const (
	PhaseIdle      = "idle"
	PhasePreparing = "preparing"
	PhaseSelling   = "selling"
	PhaseSettling  = "settling"
	PhaseDone      = "done"
	PhaseFailed    = "failed"
)

// FrameInterval is how often viewers receive updates.
const FrameInterval = 100 * time.Millisecond

const maxFeedPerFrame = 24

// ChaosMark records a fault injection on the run timeline.
type ChaosMark struct {
	T      int64  `json:"t"`
	Action string `json:"action"`
	Event  string `json:"event"` // start | end
	Detail string `json:"detail"`
}

// Point is one second of aggregated activity.
type Point struct {
	T         int64   `json:"t"`
	Holds     int64   `json:"holds"`
	Conflicts int64   `json:"conflicts"`
	SoldOut   int64   `json:"soldOut"`
	Failures  int64   `json:"failures"`
	Checkouts int64   `json:"checkouts"`
	Requests  int64   `json:"requests"`
	HoldP50   float64 `json:"holdP50"`
	HoldP99   float64 `json:"holdP99"`
	Sold      int     `json:"sold"`
	Held      int     `json:"held"`
	Waiting   int64   `json:"waiting"`
}

// Queue is the waiting room's state.
type Queue struct {
	Issued   int64 `json:"issued"`
	Admitted int64 `json:"admitted"`
	Waiting  int64 `json:"waiting"`
}

// Frame is one update sent to viewers and stored in the replay.
type Frame struct {
	T          int64              `json:"t"`
	Deltas     []int32            `json:"d,omitempty"`
	Counts     map[string]int64   `json:"c,omitempty"`
	Feed       []telemetry.Sample `json:"f,omitempty"`
	Point      *Point             `json:"p,omitempty"`
	Queue      *Queue             `json:"q,omitempty"`
	Invariants *store.Invariants  `json:"i,omitempty"`
	Chaos      []ChaosMark        `json:"x,omitempty"`
	Phase      string             `json:"phase,omitempty"`
}

// Layout is the venue geometry viewers draw.
type Layout struct {
	Seats    int             `json:"seats"`
	Width    int             `json:"width"`
	Height   int             `json:"height"`
	Sections []venue.Section `json:"sections"`
	Rows     []venue.Row     `json:"rows"`
}

// Snapshot is the full state sent when a viewer connects.
type Snapshot struct {
	Run        *store.Run        `json:"run"`
	Phase      string            `json:"phase"`
	T          int64             `json:"t"`
	Layout     *Layout           `json:"layout"`
	States     string            `json:"states"`
	Totals     map[string]int64  `json:"totals"`
	Series     []Point           `json:"series"`
	Chaos      []ChaosMark       `json:"chaos"`
	Invariants *store.Invariants `json:"invariants"`
	Queue      *Queue            `json:"queue"`
}

// Timeline is the stored replay of a run.
type Timeline struct {
	Version int               `json:"version"`
	Layout  *Layout           `json:"layout"`
	Mode    string            `json:"mode"`
	Frames  []Frame           `json:"frames"`
	Series  []Point           `json:"series"`
	Chaos   []ChaosMark       `json:"chaos"`
	Final   *store.Invariants `json:"final"`
}

type subscriber chan []byte

// Hub owns the live session.
type Hub struct {
	mu   sync.Mutex
	subs map[subscriber]struct{}

	run     *store.Run
	eventID string
	mode    string
	phase   string
	start   time.Time
	layout  *Layout
	board   *Board

	totals            map[string]int64
	window            map[string]int64
	second            map[string]int64
	holdHist, secHist []int64
	checkoutHist      []int64
	feed              []telemetry.Sample
	inv               *store.Invariants
	invDirty          bool
	chaos, newChaos   []ChaosMark
	queue             *Queue
	phaseDirty        bool
	lastPoint         time.Time

	frames []Frame
	series []Point
}

func NewHub() *Hub {
	return &Hub{subs: map[subscriber]struct{}{}, phase: PhaseIdle, totals: map[string]int64{}}
}

// Begin starts a new session for a run on a freshly reset venue.
func (h *Hub) Begin(run store.Run, eventID string, v *venue.Venue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := run
	h.run, h.eventID, h.mode = &r, eventID, run.Mode
	h.phase, h.phaseDirty = PhasePreparing, true
	h.start = time.Now()
	h.layout = &Layout{Seats: len(v.Seats), Width: v.Width, Height: v.Height, Sections: v.Sections, Rows: v.Rows}
	h.board = NewBoard(len(v.Seats))
	h.totals, h.window, h.second = map[string]int64{}, map[string]int64{}, map[string]int64{}
	h.holdHist, h.secHist, h.checkoutHist = telemetry.NewHistogram(), telemetry.NewHistogram(), telemetry.NewHistogram()
	h.feed, h.inv, h.chaos, h.newChaos, h.queue = nil, nil, nil, nil, nil
	h.frames, h.series = nil, nil
	h.lastPoint = h.start
	h.broadcastLocked("snapshot", h.snapshotLocked())
}

// SetPhase changes the run phase. Selling resets the clock so replays start
// at the moment the sale opens.
func (h *Hub) SetPhase(p string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.phase, h.phaseDirty = p, true
	if p == PhaseSelling {
		// Replays start the moment the sale opens.
		h.start = time.Now()
		h.lastPoint = h.start
		h.frames = nil
	}
}

func (h *Hub) Phase() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.phase
}

func (h *Hub) EventID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.eventID
}

func (h *Hub) Mode() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mode
}

func (h *Hub) active() bool {
	return h.board != nil && h.phase != PhaseIdle && h.phase != PhaseDone && h.phase != PhaseFailed
}

// ApplyFeed folds change-feed rows for the session's event into the board.
func (h *Hub) ApplyFeed(rows []store.FeedEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.board == nil {
		return
	}
	for _, r := range rows {
		if r.EventID == h.eventID {
			h.board.Apply(r)
		}
	}
}

// AddReport merges one API replica's telemetry window.
func (h *Hub) AddReport(r telemetry.Report) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active() || r.Event != h.eventID {
		return
	}
	if h.phase == PhasePreparing && len(r.Counts) > 0 {
		// First buyer activity: the sale is open.
		h.phase, h.phaseDirty = PhaseSelling, true
		h.start = time.Now()
		h.lastPoint = h.start
		h.frames = nil
	}
	for k, v := range r.Counts {
		h.totals[k] += v
		h.window[k] += v
		h.second[k] += v
	}
	telemetry.Merge(h.holdHist, r.Hold)
	telemetry.Merge(h.secHist, r.Hold)
	telemetry.Merge(h.checkoutHist, r.Checkout)
	for _, s := range r.Samples {
		if len(h.feed) < maxFeedPerFrame*4 {
			h.feed = append(h.feed, s)
		}
	}
}

func (h *Hub) SetInvariants(inv store.Invariants) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inv, h.invDirty = &inv, true
}

func (h *Hub) SetQueue(q Queue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queue = &q
}

func (h *Hub) MarkChaos(action, event, detail string) ChaosMark {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := ChaosMark{T: time.Since(h.start).Milliseconds(), Action: action, Event: event, Detail: detail}
	h.chaos = append(h.chaos, m)
	h.newChaos = append(h.newChaos, m)
	return m
}

// Tick builds and broadcasts the next frame and, once per second, a series point.
func (h *Hub) Tick(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.board == nil {
		return
	}
	f := Frame{T: now.Sub(h.start).Milliseconds(), Deltas: h.board.Drain()}
	if len(h.window) > 0 {
		f.Counts, h.window = h.window, map[string]int64{}
	}
	if len(h.feed) > 0 {
		n := min(len(h.feed), maxFeedPerFrame)
		f.Feed, h.feed = h.feed[:n], nil
	}
	if h.invDirty {
		f.Invariants, h.invDirty = h.inv, false
	}
	if len(h.newChaos) > 0 {
		f.Chaos, h.newChaos = h.newChaos, nil
	}
	if h.phaseDirty {
		f.Phase, h.phaseDirty = h.phase, false
	}
	if now.Sub(h.lastPoint) >= time.Second && h.phase != PhasePreparing {
		p := h.pointLocked(now)
		f.Point = &p
		h.series = append(h.series, p)
	}
	if h.queue != nil && h.active() {
		f.Queue = h.queue
	}
	if f.Deltas == nil && f.Counts == nil && f.Feed == nil && f.Invariants == nil && f.Chaos == nil && f.Phase == "" && f.Point == nil {
		return
	}
	if h.active() || f.Phase != "" {
		h.frames = append(h.frames, Frame{T: f.T, Deltas: f.Deltas, Chaos: f.Chaos, Invariants: f.Invariants, Phase: f.Phase, Point: f.Point})
	}
	h.broadcastLocked("frame", f)
}

func (h *Hub) pointLocked(now time.Time) Point {
	c := h.board.Counts()
	sec := h.second
	sum := func(prefix string) int64 {
		var n int64
		for k, v := range sec {
			if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
				n += v
			}
		}
		return n
	}
	var requests int64
	for _, v := range sec {
		requests += v
	}
	p := Point{T: now.Sub(h.start).Milliseconds(), Holds: sec["hold_201"], Conflicts: sec["hold_409_seat_unavailable"], SoldOut: sec["hold_409_sold_out"],
		Failures: sum("hold_503") + sum("hold_500") + sum("checkout_503") + sum("checkout_500"), Checkouts: sec["checkout_200"], Requests: requests,
		HoldP50: telemetry.Quantile(h.secHist, .5), HoldP99: telemetry.Quantile(h.secHist, .99), Sold: c["sold"], Held: c["held"]}
	if h.queue != nil {
		p.Waiting = h.queue.Waiting
	}
	h.second, h.secHist, h.lastPoint = map[string]int64{}, telemetry.NewHistogram(), now
	return p
}

func (h *Hub) snapshotLocked() Snapshot {
	s := Snapshot{Run: h.run, Phase: h.phase, Layout: h.layout, Totals: h.totals, Series: h.series, Chaos: h.chaos, Invariants: h.inv, Queue: h.queue}
	if h.board != nil {
		s.States = string(h.board.State)
		s.T = time.Since(h.start).Milliseconds()
	}
	if s.Series == nil {
		s.Series = []Point{}
	}
	if s.Chaos == nil {
		s.Chaos = []ChaosMark{}
	}
	return s
}

// Snapshot returns the current session state.
func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

// Summary returns final aggregates for the run record.
func (h *Hub) Summary() (board Board, totals map[string]int64, hold, checkout []int64, series []Point, chaos []ChaosMark) {
	h.mu.Lock()
	defer h.mu.Unlock()
	totals = map[string]int64{}
	for k, v := range h.totals {
		totals[k] = v
	}
	if h.board != nil {
		board = *h.board
	}
	return board, totals, append([]int64(nil), h.holdHist...), append([]int64(nil), h.checkoutHist...), append([]Point(nil), h.series...), append([]ChaosMark(nil), h.chaos...)
}

// SaleStart is when the sale opened.
func (h *Hub) SaleStart() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.start
}

// Timeline returns the compressed replay of the session.
func (h *Hub) Timeline(final *store.Invariants) ([]byte, error) {
	h.mu.Lock()
	t := Timeline{Version: 1, Layout: h.layout, Mode: h.mode, Frames: h.frames, Series: h.series, Chaos: h.chaos, Final: final}
	h.mu.Unlock()
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err := json.NewEncoder(zw).Encode(t); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Subscribe registers a viewer and returns its channel, primed with a snapshot.
func (h *Hub) Subscribe() (subscriber, func()) {
	ch := make(subscriber, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	ch <- encode("snapshot", h.snapshotLocked())
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

func encode(event string, v any) []byte {
	b, _ := json.Marshal(v)
	out := make([]byte, 0, len(b)+32)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, "\ndata: "...)
	out = append(out, b...)
	return append(out, "\n\n"...)
}

// broadcastLocked sends to every viewer. A viewer too slow to keep up is
// disconnected; its client reconnects and resynchronizes from a snapshot.
func (h *Hub) broadcastLocked(event string, v any) {
	if len(h.subs) == 0 {
		return
	}
	msg := encode(event, v)
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}
