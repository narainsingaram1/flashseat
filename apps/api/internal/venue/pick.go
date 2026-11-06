package venue

import (
	"math"
	"sort"
)

// Taken reports whether bit i is set in a Redis-ordered bitmap (most
// significant bit first). Bits past the end of the string are free.
func Taken(bits []byte, i int) bool {
	b := i >> 3
	return b < len(bits) && bits[b]&(0x80>>(i&7)) != 0
}

// Candidate is a contiguous block of free seats in one row.
type Candidate struct {
	Indexes []int
	Score   float64
}

// Request describes what a buyer asked for. Tier 0 means any tier.
type Request struct {
	Quantity int
	Tier     int
}

const candidatePool = 8

// Pick returns a contiguous block of free seats for the request, or false when
// no row in the requested tier has enough adjacent free seats.
//
// It gathers the best few blocks across the venue and chooses among them with
// a bias toward the best. Choosing the single best block would send every
// concurrent buyer to the same seats; spreading across near-equivalent blocks
// keeps them from colliding in Redis and retrying. u is a uniform random
// number in [0, 1).
func (v *Venue) Pick(bits []byte, req Request, u float64) ([]int, bool) {
	cands := v.Candidates(bits, req, candidatePool)
	if len(cands) == 0 {
		return nil, false
	}
	return cands[int(u*u*float64(len(cands)))].Indexes, true
}

// Candidates returns up to limit best blocks, best first.
func (v *Venue) Candidates(bits []byte, req Request, limit int) []Candidate {
	q := req.Quantity
	if q < 1 {
		return nil
	}
	mid := float64(v.Width) / 2
	var out []Candidate
	worst := math.Inf(1)
	for _, ri := range v.ranked {
		r := v.Rows[ri]
		if req.Tier != 0 && r.Tier != req.Tier {
			continue
		}
		floor := float64(r.Tier)*10000 + float64(r.Y)*10
		if len(out) >= limit && floor > worst {
			break
		}
		if r.Len < q {
			continue
		}
		end := r.Start + r.Len
		for i := r.Start; i < end; {
			if Taken(bits, i) {
				i++
				continue
			}
			runStart := i
			for i < end && !Taken(bits, i) {
				i++
			}
			if i-runStart < q {
				continue
			}
			ideal := r.Start + int(math.Round(mid-float64(r.X)-float64(q)/2))
			s := min(max(ideal, runStart), i-q)
			center := float64(r.X) + float64(s-r.Start) + float64(q)/2
			score := floor + math.Abs(center-mid)*0.5
			idx := make([]int, q)
			for k := range idx {
				idx[k] = s + k
			}
			out = append(out, Candidate{Indexes: idx, Score: score})
		}
		if len(out) > limit {
			sort.Slice(out, func(a, b int) bool { return out[a].Score < out[b].Score })
			out = out[:limit]
		}
		if len(out) >= limit {
			worst = out[len(out)-1].Score
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Score < out[b].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FreeSeats counts unset bits across the venue.
func (v *Venue) FreeSeats(bits []byte) int {
	free := 0
	for i := range v.Seats {
		if !Taken(bits, i) {
			free++
		}
	}
	return free
}
