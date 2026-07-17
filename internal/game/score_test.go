package game

import (
	"testing"

	"github.com/wordbreak/backend/internal/dictionary"
)

func testDict() *dictionary.Dictionary {
	return dictionary.NewFromWords([]string{"CAT", "CART", "CART", "ARC", "RAT", "TRACE", "ZEBRA"})
}

func TestScore_AcceptsValidWords(t *testing.T) {
	d := testDict()
	res := Score(d, "CARTX", []string{"cat", "cart", "arc", "rat"})
	if len(res.Accepted) != 4 {
		t.Fatalf("accepted = %d, want 4", len(res.Accepted))
	}
	// CART (4) = 2, CAT/ARC/RAT (3) = 1 each => 5
	if res.Total != 5 {
		t.Fatalf("total = %d, want 5", res.Total)
	}
}

func TestScore_RejectsWithReasons(t *testing.T) {
	d := testDict()
	res := Score(d, "CART", []string{
		"cat",   // ok
		"cat",   // duplicate
		"zebra", // not in rack
		"xy",    // too short
		"crat",  // in rack letters but not a word
	})
	if len(res.Accepted) != 1 {
		t.Fatalf("accepted = %d, want 1", len(res.Accepted))
	}
	reasons := map[string]string{}
	for _, r := range res.Rejected {
		reasons[r.Word] = r.Reason
	}
	if reasons["CAT"] != ReasonDuplicate {
		t.Errorf("CAT reason = %q, want duplicate", reasons["CAT"])
	}
	if reasons["ZEBRA"] != ReasonNotInRack {
		t.Errorf("ZEBRA reason = %q, want not_in_rack", reasons["ZEBRA"])
	}
	if reasons["XY"] != ReasonTooShort {
		t.Errorf("XY reason = %q, want too_short", reasons["XY"])
	}
	if reasons["CRAT"] != ReasonNotAWord {
		t.Errorf("CRAT reason = %q, want not_a_word", reasons["CRAT"])
	}
}

func TestScore_RespectsLetterMultiplicity(t *testing.T) {
	d := dictionary.NewFromWords([]string{"TOO", "TO"})
	// rack has only one O, so TOO (needs two) must be rejected as not_in_rack
	res := Score(d, "TOX", []string{"too"})
	if len(res.Accepted) != 0 || len(res.Rejected) != 1 || res.Rejected[0].Reason != ReasonNotInRack {
		t.Fatalf("expected TOO rejected not_in_rack, got %+v", res)
	}
}
