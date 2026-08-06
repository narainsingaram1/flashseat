package reservation

import (
	"reflect"
	"testing"
	"time"
)

func TestNormalizeSeatIDs(t *testing.T) {
	got, err := NormalizeSeatIDs([]string{"b", "a"})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := NormalizeSeatIDs([]string{"a", "a"}); err == nil {
		t.Fatal("expected duplicate error")
	}
	if _, err := NormalizeSeatIDs(make([]string, 9)); err == nil {
		t.Fatal("expected size error")
	}
}
func TestExpirationBoundary(t *testing.T) {
	now := time.Now()
	if !IsExpired(now, now) || IsExpired(now.Add(time.Second), now) {
		t.Fatal("unexpected boundary result")
	}
}
func TestDeterministicPayment(t *testing.T) {
	if PaymentApproved("decline-demo") || !PaymentApproved("normal") {
		t.Fatal("payment fixture changed")
	}
}
