// Package game scores a player's submitted words for a rack. Scoring is server-authoritative:
// this is the referee's judgment, and its output is what ultimately drives on-chain payouts,
// so validation is strict and every rejection carries a reason.
package game

import (
	"strings"

	"github.com/wordbreak/backend/internal/dictionary"
)

// Reasons a submitted word is rejected.
const (
	ReasonTooShort  = "too_short"
	ReasonNotInRack = "not_in_rack"
	ReasonNotAWord  = "not_a_word"
	ReasonDuplicate = "duplicate"
)

// WordScore is one accepted word and the points it earned.
type WordScore struct {
	Word   string `json:"word"`
	Points int    `json:"points"`
}

// Rejection is one rejected word and why.
type Rejection struct {
	Word   string `json:"word"`
	Reason string `json:"reason"`
}

// Result is the full scored submission.
type Result struct {
	Total    int         `json:"total"`
	Accepted []WordScore `json:"accepted"`
	Rejected []Rejection `json:"rejected"`
}

// Score validates words against the rack + dictionary and totals the points. Duplicate
// submissions (case-insensitive) score once; the extras are reported as rejected.
func Score(d *dictionary.Dictionary, rackLetters string, words []string) Result {
	have := letterCounts(rackLetters)
	seen := make(map[string]struct{}, len(words))
	res := Result{Accepted: make([]WordScore, 0, len(words)), Rejected: make([]Rejection, 0)}

	for _, raw := range words {
		w := strings.ToUpper(strings.TrimSpace(raw))
		if w == "" {
			continue
		}
		if _, dup := seen[w]; dup {
			res.Rejected = append(res.Rejected, Rejection{Word: w, Reason: ReasonDuplicate})
			continue
		}
		seen[w] = struct{}{}

		if len(w) < dictionary.MinWordLen {
			res.Rejected = append(res.Rejected, Rejection{Word: w, Reason: ReasonTooShort})
			continue
		}
		if !fitsRack(w, have) {
			res.Rejected = append(res.Rejected, Rejection{Word: w, Reason: ReasonNotInRack})
			continue
		}
		if !d.Contains(w) {
			res.Rejected = append(res.Rejected, Rejection{Word: w, Reason: ReasonNotAWord})
			continue
		}

		p := WordPoints(len(w))
		res.Accepted = append(res.Accepted, WordScore{Word: w, Points: p})
		res.Total += p
	}
	return res
}

// WordPoints rewards longer words on a rising curve.
func WordPoints(n int) int {
	switch {
	case n < 3:
		return 0
	case n == 3:
		return 1
	case n == 4:
		return 2
	case n == 5:
		return 4
	case n == 6:
		return 6
	case n == 7:
		return 10
	default:
		return 10 + (n-7)*4 // 8->14, 9->18, ...
	}
}

func letterCounts(s string) [26]int {
	var c [26]int
	for _, r := range strings.ToUpper(s) {
		if r >= 'A' && r <= 'Z' {
			c[r-'A']++
		}
	}
	return c
}

func fitsRack(word string, have [26]int) bool {
	var need [26]int
	for _, r := range word {
		if r < 'A' || r > 'Z' {
			return false
		}
		need[r-'A']++
		if need[r-'A'] > have[r-'A'] {
			return false
		}
	}
	return true
}
