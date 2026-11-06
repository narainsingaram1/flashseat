package reservation

import (
	"errors"
	"time"
)

var (
	ErrConflict        = errors.New("one or more seats are unavailable")
	ErrSoldOut         = errors.New("no block of adjacent seats matches the request")
	ErrExpired         = errors.New("hold has expired")
	ErrForbidden       = errors.New("resource belongs to another session")
	ErrNotFound        = errors.New("resource not found")
	ErrHoldLimit       = errors.New("buyer already holds seats for this event")
	ErrSaleClosed      = errors.New("sale is not open")
	ErrInvalidSeats    = errors.New("invalid seat selection")
	ErrPaymentDeclined = errors.New("simulated payment declined")
)

type Seat struct {
	ID         string `json:"id"`
	Index      int    `json:"index"`
	Section    string `json:"section"`
	Row        string `json:"row"`
	Number     int    `json:"number"`
	PriceCents int    `json:"priceCents"`
	State      string `json:"state,omitempty"`
}

type Hold struct {
	ID         string    `json:"id"`
	EventID    string    `json:"eventId"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Seats      []Seat    `json:"seats"`
	TotalCents int       `json:"totalCents"`
}

type Order struct {
	ID         string    `json:"id"`
	Reference  string    `json:"reference"`
	HoldID     string    `json:"holdId"`
	TotalCents int       `json:"totalCents"`
	CreatedAt  time.Time `json:"createdAt"`
	Seats      []Seat    `json:"seats,omitempty"`
}

// MaxSeatsPerHold caps a single request, as real box offices do.
const MaxSeatsPerHold = 8

func IsExpired(expiresAt, now time.Time) bool { return !expiresAt.After(now) }
