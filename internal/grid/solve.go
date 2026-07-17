package grid

import "github.com/wordbreak/backend/internal/dictionary"

// Solve returns every dictionary word (>= dictionary.MinWordLen) reachable via a path of
// adjacent, non-repeated cells through the grid. letters is a flat row-major string of
// length w*h (index i is row i/w, col i%w). This is what makes a generated board "known
// good" — the actual playable answer set, not just the letters that were placed on it.
func Solve(t *Trie, letters string, w, h int) []string {
	found := make(map[string]struct{}, 64)
	var used uint64
	buf := make([]byte, 0, w+h)

	for start := 0; start < w*h; start++ {
		dfs(t, letters, w, h, start, 0, used, buf, found)
	}

	out := make([]string, 0, len(found))
	for word := range found {
		out = append(out, word)
	}
	return out
}

func dfs(t *Trie, letters string, w, h, cell int, node int32, used uint64, buf []byte, found map[string]struct{}) {
	bit := uint64(1) << uint(cell)
	if used&bit != 0 {
		return
	}
	next, ok := t.step(node, letters[cell])
	if !ok {
		return // no dictionary word continues with this prefix — prune the branch
	}
	buf = append(buf, letters[cell])
	used |= bit

	if t.isWord(next) && len(buf) >= dictionary.MinWordLen {
		found[string(buf)] = struct{}{}
	}

	row, col := cell/w, cell%w
	for dr := -1; dr <= 1; dr++ {
		for dc := -1; dc <= 1; dc++ {
			if dr == 0 && dc == 0 {
				continue
			}
			nr, nc := row+dr, col+dc
			if nr < 0 || nr >= h || nc < 0 || nc >= w {
				continue
			}
			dfs(t, letters, w, h, nr*w+nc, next, used, buf, found)
		}
	}
}
