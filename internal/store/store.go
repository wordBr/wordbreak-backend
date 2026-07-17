// Package store is the off-chain state for WordBreak: daily rounds and their leaderboards.
// It's an in-memory, concurrency-safe implementation for the MVP — the interface is small
// on purpose so it can be swapped for Postgres without touching the API layer.
package store

import (
	"sort"
	"sync"
	"time"
)

// Submission is a player's best result for a daily round.
type Submission struct {
	Address string    `json:"address"`
	Score   int       `json:"score"`
	Words   int       `json:"words"`
	At      time.Time `json:"at"`
}

// Daily is one day's shared round.
type Daily struct {
	DateKey string `json:"dateKey"`
	Letters string `json:"letters"`

	// Paid-round fields, set once when the operator opens the on-chain round.
	Paid    bool      `json:"paid"`
	RoundID string    `json:"roundId"` // decimal string, matches the on-chain roundId
	EndTime time.Time `json:"endTime"` // entry/submission cutoff

	mu          sync.RWMutex
	submissions map[string]Submission // keyed by lowercased address, best score kept
}

// LeaderboardEntry is one ranked row.
type LeaderboardEntry struct {
	Rank    int    `json:"rank"`
	Address string `json:"address"`
	Score   int    `json:"score"`
	Words   int    `json:"words"`
}

// Store holds all daily rounds.
type Store struct {
	mu      sync.Mutex
	dailies map[string]*Daily
}

// New creates an empty Store.
func New() *Store {
	return &Store{dailies: make(map[string]*Daily)}
}

// GetOrCreateDaily returns the round for dateKey, creating it (with letters from gen) if new.
// gen is only called once per date, under lock, so the daily rack is fixed for everyone.
func (s *Store) GetOrCreateDaily(dateKey string, gen func() string) *Daily {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.dailies[dateKey]; ok {
		return d
	}
	d := &Daily{DateKey: dateKey, Letters: gen(), submissions: make(map[string]Submission)}
	s.dailies[dateKey] = d
	return d
}

// OpenPaidDaily upserts the round for dateKey and marks it as a paid on-chain round.
// Letters stay deterministic (generated once); existing submissions are preserved.
func (s *Store) OpenPaidDaily(dateKey, roundID string, endTime time.Time, letters string) *Daily {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.dailies[dateKey]
	if !ok {
		d = &Daily{DateKey: dateKey, Letters: letters, submissions: make(map[string]Submission)}
		s.dailies[dateKey] = d
	}
	d.Paid = true
	d.RoundID = roundID
	d.EndTime = endTime
	return d
}

// Submit records a player's result, keeping only their best score for the round.
func (d *Daily) Submit(sub Submission) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := lower(sub.Address)
	if prev, ok := d.submissions[key]; ok && prev.Score >= sub.Score {
		return
	}
	d.submissions[key] = sub
}

// Leaderboard returns rows sorted by score desc, then earliest submission first.
func (d *Daily) Leaderboard() []LeaderboardEntry {
	d.mu.RLock()
	subs := make([]Submission, 0, len(d.submissions))
	for _, s := range d.submissions {
		subs = append(subs, s)
	}
	d.mu.RUnlock()

	sort.Slice(subs, func(i, j int) bool {
		if subs[i].Score != subs[j].Score {
			return subs[i].Score > subs[j].Score
		}
		return subs[i].At.Before(subs[j].At)
	})

	out := make([]LeaderboardEntry, len(subs))
	for i, s := range subs {
		out[i] = LeaderboardEntry{Rank: i + 1, Address: s.Address, Score: s.Score, Words: s.Words}
	}
	return out
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
