package grid

import (
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/wordbreak/backend/internal/dictionary"
)

func TestGenerateSolo_IsPlayable(t *testing.T) {
	trie := NewTrie(dictionary.New().AllWords())

	sizes := []struct{ w, h int }{{4, 4}, {5, 5}, {6, 6}}
	for _, sz := range sizes {
		start := time.Now()
		r := GenerateSolo(trie, sz.w, sz.h, rand.New(rand.NewSource(1)))
		elapsed := time.Since(start)

		floor := minWords(sz.w * sz.h)
		if len(r.Words) < floor {
			t.Errorf("%dx%d: only %d words (floor %d), took %v", sz.w, sz.h, len(r.Words), floor, elapsed)
		}
		if len(r.Letters) != sz.w*sz.h {
			t.Errorf("%dx%d: letters length %d, want %d", sz.w, sz.h, len(r.Letters), sz.w*sz.h)
		}
		t.Logf("%dx%d: %d words, %v", sz.w, sz.h, len(r.Words), elapsed)

		// Every returned word must actually validate against the dictionary — the trie was
		// built from it, but this catches any trie-construction bug (e.g. bad insert).
		d := dictionary.New()
		for _, w := range r.Words {
			if !d.Contains(w) {
				t.Errorf("%dx%d: solver returned %q, not in dictionary", sz.w, sz.h, w)
			}
		}
	}
}

func TestGenerateDaily_Deterministic(t *testing.T) {
	trie := NewTrie(dictionary.New().AllWords())
	a := GenerateDaily(trie, 4, 4, "2026-07-17")
	b := GenerateDaily(trie, 4, 4, "2026-07-17")
	if a.Letters != b.Letters {
		t.Fatalf("same date key produced different boards: %q vs %q", a.Letters, b.Letters)
	}
	c := GenerateDaily(trie, 4, 4, "2026-07-18")
	if a.Letters == c.Letters {
		t.Fatalf("different date keys produced the same board (suspiciously)")
	}
}

func TestSolve_FindsAdjacentPathsOnly(t *testing.T) {
	// Hand-built 3x3 grid where "CAT" is adjacency-traceable but "TAG" (T at index 8,
	// far corner from A) is not, despite TAG being a real English word.
	// C A T
	// X X X
	// X X G
	letters := "CATXXXXXG"
	trie := NewTrie([]string{"CAT", "TAG", "CATNAP"})
	words := Solve(trie, letters, 3, 3)

	got := map[string]bool{}
	for _, w := range words {
		got[w] = true
	}
	if !got["CAT"] {
		t.Errorf("expected CAT to be found via the adjacent C-A-T path, got %v", words)
	}
	if got["TAG"] {
		t.Errorf("TAG should not be reachable (T and G are not adjacent), got %v", words)
	}
}

func TestTrieMemory(t *testing.T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	trie := NewTrie(dictionary.New().AllWords())
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("trie: %d nodes, heap delta ~%d MB", len(trie.nodes), (after.HeapAlloc-before.HeapAlloc)/1e6)
}
