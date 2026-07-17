// Package rack generates letter racks for WordBreak.
//
// Two guarantees matter:
//  1. A rack is never unplayable — we regenerate until it contains at least a minimum
//     number of valid words (the "known-good rack" trick, so nobody gets "QXZJV").
//  2. The daily rack is deterministic from its date key, so every player worldwide gets
//     the exact same letters — the fairness property the paid pool depends on.
package rack

import (
	"hash/fnv"
	"math/rand"
	"strings"

	"github.com/wordbreak/backend/internal/dictionary"
)

// Letter bag weighted roughly to English/Scrabble frequency (consonants).
const consonantBag = "BBCCDDDDFFGGGHHJKLLLLMMNNNNNNPPRRRRRRSSSSTTTTTTVWWXYZ"

// Vowels weighted so A/E/O appear more than U.
const vowelBag = "AAAAEEEEEEIIIIOOOOUU"

// Result is a generated rack and the answer set the referee will score against.
type Result struct {
	Letters string   // the rack, uppercase (e.g. "AENRST")
	Words   []string // every valid word buildable from Letters (>= MinWordLen)
}

// minWords is the difficulty floor: a rack must yield at least this many words. Small racks
// (3–4 letters, the early levels) can't have many, so the floor scales with size.
func minWords(size int) int {
	switch {
	case size <= 3:
		return 2
	case size == 4:
		return 4
	case size == 5:
		return 6
	case size == 6:
		return 10
	case size == 7:
		return 14
	default:
		return 18
	}
}

// vowelCount targets a playable vowel/consonant balance for a given rack size.
func vowelCount(size int) int {
	v := size / 3
	if v < 2 {
		v = 2
	}
	if v > size-2 {
		v = size - 2
	}
	return v
}

// GenerateSolo returns a fresh random known-good rack of the given size.
func GenerateSolo(d *dictionary.Dictionary, size int) Result {
	return generate(d, size, rand.New(rand.NewSource(rand.Int63())))
}

// GenerateDaily returns the deterministic rack for a date key like "2026-07-16".
// Same key + same size + same dictionary => identical rack for everyone.
func GenerateDaily(d *dictionary.Dictionary, dateKey string, size int) Result {
	h := fnv.New64a()
	_, _ = h.Write([]byte(dateKey))
	return generate(d, size, rand.New(rand.NewSource(int64(h.Sum64()))))
}

// generate draws racks from rng until one clears the difficulty floor (bounded retries;
// the best candidate is returned if none clears, so this always terminates).
func generate(d *dictionary.Dictionary, size int, rng *rand.Rand) Result {
	const maxTries = 300
	floor := minWords(size)

	var best Result
	for i := 0; i < maxTries; i++ {
		letters := drawRack(size, rng)
		words := d.FindWords(letters)
		if len(words) > len(best.Words) {
			best = Result{Letters: letters, Words: words}
		}
		if len(words) >= floor {
			return Result{Letters: letters, Words: words}
		}
	}
	return best
}

// drawRack builds one candidate rack: a balanced set of vowels + consonants, shuffled.
func drawRack(size int, rng *rand.Rand) string {
	nv := vowelCount(size)
	b := make([]byte, 0, size)
	for i := 0; i < nv; i++ {
		b = append(b, vowelBag[rng.Intn(len(vowelBag))])
	}
	for i := nv; i < size; i++ {
		b = append(b, consonantBag[rng.Intn(len(consonantBag))])
	}
	rng.Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
	return strings.ToUpper(string(b))
}
