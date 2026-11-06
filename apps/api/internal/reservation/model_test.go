package reservation

import (
	"testing"
	"time"
)

func TestExpirationBoundary(t *testing.T) {
	now := time.Now()
	if !IsExpired(now, now) || IsExpired(now.Add(time.Second), now) {
		t.Fatal("a hold expires exactly at expires_at")
	}
}
