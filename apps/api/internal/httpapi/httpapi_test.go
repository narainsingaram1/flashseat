package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/admission"
	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/store"
)

func TestActorLabelIsSanitized(t *testing.T) {
	for in, want := range map[string]string{"#4821": "#4821", "": "web", "<script>": "web", "a b": "web", "buyer-12": "buyer-12"} {
		r := httptest.NewRequest("POST", "/", nil)
		if in != "" {
			r.Header.Set("X-Buyer", in)
		}
		if got := actor(r); got != want {
			t.Fatalf("actor(%q)=%q want %q", in, got, want)
		}
	}
}

func TestErrorsMapToStableStatuses(t *testing.T) {
	cases := map[error]int{
		reservation.ErrConflict: 409, reservation.ErrHoldLimit: 409, reservation.ErrExpired: 410,
		reservation.ErrForbidden: 403, reservation.ErrNotFound: 404, reservation.ErrSaleClosed: 423,
		reservation.ErrPaymentDeclined: 402, context.DeadlineExceeded: 503, errors.New("boom"): 500,
	}
	for err, want := range cases {
		if got := statusOf(err); got != want {
			t.Fatalf("statusOf(%v)=%d want %d", err, got, want)
		}
	}
	if isDomainErr(context.DeadlineExceeded) {
		t.Fatal("a timeout is ambiguous and must not release the Redis claim")
	}
	if !isDomainErr(reservation.ErrConflict) {
		t.Fatal("a definite conflict must release the Redis claim")
	}
}

func TestQueueViewAdmitsByTicketOrder(t *testing.T) {
	a := &API{signer: admission.NewSigner("k")}
	open := time.Unix(1000, 0)
	m := store.EventMeta{ID: "arena", Version: 4, QueueRate: 100, QueueOpenedAt: &open}
	now := open.Add(time.Second)
	if v := a.queueView(m, "u", 100, now); !v.Admitted || v.Pass == "" {
		t.Fatalf("ticket 100 should be admitted after 1 s at 100/s: %+v", v)
	}
	v := a.queueView(m, "u", 350, now)
	if v.Admitted || v.Position != 250 || v.AdmitAt == nil || !v.AdmitAt.Equal(open.Add(3500*time.Millisecond)) {
		t.Fatalf("ticket 350: %+v", v)
	}
	closed := store.EventMeta{ID: "arena", Version: 4, QueueRate: 100}
	if v := a.queueView(closed, "u", 1, now); v.Admitted || v.Position != 1 {
		t.Fatalf("queue not open yet: %+v", v)
	}
}
