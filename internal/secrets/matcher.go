package secrets

import (
	"slices"
	"strings"
)

// Immutable automata are shared by every stream and rebuilt only after registration.
type compiledMatcher struct {
	exact   automaton
	percent automaton
}

type automaton struct {
	classes  [256]int
	width    int
	next     []int
	depth    []int
	match    []int
	fail     []int
	links    []int
	patterns [][]string
	prefixes [][]string
	length   []int
	fold     bool
}

func (m *Masker) matcher() *compiledMatcher {
	m.mu.RLock()
	compiled := m.compiled
	m.mu.RUnlock()
	if compiled != nil {
		return compiled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.compiled == nil {
		var percent []string
		for _, value := range m.values {
			if strings.Contains(value, "%") {
				percent = append(percent, value)
			}
		}
		m.compiled = &compiledMatcher{exact: compilePatterns(m.values, false), percent: compilePatterns(percent, true)}
	}
	return m.compiled
}

// Alphabet compression keeps a dense Aho-Corasick transition table compact.
func compilePatterns(patterns []string, fold bool) automaton {
	type node struct {
		edges              map[byte]int
		fail, depth, match int
		patterns           []string
		prefixes           []string
	}
	nodes := []node{{edges: map[byte]int{}}}
	a := automaton{width: 1, fold: fold}
	for _, pattern := range patterns {
		at := 0
		for i := range len(pattern) {
			c := pattern[i]
			if fold {
				c = foldHex(c)
			}
			if a.classes[c] == 0 {
				a.classes[c] = a.width
				a.width++
			}
			next, ok := nodes[at].edges[c]
			if !ok {
				next = len(nodes)
				nodes = append(nodes, node{edges: map[byte]int{}, depth: nodes[at].depth + 1})
				nodes[at].edges[c] = next
			}
			at = next
			if fold && !slices.Contains(nodes[at].prefixes, pattern[:i+1]) {
				nodes[at].prefixes = append(nodes[at].prefixes, pattern[:i+1])
			}
		}
		nodes[at].match = len(pattern)
		if fold {
			nodes[at].patterns = append(nodes[at].patterns, pattern)
		}
	}
	a.next = make([]int, len(nodes)*a.width)
	a.depth = make([]int, len(nodes))
	a.match = make([]int, len(nodes))
	a.fail = make([]int, len(nodes))
	a.links = make([]int, len(nodes))
	a.length = make([]int, len(nodes))
	if fold {
		a.patterns = make([][]string, len(nodes))
		a.prefixes = make([][]string, len(nodes))
	}
	queue := []int{0}
	for head := 0; head < len(queue); head++ {
		at := queue[head]
		n := &nodes[at]
		a.depth[at], a.match[at] = n.depth, max(n.match, nodes[n.fail].match)
		if len(n.edges) == 0 {
			a.depth[at] = a.depth[n.fail]
		}
		n.match = a.match[at]
		a.fail[at], a.length[at] = n.fail, n.depth
		if fold {
			a.patterns[at] = n.patterns
			if len(n.edges) > 0 {
				a.prefixes[at] = n.prefixes
			}
			a.links[at] = n.fail
			if len(nodes[n.fail].patterns) == 0 {
				a.links[at] = a.links[n.fail]
			}
		}
		if at != 0 {
			copy(a.next[at*a.width:(at+1)*a.width], a.next[n.fail*a.width:(n.fail+1)*a.width])
		}
		for c, child := range n.edges {
			nodes[child].fail = a.next[n.fail*a.width+a.classes[c]]
			if at == 0 {
				nodes[child].fail = 0
			}
			a.next[at*a.width+a.classes[c]] = child
			queue = append(queue, child)
		}
	}
	return a
}

func foldHex(c byte) byte {
	if c >= 'a' && c <= 'f' {
		return c - ('a' - 'A')
	}
	return c
}

// Only percent-escape positions accept case variants; neighboring text remains exact.
func percentEqual(text, pattern string) bool {
	for i := range len(pattern) {
		if text[i] == pattern[i] {
			continue
		}
		if (i > 0 && pattern[i-1] == '%' || i > 1 && pattern[i-2] == '%') && foldHex(text[i]) == foldHex(pattern[i]) {
			continue
		}
		return false
	}
	return true
}

func (a *automaton) matched(text string, at, end int) int {
	if !a.fold {
		return a.match[at]
	}
	for state := at; state != 0; state = a.links[state] {
		for _, pattern := range a.patterns[state] {
			if percentEqual(text[end-len(pattern):end], pattern) {
				return len(pattern)
			}
		}
	}
	return 0
}

func (a *automaton) tail(text string, at int) int {
	if !a.fold {
		return len(text) - a.depth[at]
	}
	for state := at; state != 0; state = a.fail[state] {
		for _, prefix := range a.prefixes[state] {
			if percentEqual(text[len(text)-a.length[state]:], prefix) {
				return len(text) - a.length[state]
			}
		}
	}
	return len(text)
}

func (a *automaton) scan(text string, spans []span, units []span) ([]span, int) {
	at := 0
	for i := range len(text) {
		c := text[i]
		if a.fold {
			c = foldHex(c)
		}
		at = a.next[at*a.width+a.classes[c]]
		if length := a.matched(text, at, i+1); length > 0 {
			sp := span{i + 1 - length, i + 1}
			if units != nil {
				sp = span{units[sp.lo].lo, units[sp.hi-1].hi}
			}
			spans = append(spans, sp)
		}
	}
	return spans, a.tail(text, at)
}

func (m *compiledMatcher) view(text string, units []span, spans []span) ([]span, int) {
	spans, cut := m.exact.scan(text, spans, units)
	if m.percent.width > 1 && strings.IndexByte(text, '%') >= 0 {
		var percentCut int
		spans, percentCut = m.percent.scan(text, spans, units)
		cut = min(cut, percentCut)
	}
	if len(units) > 0 {
		if cut < len(units) {
			cut = units[cut].lo
		} else {
			cut = units[len(units)-1].hi
		}
	}
	return spans, cut
}

// scan also maps prefixes in two JSON escape views back to their raw byte boundary.
func (m *compiledMatcher) scan(text string, partial bool) ([]span, int) {
	if m.exact.width == 1 {
		return nil, len(text)
	}
	found, cut := m.view(text, nil, nil)
	if strings.IndexByte(text, '\\') >= 0 {
		decoded, units, end := unescapeJSON(text, partial)
		var escapedCut int
		if end < len(text) {
			found, escapedCut = m.view(text[:end], nil, found)
			cut = min(cut, escapedCut)
		}
		found, escapedCut = m.view(decoded, units, found)
		cut = min(cut, escapedCut)
		if strings.IndexByte(decoded, '\\') >= 0 {
			second, secondUnits, end := unescapeJSON(decoded, partial)
			if end < len(decoded) {
				found, escapedCut = m.view(decoded[:end], units[:end], found)
				cut = min(cut, escapedCut)
			}
			for i, unit := range secondUnits {
				secondUnits[i] = span{units[unit.lo].lo, units[unit.hi-1].hi}
			}
			found, escapedCut = m.view(second, secondUnits, found)
			cut = min(cut, escapedCut)
		}
	}
	if len(found) == 0 {
		return nil, cut
	}
	slices.SortFunc(found, func(a, b span) int { return a.lo - b.lo })
	merged := found[:1]
	for _, sp := range found[1:] {
		last := &merged[len(merged)-1]
		if sp.lo < last.hi {
			last.hi = max(last.hi, sp.hi)
		} else {
			merged = append(merged, sp)
		}
	}
	return merged, cut
}

// A partial escape is kept by the same decoder that handles complete escapes.
func partialJSONEscape(text string) bool {
	if text[0] != '\\' {
		return false
	}
	if len(text) == 1 {
		return true
	}
	if text[1] != 'u' {
		return false
	}
	for i := 2; i < min(6, len(text)); i++ {
		c := text[i]
		if !(c >= '0' && c <= '9' || c|32 >= 'a' && c|32 <= 'f') {
			return false
		}
	}
	if len(text) < 6 {
		return true
	}
	if r, ok := hex4(text); ok && r >= 0xD800 && r <= 0xDBFF && len(text) < 12 {
		if len(text) == 6 {
			return true
		}
		return partialJSONEscape(text[6:])
	}
	return false
}

func (m *compiledMatcher) mask(text string) string {
	for {
		spans, _ := m.scan(text, false)
		if len(spans) == 0 {
			return text
		}
		text = replaceSpans(text, spans, "***")
	}
}
