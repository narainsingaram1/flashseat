package admission

import (
	"testing"
	"time"
)

func TestGateAdmitsAtFixedRate(t *testing.T) {
	open := time.Unix(1000, 0)
	g := Gate{Rate: 200, OpenedAt: open}
	if g.Admitted(open.Add(-time.Second)) != 0 {
		t.Fatal("nothing is admitted before the queue opens")
	}
	if got := g.Admitted(open.Add(2500 * time.Millisecond)); got != 500 {
		t.Fatalf("admitted=%d", got)
	}
	if at := g.AdmitAt(500); !at.Equal(open.Add(2500 * time.Millisecond)) {
		t.Fatalf("ticket 500 admitted at %v", at)
	}
	if (Gate{}).Admitted(open) < 1<<40 {
		t.Fatal("a disabled queue admits everyone")
	}
}

func TestTokensAreBoundToBuyerEventAndVersion(t *testing.T) {
	s := NewSigner("secret")
	sig := s.TicketSig("arena", 3, "u1", 42)
	if !s.ValidTicket("arena", 3, "u1", 42, sig) {
		t.Fatal("valid ticket rejected")
	}
	for _, bad := range []bool{
		s.ValidTicket("arena", 3, "u2", 42, sig),
		s.ValidTicket("arena", 4, "u1", 42, sig),
		s.ValidTicket("arena", 3, "u1", 41, sig),
	} {
		if bad {
			t.Fatal("ticket signature reused across buyer, version, or number")
		}
	}
	now := time.Unix(5000, 0)
	pass := s.Pass("arena", 3, "u1", now.Add(time.Minute))
	if err := s.ValidPass("arena", 3, "u1", pass, now); err != nil {
		t.Fatal(err)
	}
	if s.ValidPass("arena", 3, "u2", pass, now) == nil {
		t.Fatal("pass accepted for another buyer")
	}
	if s.ValidPass("arena", 3, "u1", pass, now.Add(2*time.Minute)) == nil {
		t.Fatal("expired pass accepted")
	}
	if NewSigner("other").ValidPass("arena", 3, "u1", pass, now) == nil {
		t.Fatal("pass accepted under another key")
	}
}
