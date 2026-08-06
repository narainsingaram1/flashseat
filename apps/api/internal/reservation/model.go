package reservation

import (
	"errors"
	"sort"
	"time"
)

var (
	ErrConflict  = errors.New("one or more seats are unavailable")
	ErrExpired   = errors.New("hold has expired")
	ErrForbidden = errors.New("resource belongs to another session")
	ErrNotFound  = errors.New("resource not found")
)

type Seat struct {
	ID         string `json:"id"`
	Section    string `json:"section"`
	Row        string `json:"row"`
	State      string `json:"state"`
	Number     int    `json:"number"`
	PriceCents int    `json:"priceCents"`
	Accessible bool   `json:"accessible"`
	Mine       bool   `json:"mine,omitempty"`
}
type Hold struct {
	ID         string    `json:"id"`
	EventID    string    `json:"eventId"`
	UserID     string    `json:"-"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Seats      []Seat    `json:"seats"`
	TotalCents int       `json:"totalCents"`
}
type Order struct {
	ID         string    `json:"id"`
	Reference  string    `json:"reference"`
	HoldID     string    `json:"holdId"`
	UserID     string    `json:"-"`
	TotalCents int       `json:"totalCents"`
	CreatedAt  time.Time `json:"createdAt"`
	Seats      []Seat    `json:"seats,omitempty"`
}

func NormalizeSeatIDs(ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > 8 {
		return nil, errors.New("select between one and eight seats")
	}
	seen := make(map[string]bool, len(ids))
	out := append([]string(nil), ids...)
	for _, id := range out {
		if id == "" || seen[id] {
			return nil, errors.New("seat ids must be unique")
		}
		seen[id] = true
	}
	sort.Strings(out)
	return out, nil
}
func IsExpired(expiresAt, now time.Time) bool    { return !expiresAt.After(now) }
func PaymentApproved(idempotencyKey string) bool { return idempotencyKey != "decline-demo" }
