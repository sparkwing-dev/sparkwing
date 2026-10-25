package secrets

import (
	"slices"
	"strings"
)

// Immutable per-registration automata let issued values extend a registry without
// rebuilding patterns already shared by active streams.
type compiledMatcher struct {
	groups       []*patternMatcher
	markerSuffix bool
}

type patternMatcher struct {
	exact   automaton
	percent automaton
}

type matchNode struct {
	child, sibling, fail, match, depth int32
	letter                             byte
}

type automaton struct {
	root        [256]int32
	nodes       []matchNode
	patterns    map[int32][]string
	prefixes    map[int32][]string
	samples     []int32
	values      []string
	fold        bool
	onlyStart   byte
	singleStart bool
}

func (m *Masker) matcher() *compiledMatcher {
	local := m.localMatcher()
	if m.parent == nil {
		return local
	}
	shared := m.parent.matcher()
	return &compiledMatcher{groups: append(slices.Clone(shared.groups), local.groups...), markerSuffix: shared.markerSuffix || local.markerSuffix}
}

func (m *Masker) localMatcher() *compiledMatcher {
	m.mu.RLock()
	compiled := m.compiled
	current := m.compiledCount == len(m.values)
	m.mu.RUnlock()
	if compiled != nil && current {
		return compiled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.compiled == nil || m.compiledCount != len(m.values) {
		var groups []*patternMatcher
		if m.compiled != nil {
			groups = slices.Clone(m.compiled.groups)
		}
		patterns := m.values[m.compiledCount:]
		if len(patterns) > 0 {
			var exact, percent []string
			for _, value := range patterns {
				if strings.Contains(value, "%") {
					percent = append(percent, value)
				} else {
					exact = append(exact, value)
				}
			}
			groups = append(groups, &patternMatcher{exact: compilePatterns(exact, false), percent: compilePatterns(percent, true)})
		}
		markerSuffix := m.compiled != nil && m.compiled.markerSuffix
		for _, pattern := range patterns {
			markerSuffix = markerSuffix || strings.HasSuffix(pattern, "*")
		}
		m.compiled = &compiledMatcher{groups: groups, markerSuffix: markerSuffix}
		m.compiledCount = len(m.values)
	}
	return m.compiled
}

func (a *automaton) child(at int32, c byte) int32 {
	if at == 0 {
		return a.root[c]
	}
	for next := a.nodes[at].child; next != 0; next = a.nodes[next].sibling {
		if a.nodes[next].letter == c {
			return next
		}
	}
	return 0
}

func compilePatterns(patterns []string, fold bool) automaton {
	size := 1
	for _, pattern := range patterns {
		size += len(pattern)
	}
	a := automaton{nodes: make([]matchNode, 1, size), fold: fold}
	if fold {
		a.patterns = make(map[int32][]string)
		a.samples = make([]int32, 1, size)
		a.values = patterns
	}
	for patternID, pattern := range patterns {
		var at int32
		for i := range len(pattern) {
			c := pattern[i]
			if fold {
				c = foldHex(c)
			}
			next := a.child(at, c)
			if next == 0 {
				next = int32(len(a.nodes))
				a.nodes = append(a.nodes, matchNode{letter: c, depth: a.nodes[at].depth + 1, sibling: a.nodes[at].child})
				a.nodes[at].child = next
				if fold {
					a.samples = append(a.samples, 0)
				}
				if at == 0 {
					a.root[c] = next
				}
			}
			at = next
			if fold {
				if a.samples[at] == 0 {
					a.samples[at] = int32(patternID + 1)
				} else if original := patterns[a.samples[at]-1][:i+1]; original != pattern[:i+1] && !slices.Contains(a.prefixes[at], pattern[:i+1]) {
					if a.prefixes == nil {
						a.prefixes = make(map[int32][]string)
					}
					a.prefixes[at] = append(a.prefixes[at], pattern[:i+1])
				}
			}
		}
		if !fold {
			a.nodes[at].match = int32(len(pattern))
		}
		if fold {
			a.patterns[at] = append(a.patterns[at], pattern)
		}
	}
	queue := make([]int32, 1, len(a.nodes))
	for head := 0; head < len(queue); head++ {
		at := queue[head]
		fail := a.nodes[at].fail
		if fold {
			// Percent matches require spelling checks, so retain only terminal links.
			a.nodes[at].match = a.nodes[fail].match
			if len(a.patterns[fail]) > 0 {
				a.nodes[at].match = fail
			}
		} else {
			a.nodes[at].match = max(a.nodes[at].match, a.nodes[fail].match)
		}
		for next := a.nodes[at].child; next != 0; next = a.nodes[next].sibling {
			if at != 0 {
				fail := a.nodes[at].fail
				for {
					if target := a.child(fail, a.nodes[next].letter); target != 0 {
						a.nodes[next].fail = target
						break
					}
					if fail == 0 {
						break
					}
					fail = a.nodes[fail].fail
				}
			}
			queue = append(queue, next)
		}
	}
	starts := 0
	for c, node := range a.root {
		if node != 0 {
			starts++
			a.onlyStart = byte(c)
		}
	}
	a.singleStart = starts == 1
	a.nodes = slices.Clone(a.nodes)
	if fold {
		a.samples = slices.Clone(a.samples)
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

func (a *automaton) matched(text string, at int32, end int) int {
	if !a.fold {
		return int(a.nodes[at].match)
	}
	for state := at; state != 0; state = a.nodes[state].match {
		for _, pattern := range a.patterns[state] {
			if percentEqual(text[end-len(pattern):end], pattern) {
				return len(pattern)
			}
		}
	}
	return 0
}

func (a *automaton) tail(text string, at int32) int {
	if !a.fold {
		for at != 0 && a.nodes[at].child == 0 {
			at = a.nodes[at].fail
		}
		return len(text) - int(a.nodes[at].depth)
	}
	for state := at; state != 0; state = a.nodes[state].fail {
		if a.nodes[state].child == 0 {
			continue
		}
		if percentEqual(text[len(text)-int(a.nodes[state].depth):], a.values[a.samples[state]-1][:a.nodes[state].depth]) {
			return len(text) - int(a.nodes[state].depth)
		}
		for _, prefix := range a.prefixes[state] {
			if percentEqual(text[len(text)-int(a.nodes[state].depth):], prefix) {
				return len(text) - int(a.nodes[state].depth)
			}
		}
	}
	return len(text)
}

func (a *automaton) scan(text string, spans, units []span) ([]span, int) {
	if len(a.nodes) == 1 || a.singleStart && (!a.fold || a.onlyStart < 'A' || a.onlyStart > 'F') && strings.IndexByte(text, a.onlyStart) < 0 {
		return spans, len(text)
	}
	var at int32
	start := len(spans)
	for i := range len(text) {
		c := text[i]
		if a.fold {
			c = foldHex(c)
		}
		for {
			if next := a.child(at, c); next != 0 {
				at = next
				break
			}
			if at == 0 {
				break
			}
			at = a.nodes[at].fail
		}
		if length := a.matched(text, at, i+1); length > 0 {
			sp := span{i + 1 - length, i + 1}
			if units != nil {
				sp = span{units[sp.lo].lo, units[sp.hi-1].hi}
			}
			for len(spans) > start && sp.lo < spans[len(spans)-1].hi {
				previous := spans[len(spans)-1]
				sp.lo, sp.hi = min(previous.lo, sp.lo), max(previous.hi, sp.hi)
				spans = spans[:len(spans)-1]
			}
			spans = append(spans, sp)
		}
	}
	return spans, a.tail(text, at)
}

func (m *patternMatcher) view(text string, units, spans []span) ([]span, int) {
	spans, cut := m.exact.scan(text, spans, units)
	if len(m.percent.nodes) > 1 {
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
func (m *patternMatcher) scan(text string, partial bool) ([]span, int) {
	if len(m.exact.nodes) == 1 && len(m.percent.nodes) == 1 {
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
	return found, cut
}

func (m *compiledMatcher) scan(text string, partial bool) ([]span, int) {
	var found []span
	cut := len(text)
	for _, group := range m.groups {
		spans, end := group.scan(text, partial)
		found = append(found, spans...)
		cut = min(cut, end)
	}
	if len(found) == 0 {
		return nil, cut
	}
	return mergeSpans(found), cut
}

func mergeSpans(found []span) []span {
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
	return merged
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
	// Marker edges can recreate a pattern. Bound closure work even for a chain
	// such as repeated "ab" followed by a match for "ab**".
	for range 4 {
		spans, _ := m.scan(text, false)
		if len(spans) == 0 {
			return text
		}
		text = replaceSpans(text, spans, "***")
	}
	if spans, _ := m.scan(text, false); len(spans) > 0 {
		return "***"
	}
	return text
}
