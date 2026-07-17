// Package dictionary loads a word list and answers "is this a valid word?" plus
// "which words can be built from these letters?" — the linguistic core the referee
// uses to score rounds. Lookups are O(1) map hits; word-finding is bounded by rack size.
package dictionary

import (
	"bufio"
	"bytes"
	_ "embed"
	"strings"
)

// MinWordLen is the shortest accepted word. 2-letter words in the raw list are mostly
// junk ("aa", "ab"), so we start at 3 to keep the game feeling fair.
const MinWordLen = 3

//go:embed data/words_alpha.txt
var embeddedWords []byte

// Dictionary is an immutable set of valid words, keyed uppercase.
type Dictionary struct {
	words map[string]struct{}
}

// New builds a Dictionary from the embedded English word list.
func New() *Dictionary {
	return newFromReader(bytes.NewReader(embeddedWords))
}

func newFromReader(r *bytes.Reader) *Dictionary {
	d := &Dictionary{words: make(map[string]struct{}, 300_000)}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		w := strings.ToUpper(strings.TrimSpace(sc.Text()))
		if len(w) < MinWordLen || !isAlpha(w) {
			continue
		}
		d.words[w] = struct{}{}
	}
	return d
}

// NewFromWords builds a Dictionary from an explicit slice (used in tests).
func NewFromWords(words []string) *Dictionary {
	d := &Dictionary{words: make(map[string]struct{}, len(words))}
	for _, w := range words {
		w = strings.ToUpper(strings.TrimSpace(w))
		if len(w) >= MinWordLen && isAlpha(w) {
			d.words[w] = struct{}{}
		}
	}
	return d
}

// Size reports how many words are loaded.
func (d *Dictionary) Size() int { return len(d.words) }

// Contains reports whether word (case-insensitive) is in the dictionary.
func (d *Dictionary) Contains(word string) bool {
	_, ok := d.words[strings.ToUpper(word)]
	return ok
}

// FindWords returns every dictionary word (>= MinWordLen) that can be spelled from the
// multiset of letters in rack. Used to size a rack's difficulty and to reveal answers.
func (d *Dictionary) FindWords(rack string) []string {
	have := letterCounts(rack)
	out := make([]string, 0, 64)
	for w := range d.words {
		if canBuild(w, have) {
			out = append(out, w)
		}
	}
	return out
}

// letterCounts returns a 26-slot count of A–Z in s (non-letters ignored).
func letterCounts(s string) [26]int {
	var c [26]int
	for _, r := range strings.ToUpper(s) {
		if r >= 'A' && r <= 'Z' {
			c[r-'A']++
		}
	}
	return c
}

// canBuild reports whether word fits within the available letter multiset.
func canBuild(word string, have [26]int) bool {
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

func isAlpha(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
