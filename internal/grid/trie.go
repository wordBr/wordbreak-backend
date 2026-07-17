// Package grid generates and solves Boggle-style letter grids: an NxN board where words are
// traced through paths of adjacent (8-directional), non-repeated cells — a different shape of
// puzzle than the rack package's "any subset of letters" matching, so it needs its own
// word-finding approach: a prefix trie that prunes a DFS walk of the board.
package grid

// Trie is a prefix tree over an uppercase A-Z word list, used to prune adjacency-path search:
// a DFS branch dies the instant no dictionary word starts with the prefix traced so far.
//
// Slice-backed rather than map[rune]*node per node — cheaper to build and hold in memory for
// ~370k words than one map per node would be.
type Trie struct {
	nodes []trieNode
}

type trieNode struct {
	children [26]int32 // index into nodes, -1 = absent
	isWord   bool
}

func newNode() trieNode {
	n := trieNode{}
	for i := range n.children {
		n.children[i] = -1
	}
	return n
}

// NewTrie builds a trie from words (case-insensitive; non-A-Z runes make a word unindexable
// and it's silently skipped, matching dictionary.isAlpha's already-applied filtering upstream).
func NewTrie(words []string) *Trie {
	t := &Trie{nodes: []trieNode{newNode()}} // nodes[0] = root
	for _, w := range words {
		t.insert(w)
	}
	return t
}

func (t *Trie) insert(w string) {
	cur := int32(0)
	for i := 0; i < len(w); i++ {
		c := w[i]
		if c < 'A' || c > 'Z' {
			return // not a plain A-Z word — skip
		}
		idx := int32(c - 'A')
		next := t.nodes[cur].children[idx]
		if next == -1 {
			t.nodes = append(t.nodes, newNode())
			next = int32(len(t.nodes) - 1)
			t.nodes[cur].children[idx] = next
		}
		cur = next
	}
	t.nodes[cur].isWord = true
}

// step follows one letter from node, reporting the next node and whether that prefix exists
// at all in the trie. Callers use ok=false to prune a DFS branch immediately.
func (t *Trie) step(node int32, c byte) (next int32, ok bool) {
	if c < 'A' || c > 'Z' {
		return 0, false
	}
	next = t.nodes[node].children[c-'A']
	return next, next != -1
}

func (t *Trie) isWord(node int32) bool { return t.nodes[node].isWord }
