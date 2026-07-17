package rack

import (
	"testing"

	"github.com/wordbreak/backend/internal/dictionary"
)

func TestGenerateDaily_Deterministic(t *testing.T) {
	d := dictionary.New()
	a := GenerateDaily(d, "2026-07-16", 6)
	b := GenerateDaily(d, "2026-07-16", 6)
	if a.Letters != b.Letters {
		t.Fatalf("daily rack not deterministic: %q vs %q", a.Letters, b.Letters)
	}
	c := GenerateDaily(d, "2026-07-17", 6)
	if a.Letters == c.Letters {
		t.Log("note: different dates produced same rack (possible but unlikely)")
	}
}

func TestGenerateSolo_IsPlayable(t *testing.T) {
	d := dictionary.New()
	for _, size := range []int{4, 5, 6, 7} {
		r := GenerateSolo(d, size)
		if len(r.Letters) != size {
			t.Errorf("size %d: got %d letters", size, len(r.Letters))
		}
		if len(r.Words) == 0 {
			t.Errorf("size %d: rack %q has no valid words", size, r.Letters)
		}
	}
}
