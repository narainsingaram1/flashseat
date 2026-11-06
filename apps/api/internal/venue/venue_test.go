package venue

import (
	"math/rand/v2"
	"testing"
)

func build(t *testing.T, p Preset) *Venue {
	t.Helper()
	gen := Generate("test", p)
	tiers := map[string]SectionSpec{}
	for _, s := range p.Sections {
		tiers[s.ID] = s
	}
	layout := make([]LayoutSeat, len(gen))
	for i, g := range gen {
		spec := tiers[g.SectionID]
		layout[i] = LayoutSeat{Seat: g.Seat, SectionID: g.SectionID, Tier: spec.Tier, RowIndex: g.RowIndex, GridX: spec.GridX, GridY: spec.GridY}
	}
	v, err := Build(layout)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func set(bits []byte, i int) []byte {
	for len(bits) <= i>>3 {
		bits = append(bits, 0)
	}
	bits[i>>3] |= 0x80 >> (i & 7)
	return bits
}

func TestPresetsAreSymmetricAndSized(t *testing.T) {
	if got := Presets["arena"].Seats(); got != 9000 {
		t.Fatalf("arena seats = %d", got)
	}
	v := build(t, Presets["arena"])
	if len(v.Seats) != 9000 || v.Width != 158 {
		t.Fatalf("seats=%d width=%d", len(v.Seats), v.Width)
	}
	if v.Rows[v.ranked[0]].Section != "Floor" || v.Rows[v.ranked[0]].Label != "A" {
		t.Fatalf("best row = %+v", v.Rows[v.ranked[0]])
	}
}

func TestRowLabel(t *testing.T) {
	for in, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"} {
		if got := RowLabel(in); got != want {
			t.Fatalf("RowLabel(%d)=%s want %s", in, got, want)
		}
	}
}

func TestPickReturnsContiguousCenteredFrontRowBlock(t *testing.T) {
	v := build(t, Presets["club"])
	c := v.Candidates(nil, Request{Quantity: 4}, 8)
	if len(c) == 0 {
		t.Fatal("no candidates on an empty venue")
	}
	best := c[0].Indexes
	row := v.RowOf(best[0])
	if row.Section != "Floor" || row.Label != "A" {
		t.Fatalf("best block in %s %s", row.Section, row.Label)
	}
	for k := 1; k < len(best); k++ {
		if best[k] != best[k-1]+1 {
			t.Fatalf("block not contiguous: %v", best)
		}
	}
	// Floor row A has 30 seats; a centered block of 4 starts at seat 14.
	if v.Seats[best[0]].Number != 14 {
		t.Fatalf("block starts at seat %d", v.Seats[best[0]].Number)
	}
}

func TestPickSkipsTakenSeatsAndRespectsTier(t *testing.T) {
	v := build(t, Presets["club"])
	var bits []byte
	// Fill all of floor row A except two seats at the far left.
	row := v.Rows[0]
	for i := row.Start + 2; i < row.Start+row.Len; i++ {
		bits = set(bits, i)
	}
	c := v.Candidates(bits, Request{Quantity: 3}, 8)
	for _, cand := range c {
		for _, i := range cand.Indexes {
			if Taken(bits, i) {
				t.Fatalf("candidate uses taken seat %d", i)
			}
		}
		if v.RowOf(cand.Indexes[0]).Label == "A" && v.RowOf(cand.Indexes[0]).Section == "Floor" {
			t.Fatal("row A cannot fit 3 adjacent seats")
		}
	}
	tier2 := v.Candidates(bits, Request{Quantity: 2, Tier: 2}, 8)
	for _, cand := range tier2 {
		if v.RowOf(cand.Indexes[0]).Tier != 2 {
			t.Fatal("tier filter ignored")
		}
	}
}

func TestPickReportsSoldOut(t *testing.T) {
	v := build(t, Presets["club"])
	var bits []byte
	for i := range v.Seats {
		if i%2 == 0 {
			bits = set(bits, i)
		}
	}
	if _, ok := v.Pick(bits, Request{Quantity: 2}, 0.3); ok {
		t.Fatal("no two adjacent seats are free")
	}
	if _, ok := v.Pick(bits, Request{Quantity: 1}, 0.3); !ok {
		t.Fatal("single seats remain")
	}
	if v.FreeSeats(bits) != len(v.Seats)/2 {
		t.Fatalf("free=%d", v.FreeSeats(bits))
	}
}

func TestPickSpreadsConcurrentBuyers(t *testing.T) {
	v := build(t, Presets["arena"])
	rng := rand.New(rand.NewPCG(7, 9))
	seen := map[int]bool{}
	for range 200 {
		idx, ok := v.Pick(nil, Request{Quantity: 2}, rng.Float64())
		if !ok {
			t.Fatal("empty arena should allocate")
		}
		seen[idx[0]] = true
	}
	if len(seen) < 4 {
		t.Fatalf("picker herded buyers onto %d blocks", len(seen))
	}
}

func BenchmarkPickHalfFullArena(b *testing.B) {
	p := Presets["arena"]
	gen := Generate("bench", p)
	specs := map[string]SectionSpec{}
	for _, s := range p.Sections {
		specs[s.ID] = s
	}
	layout := make([]LayoutSeat, len(gen))
	for i, g := range gen {
		s := specs[g.SectionID]
		layout[i] = LayoutSeat{Seat: g.Seat, SectionID: g.SectionID, Tier: s.Tier, RowIndex: g.RowIndex, GridX: s.GridX, GridY: s.GridY}
	}
	v, _ := Build(layout)
	rng := rand.New(rand.NewPCG(3, 4))
	var bits []byte
	for i := 0; i < len(v.Seats); i++ {
		if rng.Float64() < 0.5 {
			bits = set(bits, i)
		}
	}
	b.ResetTimer()
	for b.Loop() {
		v.Pick(bits, Request{Quantity: 3}, rng.Float64())
	}
}
