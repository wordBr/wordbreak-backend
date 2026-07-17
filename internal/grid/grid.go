package grid

import (
	"hash/fnv"
	"math/rand"
	"strings"
)

// Letter bags — same weighting philosophy as internal/rack, but grids run a richer vowel
// mix: dense adjacency boards with too few vowels barely connect into any words at all.
const consonantBag = "BBCCDDDDFFGGGHHJKLLLLMMNNNNNNPPRRRRRRSSSSTTTTTTVWWXYZ"
const vowelBag = "AAAAAEEEEEEEIIIIIOOOOOUUU"

// Result is a generated board and the full answer set — every word actually traceable on it.
type Result struct {
	Letters string // flat row-major, length Width*Height
	Width   int
	Height  int
	Words   []string
}

// minWords is the difficulty floor: a board must yield at least this many findable words.
func minWords(size int) int {
	switch {
	case size <= 16: // 4x4
		return 8
	case size <= 25: // 5x5
		return 16
	default: // 6x6
		return 24
	}
}

// vowelCount targets a playable vowel/consonant balance for a board of size cells.
func vowelCount(size int) int {
	v := size / 3
	if v < 3 {
		v = 3
	}
	if v > size-3 {
		v = size - 3
	}
	return v
}

// GenerateSolo returns a fresh random known-good board of w x h.
func GenerateSolo(t *Trie, w, h int, rng *rand.Rand) Result {
	return generate(t, w, h, rng)
}

// GenerateDaily returns the deterministic board for a date key like "2026-07-16" — same key
// + size + trie => identical board for everyone (kept for parity with rack.GenerateDaily;
// not wired to an endpoint yet).
func GenerateDaily(t *Trie, w, h int, dateKey string) Result {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(dateKey))
	return generate(t, w, h, rand.New(rand.NewSource(int64(hasher.Sum64()))))
}

// generate draws boards from rng until one clears the difficulty floor (bounded retries; the
// best candidate seen is returned if none clears, so this always terminates).
func generate(t *Trie, w, h int, rng *rand.Rand) Result {
	const maxTries = 300
	size := w * h
	floor := minWords(size)

	var best Result
	for i := 0; i < maxTries; i++ {
		letters := drawGrid(size, rng)
		words := Solve(t, letters, w, h)
		if len(words) > len(best.Words) {
			best = Result{Letters: letters, Width: w, Height: h, Words: words}
		}
		if len(words) >= floor {
			return Result{Letters: letters, Width: w, Height: h, Words: words}
		}
	}
	return best
}

// drawGrid builds one candidate board: a balanced set of vowels + consonants, shuffled flat.
func drawGrid(size int, rng *rand.Rand) string {
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
