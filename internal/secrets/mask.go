package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

type registration struct {
	value string
	list  bool
}

type Masker struct {
	mu     sync.RWMutex
	raw    map[registration]struct{}
	values []string
	known  map[string]struct{}
}

func NewMasker() *Masker { return &Masker{} }

// Register protects a raw secret and its eligible line fragments and encodings.
func (m *Masker) Register(value string) { m.register(value, false) }

// RegisterList also protects eligible comma-separated items of a []string argument.
func (m *Masker) RegisterList(value string) { m.register(value, true) }

const minFragmentLength = 12

// ListFragments returns eligible derived items from a list-typed argument.
func ListFragments(value string) []string {
	var parts []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if len(part) >= minFragmentLength {
			parts = append(parts, part)
		}
	}
	return parts
}

func (m *Masker) register(value string, list bool) {
	if value == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	raw := registration{value, list}
	if _, ok := m.raw[raw]; ok {
		return
	}
	if m.raw == nil {
		m.raw = map[registration]struct{}{}
	}
	m.raw[raw] = struct{}{}
	literals := []string{value}
	if list {
		literals = append(literals, ListFragments(value)...)
	}
	for _, literal := range slices.Clone(literals) {
		for _, line := range strings.Split(literal, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if len(line) >= minFragmentLength && strings.TrimSpace(line) != "" {
				literals = append(literals, line)
			}
		}
	}
	for _, literal := range literals {
		m.add(literal)
		for _, encoded := range encodedForms(literal) {
			m.add(encoded)
		}
	}
}

func (m *Masker) add(value string) {
	if value == "" {
		return
	}
	if m.known == nil {
		m.known = map[string]struct{}{}
	}
	if _, ok := m.known[value]; ok {
		return
	}
	m.known[value] = struct{}{}
	m.values = append(m.values, value)
}

// Merge copies raw registrations and patterns without re-encoding derived patterns.
func (m *Masker) Merge(other *Masker) {
	if other == nil || other == m {
		return
	}
	other.mu.RLock()
	values := slices.Clone(other.values)
	raw := make([]registration, 0, len(other.raw))
	for entry := range other.raw {
		raw = append(raw, entry)
	}
	other.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.raw == nil {
		m.raw = map[registration]struct{}{}
	}
	for _, entry := range raw {
		m.raw[entry] = struct{}{}
	}
	for _, value := range values {
		m.add(value)
	}
}

// safety: shorter encodings of short secrets would mask ordinary log text.
const minEncodedFormLen = 4

func encodedForms(value string) []string {
	jsonHTML, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var jsonPlain bytes.Buffer
	enc := json.NewEncoder(&jsonPlain)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil
	}
	query := url.QueryEscape(value)
	// safety: a JSON spelling differs from value only by a backslash escape, which ordinary text rarely holds,
	// so it registers at any length; JSON masking sees no other spelling of a short secret.
	jsonForms := []string{trimJSONString(string(jsonHTML)), trimJSONString(jsonPlain.String()), trimJSONString(strconv.Quote(value)), trimJSONString(strconv.QuoteToASCII(value))}
	candidates := []string{
		query,
		strings.ReplaceAll(query, "+", "%20"),
		url.PathEscape(value),
		uriComponentEscape(value),
		base64.StdEncoding.EncodeToString([]byte(value)),
		base64.RawURLEncoding.EncodeToString([]byte(value)),
	}
	for lead := range 3 {
		std := embeddedBase64(value, lead)
		candidates = append(candidates, std, strings.NewReplacer("+", "-", "/", "_").Replace(std))
	}
	var out []string
	for _, c := range jsonForms {
		if c != value && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	for _, c := range candidates {
		if len(c) >= minEncodedFormLen && c != value && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// safety: JavaScript's encodeURIComponent leaves !'()* unescaped, which no Go
// escaper matches.
func uriComponentEscape(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.~!'()*", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func trimJSONString(encoded string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(encoded, "\n"), `"`), `"`)
}

func embeddedBase64(value string, lead int) string {
	buf := make([]byte, lead, lead+len(value))
	buf = append(buf, value...)
	encoded := base64.RawStdEncoding.EncodeToString(buf)
	// safety: inside a longer blob the edge characters also carry bits of the
	// neighboring bytes, so only the characters built from value alone match.
	start := (8*lead + 5) / 6
	end := 8 * len(buf) / 6
	if start >= end {
		return ""
	}
	return encoded[start:end]
}

// Mask replaces every registered value in s with ***, also where s spells it
// with JSON escapes. A nil Masker masks nothing.
func (m *Masker) Mask(s string) string {
	if m == nil || s == "" {
		return s
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return maskPatterns(s, m.values)
}

func maskPatterns(s string, values []string) string {
	marker := maskMarker(values)
	for {
		spans := maskSpans(s, values)
		if len(spans) == 0 {
			return s
		}
		s = replaceSpans(s, spans, marker)
	}
}

func maskMarker(values []string) string {
	if len(maskSpans("***", values)) > 0 {
		return ""
	}
	return "***"
}

func replaceSpans(s string, spans []span, marker string) string {
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(s[prev:sp.lo])
		b.WriteString(marker)
		prev = sp.hi
	}
	b.WriteString(s[prev:])
	return b.String()
}

type span struct{ lo, hi int }

// safety: overlapping values merge into one range, so replacing one cannot
// leave a fragment of the other in the clear.
func maskSpans(s string, values []string) []span {
	if len(values) == 0 {
		return nil
	}
	var found []span
	for _, v := range values {
		for at := 0; ; {
			i := strings.Index(s[at:], v)
			if i < 0 {
				break
			}
			found = append(found, span{at + i, at + i + len(v)})
			at += i + 1
		}
	}
	if strings.IndexByte(s, '\\') >= 0 {
		decoded, units := unescapeJSON(s)
		for _, v := range values {
			for at := 0; ; {
				i := strings.Index(decoded[at:], v)
				if i < 0 {
					break
				}
				found = append(found, span{units[at+i].lo, units[at+i+len(v)-1].hi})
				at += i + 1
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	slices.SortFunc(found, func(a, b span) int { return a.lo - b.lo })
	merged := found[:1]
	for _, sp := range found[1:] {
		last := &merged[len(merged)-1]
		if sp.lo < last.hi {
			last.hi = max(last.hi, sp.hi)
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}

func unescapeJSON(s string) (string, []span) {
	var b strings.Builder
	units := make([]span, 0, len(s))
	emit := func(text string, lo, hi int) {
		b.WriteString(text)
		for range len(text) {
			units = append(units, span{lo, hi})
		}
	}
	for i := 0; i < len(s); {
		if s[i] != '\\' || i+1 >= len(s) {
			emit(s[i:i+1], i, i+1)
			i++
			continue
		}
		if c, ok := simpleEscapes[s[i+1]]; ok {
			emit(c, i, i+2)
			i += 2
			continue
		}
		if r, n := unicodeEscape(s[i:]); n > 0 {
			emit(string(r), i, i+n)
			i += n
			continue
		}
		emit(s[i:i+1], i, i+1)
		i++
	}
	return b.String(), units
}

var simpleEscapes = map[byte]string{'"': `"`, '\\': `\`, '/': "/", 'b': "\b", 'f': "\f", 'n': "\n", 'r': "\r", 't': "\t"}

func unicodeEscape(s string) (rune, int) {
	r, ok := hex4(s)
	if !ok {
		return 0, 0
	}
	if !utf16.IsSurrogate(r) {
		return r, 6
	}
	if low, ok := hex4(s[6:]); ok {
		if pair := utf16.DecodeRune(r, low); pair != utf8.RuneError {
			return pair, 12
		}
	}
	return 0, 0
}

func hex4(s string) (rune, bool) {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(s[2:6], 16, 16)
	return rune(n), err == nil
}

// MaskJSON masks arbitrary data keys, strings and scalar values in canonical JSON.
// Invalid JSON is masked as text; numbers keep their exact spelling.
func (m *Masker) MaskJSON(raw []byte) []byte {
	var values []string
	if m != nil {
		m.mu.RLock()
		defer m.mu.RUnlock()
		values = m.values
	}
	mask := func(text string) string { return maskPatterns(text, values) }
	if !json.Valid(raw) {
		return []byte(mask(string(raw)))
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return []byte(mask(string(raw)))
	}
	var visit func(any) any
	visit = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			out := make(map[string]any, len(v))
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, key := range keys {
				masked := mask(key)
				if _, collision := out[masked]; collision {
					return maskMarker(values)
				}
				out[masked] = visit(v[key])
			}
			return out
		case []any:
			for i, child := range v {
				v[i] = visit(child)
			}
		case string:
			return mask(v)
		case json.Number, bool, nil:
			text := "null"
			if v != nil {
				text = fmt.Sprint(v)
			}
			if masked := mask(text); masked != text {
				return masked
			}
		}
		return value
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(visit(value)) != nil {
		return []byte(`{"masked_attrs":"***"}`)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// Values returns a copy of all raw and derived masking patterns.
func (m *Masker) Values() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.values))
	copy(out, m.values)
	return out
}

// ContainsSecret reports whether an identifier contains a registered masking pattern.
func (m *Masker) ContainsSecret(identifier string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(maskSpans(identifier, m.values)) > 0
}

// Stream masks one text stream that arrives as lines, and as pieces of a line
// too long to hold whole, so a value split across pieces is still masked.
// One goroutine owns a Stream.
type Stream struct {
	m       *Masker
	held    string
	covered int
}

// Stream returns a stream masked with m's values; a nil m masks nothing.
func (m *Masker) Stream() *Stream { return &Stream{m: m} }

// Mask masks text and returns what can be shown now. Text ends a line when
// lineEnd is set and is a piece of one otherwise; a piece keeps back the tail
// that could start a value until the next text arrives.
func (s *Stream) Mask(text string, lineEnd bool) string {
	text = s.held + text
	cut, spans := s.safeCut(text)
	if lineEnd {
		cut = len(text)
	}
	skip := s.covered
	for _, sp := range spans {
		if skip > 0 && sp.lo < skip {
			skip = max(skip, sp.hi)
		}
	}
	s.held, s.covered = text[cut:], max(0, skip-cut)
	if skip >= cut {
		return ""
	}
	for _, sp := range spans {
		if sp.lo < cut && cut < sp.hi {
			s.covered = sp.hi - cut
			return s.m.Mask(text[skip:sp.lo]) + s.m.Mask("***")
		}
	}
	return s.m.Mask(text[skip:cut])
}

// Drop forgets the held tail of a line whose rest was lost, so no later line
// completes a value the tail starts.
func (s *Stream) Drop() { s.held, s.covered = "", 0 }

// Retain raw overlap so the next piece can extend a match already shown as a mask.
func (s *Stream) safeCut(text string) (int, []span) {
	if s.m == nil {
		return len(text), nil
	}
	s.m.mu.RLock()
	longest := 0
	for _, v := range s.m.values {
		longest = max(longest, len(v))
	}
	spans := maskSpans(text, s.m.values)
	s.m.mu.RUnlock()
	if strings.IndexByte(text, '\\') >= 0 {
		longest *= len(`\u0000`)
	}
	cut := max(0, len(text)-max(0, longest-1))
	if strings.IndexByte(text, '\\') >= 0 {
		_, units := unescapeJSON(text)
		for _, u := range units {
			if u.lo < cut && cut < u.hi {
				return u.lo, spans
			}
		}
	}
	return cut, spans
}

// MaskDetails projects JSON error details through the run's masker without mutating the source.
func (m *Masker) MaskDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(m.MaskJSON(raw)))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil {
		return nil
	}
	return out
}
