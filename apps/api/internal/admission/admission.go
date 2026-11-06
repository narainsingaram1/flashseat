// Package admission implements the virtual waiting room. Tickets are numbered
// in arrival order; admission advances at a fixed rate from the moment the
// queue opens, so whether ticket N is admitted is a pure function of time.
// Every API replica agrees without coordination, and tokens are HMAC-signed
// so admission can be verified without a lookup.
package admission

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Gate is one event's queue configuration.
type Gate struct {
	Rate     int       // buyers admitted per second; 0 disables the queue
	OpenedAt time.Time // when admission started
}

// Admitted returns how many tickets have been admitted by now.
func (g Gate) Admitted(now time.Time) int64 {
	if g.Rate <= 0 {
		return math.MaxInt64
	}
	elapsed := now.Sub(g.OpenedAt).Seconds()
	if elapsed < 0 {
		return 0
	}
	return int64(elapsed * float64(g.Rate))
}

// AdmitAt is when a ticket is admitted. Ticket numbers start at 1.
func (g Gate) AdmitAt(ticket int64) time.Time {
	if g.Rate <= 0 {
		return g.OpenedAt
	}
	return g.OpenedAt.Add(time.Duration(float64(ticket) / float64(g.Rate) * float64(time.Second)))
}

// Signer issues and verifies tokens bound to an event inventory version and buyer.
type Signer struct{ key []byte }

func NewSigner(secret string) Signer { return Signer{key: []byte(secret)} }

func (s Signer) mac(parts ...string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(strings.Join(parts, "|")))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:18])
}

// TicketSig proves a buyer owns a ticket number.
func (s Signer) TicketSig(event string, version int64, user string, ticket int64) string {
	return s.mac("ticket", event, strconv.FormatInt(version, 10), user, strconv.FormatInt(ticket, 10))
}

func (s Signer) ValidTicket(event string, version int64, user string, ticket int64, sig string) bool {
	return hmac.Equal([]byte(sig), []byte(s.TicketSig(event, version, user, ticket)))
}

// Pass is an admission token: "<expiryUnix>.<mac>".
func (s Signer) Pass(event string, version int64, user string, until time.Time) string {
	exp := strconv.FormatInt(until.Unix(), 10)
	return exp + "." + s.mac("pass", event, strconv.FormatInt(version, 10), user, exp)
}

func (s Signer) ValidPass(event string, version int64, user, token string, now time.Time) error {
	exp, sig, ok := strings.Cut(token, ".")
	if !ok {
		return fmt.Errorf("malformed admission token")
	}
	until, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return fmt.Errorf("malformed admission token")
	}
	if !hmac.Equal([]byte(sig), []byte(s.mac("pass", event, strconv.FormatInt(version, 10), user, exp))) {
		return fmt.Errorf("invalid admission token")
	}
	if now.Unix() > until {
		return fmt.Errorf("admission token expired")
	}
	return nil
}
