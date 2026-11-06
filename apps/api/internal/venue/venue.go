// Package venue describes seating layouts and allocates best-available seats
// against an inventory bitmap.
package venue

import (
	"fmt"
	"math"
	"sort"
)

// SectionSpec is one rectangular block of seats in a preset layout. GridX and
// GridY place the block on the venue map in seat units (stage at the top).
type SectionSpec struct {
	ID, Name          string
	Tier              int
	Rows, SeatsPerRow int
	GridX, GridY      int
	PriceCents        int
}

type Preset struct {
	ID, Name string
	Sections []SectionSpec
}

func (p Preset) Seats() int {
	n := 0
	for _, s := range p.Sections {
		n += s.Rows * s.SeatsPerRow
	}
	return n
}

// Presets are the venues the simulator can build.
var Presets = map[string]Preset{
	"club": {ID: "club", Name: "The Basement Club", Sections: []SectionSpec{
		{ID: "floor", Name: "Floor", Tier: 1, Rows: 15, SeatsPerRow: 30, GridX: 22, GridY: 0, PriceCents: 12000},
		{ID: "balcony-l", Name: "Balcony Left", Tier: 2, Rows: 10, SeatsPerRow: 24, GridX: 0, GridY: 19, PriceCents: 7500},
		{ID: "balcony-c", Name: "Balcony Center", Tier: 2, Rows: 10, SeatsPerRow: 24, GridX: 25, GridY: 19, PriceCents: 8500},
		{ID: "balcony-r", Name: "Balcony Right", Tier: 2, Rows: 10, SeatsPerRow: 24, GridX: 50, GridY: 19, PriceCents: 7500},
	}},
	"arena": {ID: "arena", Name: "Meridian Arena", Sections: []SectionSpec{
		{ID: "floor", Name: "Floor", Tier: 1, Rows: 25, SeatsPerRow: 48, GridX: 55, GridY: 0, PriceCents: 25000},
		{ID: "lower-l", Name: "Lower Left", Tier: 2, Rows: 25, SeatsPerRow: 40, GridX: 8, GridY: 29, PriceCents: 15000},
		{ID: "lower-c", Name: "Lower Center", Tier: 2, Rows: 25, SeatsPerRow: 40, GridX: 59, GridY: 29, PriceCents: 17500},
		{ID: "lower-r", Name: "Lower Right", Tier: 2, Rows: 25, SeatsPerRow: 40, GridX: 110, GridY: 29, PriceCents: 15000},
		{ID: "upper-l", Name: "Upper Left", Tier: 3, Rows: 32, SeatsPerRow: 50, GridX: 0, GridY: 58, PriceCents: 8000},
		{ID: "upper-c", Name: "Upper Center", Tier: 3, Rows: 32, SeatsPerRow: 50, GridX: 54, GridY: 58, PriceCents: 9500},
		{ID: "upper-r", Name: "Upper Right", Tier: 3, Rows: 32, SeatsPerRow: 50, GridX: 108, GridY: 58, PriceCents: 8000},
	}},
}

// Seat is one sellable seat. Index is its bit position in the inventory bitmap.
type Seat struct {
	ID         string `json:"id"`
	Index      int    `json:"index"`
	Section    string `json:"section"`
	Row        string `json:"row"`
	Number     int    `json:"number"`
	PriceCents int    `json:"priceCents"`
}

// Row is a contiguous run of bitmap indexes [Start, Start+Len).
type Row struct {
	Section string  `json:"section"`
	Tier    int     `json:"tier"`
	Label   string  `json:"label"`
	Start   int     `json:"start"`
	Len     int     `json:"len"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
	quality float64 // lower is better
	centerX float64
}

type Section struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Tier  int    `json:"tier"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	Rows  int    `json:"rows"`
	Width int    `json:"width"`
}

// Venue is the immutable, indexed layout of one event's inventory.
type Venue struct {
	Seats    []Seat    `json:"-"`
	Rows     []Row     `json:"rows"`
	Sections []Section `json:"sections"`
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	byID     map[string]int
	ranked   []int // row positions ordered best-first
}

// RowLabel converts a zero-based row number into spreadsheet-style letters.
func RowLabel(i int) string {
	label := ""
	for i >= 0 {
		label = string(rune('A'+i%26)) + label
		i = i/26 - 1
	}
	return label
}

// SeatID is the stable identifier of a generated seat.
func SeatID(eventID, sectionID string, row, number int) string {
	return fmt.Sprintf("%s-%s-%s%d", eventID, sectionID, RowLabel(row), number)
}

// GeneratedSeat is a seat row ready for insertion when building a venue.
type GeneratedSeat struct {
	Seat
	SectionID string
	RowIndex  int
}

// Generate expands a preset into seats in bitmap order: sections in order,
// rows front to back, seats left to right.
func Generate(eventID string, p Preset) []GeneratedSeat {
	out := make([]GeneratedSeat, 0, p.Seats())
	idx := 0
	for _, sec := range p.Sections {
		for r := 0; r < sec.Rows; r++ {
			for n := 1; n <= sec.SeatsPerRow; n++ {
				out = append(out, GeneratedSeat{
					Seat:      Seat{ID: SeatID(eventID, sec.ID, r, n), Index: idx, Section: sec.Name, Row: RowLabel(r), Number: n, PriceCents: sec.PriceCents},
					SectionID: sec.ID, RowIndex: r,
				})
				idx++
			}
		}
	}
	return out
}

// LayoutSeat is the minimal per-seat record needed to rebuild a Venue.
type LayoutSeat struct {
	Seat
	SectionID string
	Tier      int
	RowIndex  int
	GridX     int
	GridY     int
}

// Build indexes seats (which must be sorted by Index with no gaps) into rows
// and ranks rows by desirability: lower tier first, then nearer the stage, then
// nearer the venue's center line.
func Build(seats []LayoutSeat) (*Venue, error) {
	v := &Venue{Seats: make([]Seat, len(seats)), byID: make(map[string]int, len(seats))}
	sectionAt := map[string]int{}
	for i, s := range seats {
		if s.Index != i {
			return nil, fmt.Errorf("seat %s has index %d at position %d", s.ID, s.Index, i)
		}
		v.Seats[i] = s.Seat
		v.byID[s.ID] = i
		pos, ok := sectionAt[s.SectionID]
		if !ok {
			pos = len(v.Sections)
			sectionAt[s.SectionID] = pos
			v.Sections = append(v.Sections, Section{ID: s.SectionID, Name: s.Section, Tier: s.Tier, X: s.GridX, Y: s.GridY})
		}
		sec := &v.Sections[pos]
		sec.Rows = max(sec.Rows, s.RowIndex+1)
		sec.Width = max(sec.Width, s.Number)
		n := len(v.Rows)
		if n == 0 || v.Rows[n-1].Section != s.Section || v.Rows[n-1].Label != s.Row {
			v.Rows = append(v.Rows, Row{Section: s.Section, Tier: s.Tier, Label: s.Row, Start: i, X: s.GridX, Y: s.GridY + s.RowIndex})
		}
		v.Rows[len(v.Rows)-1].Len++
	}
	for _, sec := range v.Sections {
		v.Width = max(v.Width, sec.X+sec.Width)
		v.Height = max(v.Height, sec.Y+sec.Rows)
	}
	mid := float64(v.Width) / 2
	for i := range v.Rows {
		r := &v.Rows[i]
		r.centerX = float64(r.X) + float64(r.Len)/2
		depth := float64(r.Y)
		r.quality = float64(r.Tier)*10000 + depth*10 + math.Abs(r.centerX-mid)*0.5
	}
	v.ranked = make([]int, len(v.Rows))
	for i := range v.ranked {
		v.ranked[i] = i
	}
	sort.SliceStable(v.ranked, func(a, b int) bool { return v.Rows[v.ranked[a]].quality < v.Rows[v.ranked[b]].quality })
	return v, nil
}

// Index returns the bitmap position of a seat id.
func (v *Venue) Index(id string) (int, bool) {
	i, ok := v.byID[id]
	return i, ok
}

// RowOf returns the row containing a seat index.
func (v *Venue) RowOf(index int) Row {
	i := sort.Search(len(v.Rows), func(i int) bool { return v.Rows[i].Start+v.Rows[i].Len > index })
	if i < len(v.Rows) {
		return v.Rows[i]
	}
	return Row{}
}
