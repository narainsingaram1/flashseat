package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/admission"
	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/flashseat/flashseat/apps/api/internal/telemetry"
	"github.com/go-chi/chi/v5"
)

// passLifetime is how long an admitted buyer may keep shopping.
const passLifetime = 10 * time.Minute

// maxLongPoll caps how long a queue status request may wait for admission.
const maxLongPoll = 10 * time.Second

type queueView struct {
	Ticket   int64      `json:"ticket"`
	Sig      string     `json:"sig"`
	Position int64      `json:"position"`
	Admitted bool       `json:"admitted"`
	AdmitAt  *time.Time `json:"admitAt,omitempty"`
	Pass     string     `json:"pass,omitempty"`
}

func gate(m store.EventMeta) (admission.Gate, bool) {
	if m.QueueRate <= 0 {
		return admission.Gate{}, true
	}
	if m.QueueOpenedAt == nil {
		return admission.Gate{Rate: m.QueueRate}, false
	}
	return admission.Gate{Rate: m.QueueRate, OpenedAt: *m.QueueOpenedAt}, true
}

func (a *API) queueView(m store.EventMeta, user string, ticket int64, now time.Time) queueView {
	v := queueView{Ticket: ticket, Sig: a.signer.TicketSig(m.ID, m.Version, user, ticket)}
	g, open := gate(m)
	if !open {
		v.Position = ticket
		return v
	}
	admitted := g.Admitted(now)
	if ticket <= admitted {
		v.Admitted = true
		v.Pass = a.signer.Pass(m.ID, m.Version, user, now.Add(passLifetime))
		return v
	}
	v.Position = ticket - admitted
	at := g.AdmitAt(ticket)
	v.AdmitAt = &at
	return v
}

// joinQueue issues the next waiting-room ticket.
func (a *API) joinQueue(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	m, err := a.events.meta(r.Context(), chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if m.QueueRate > 0 && m.QueueOpenedAt == nil {
		// The sale opens when the first buyer arrives. Every replica sees the
		// same opening time because the first UPDATE wins.
		at, err := a.store.OpenQueue(r.Context(), m.ID)
		if err != nil {
			problem(w, 503, "queue_unavailable", "the waiting room is unavailable")
			return
		}
		m.QueueOpenedAt = &at
		a.events.forget(m.ID)
	}
	n, err := a.store.NextTicket(r.Context())
	if err != nil {
		problem(w, 503, "queue_unavailable", "the waiting room is unavailable")
		return
	}
	v := a.queueView(m, user, max(1, n-m.QueueBase), time.Now())
	a.tel.Record(m.ID, "queue_join", "queue", time.Since(start), telemetry.Sample{Actor: actor(r), Kind: "queue", Status: 201, Detail: "joined queue at #" + strconv.FormatInt(v.Ticket, 10)})
	jsonOut(w, 201, v)
}

// queueStatus reports a ticket's position. With ?wait=N it long-polls up to N
// seconds: admission time is known in advance, so the server sleeps until
// then instead of the client polling.
func (a *API) queueStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	ticket, err := strconv.ParseInt(chi.URLParam(r, "ticket"), 10, 64)
	if err != nil || ticket < 1 {
		problem(w, 422, "invalid_ticket", "ticket must be a positive integer")
		return
	}
	m, err := a.events.meta(r.Context(), chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !a.signer.ValidTicket(m.ID, m.Version, user, ticket, r.URL.Query().Get("sig")) {
		problem(w, 403, "invalid_ticket", "ticket does not belong to this session")
		return
	}
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	deadline := time.Now().Add(min(time.Duration(max(wait, 0))*time.Second, maxLongPoll))
	for {
		now := time.Now()
		v := a.queueView(m, user, ticket, now)
		if v.Admitted || !now.Before(deadline) {
			if v.Admitted {
				a.tel.Record(m.ID, "queue_admit", "queue", 0, telemetry.Sample{Actor: actor(r), Kind: "queue", Status: 200, Detail: "admitted from #" + strconv.FormatInt(ticket, 10)})
			}
			jsonOut(w, 200, v)
			return
		}
		sleep := time.Until(deadline)
		if v.AdmitAt != nil {
			sleep = min(sleep, time.Until(*v.AdmitAt)+5*time.Millisecond)
		} else {
			sleep = min(sleep, 250*time.Millisecond) // queue not open yet
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(max(sleep, 5*time.Millisecond)):
		}
		if m2, err := a.events.meta(r.Context(), m.ID); err == nil && m2.Version == m.Version {
			m = m2
		}
	}
}
