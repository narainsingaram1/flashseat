package telemetry

import (
	"math"
	"testing"
)

func TestQuantileInterpolatesWithinBucket(t *testing.T) {
	h := NewHistogram()
	h[bucket(4)] = 100 // all samples in the (3, 5] bucket
	if got := Quantile(h, 0.5); math.Abs(got-4) > 0.01 {
		t.Fatalf("p50=%v", got)
	}
	if got := Quantile(h, 0.99); got <= 4 || got > 5 {
		t.Fatalf("p99=%v", got)
	}
	if Quantile(NewHistogram(), 0.99) != 0 {
		t.Fatal("empty histogram must report 0")
	}
}

func TestMergeAndOverflowBucket(t *testing.T) {
	a, b := NewHistogram(), NewHistogram()
	a[bucket(1)] = 2
	b[bucket(1)] = 3
	b[bucket(99999)] = 1
	Merge(a, b)
	if a[0] != 5 || a[len(a)-1] != 1 {
		t.Fatalf("merge=%v", a)
	}
}
