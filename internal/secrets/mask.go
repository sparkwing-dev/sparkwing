package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
)

type Masker struct {
	mu     sync.RWMutex
	values []string
}

func NewMasker() *Masker { return &Masker{} }

func (m *Masker) Register(value string) {
	if value == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	values := []string{value}
	if strings.Contains(value, "\n") {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if strings.TrimSpace(line) != "" {
				values = append(values, line)
			}
		}
	}
	for _, literal := range values {
		for _, v := range append([]string{literal}, encodedForms(literal)...) {
			if !slices.Contains(m.values, v) {
				m.values = append(m.values, v)
			}
		}
	}
	// safety: Mask replaces in slice order, so a shorter secret that prefixes a
	// longer one must not run first or it leaves the longer tail in the clear.
	slices.SortStableFunc(m.values, func(a, b string) int { return len(b) - len(a) })
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
	candidates := []string{
		trimJSONString(string(jsonHTML)),
		trimJSONString(jsonPlain.String()),
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

// Mask replaces every registered value in s with ***. A nil Masker masks nothing.
func (m *Masker) Mask(s string) string {
	if m == nil || s == "" {
		return s
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.values) == 0 {
		return s
	}
	for _, v := range m.values {
		if !strings.Contains(s, v) {
			continue
		}
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// MaskJSON masks raw's canonical encoding as text, so a value is found in a
// key or a number, and in a string however it was escaped. Raw comes back as
// it is when nothing matched. Masked JSON that no longer parses, such as a
// masked number, comes back as {"masked_attrs": text}, so JSON in is JSON out.
func (m *Masker) MaskJSON(raw []byte) []byte {
	text, valid := string(raw), json.Valid(raw)
	if valid {
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if dec.Decode(&v) == nil && enc.Encode(v) == nil {
			text = strings.TrimSuffix(buf.String(), "\n")
		}
	}
	masked := m.Mask(text)
	switch {
	case masked == text:
		return raw
	case valid && !json.Valid([]byte(masked)):
		out, err := json.Marshal(map[string]string{"masked_attrs": masked})
		if err != nil {
			return []byte(`{"masked_attrs":"***"}`)
		}
		return out
	}
	return []byte(masked)
}

// Values returns a copy of masking patterns: whole values, nonblank multiline
// components, and their encoded forms.
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

// Stream masks one text stream that arrives as lines, and as pieces of a line
// too long to hold whole, so a value split across pieces is still masked.
// One goroutine owns a Stream.
type Stream struct {
	m    *Masker
	held string
}

// Stream returns a stream masked with m's values; a nil m masks nothing.
func (m *Masker) Stream() *Stream { return &Stream{m: m} }

// Mask masks text and returns what can be shown now. Text ends a line when
// lineEnd is set and is a piece of one otherwise; a piece keeps back the tail
// that could start a value until the next text arrives.
func (s *Stream) Mask(text string, lineEnd bool) string {
	text, s.held = s.held+text, ""
	if !lineEnd {
		cut := s.safeCut(text)
		text, s.held = text[:cut], text[cut:]
	}
	return s.m.Mask(text)
}

// safety: the held rest is shorter than the longest value, so the next piece
// can complete a value it starts; a value straddling the cut moves the cut
// past its end, which bounds both the hold and the scan.
func (s *Stream) safeCut(text string) int {
	values := s.m.Values()
	longest := 0
	for _, v := range values {
		longest = max(longest, len(v))
	}
	cut := max(0, len(text)-max(0, longest-1))
	for moved := true; moved; {
		moved = false
		for _, v := range values {
			lo, hi := max(0, cut-len(v)+1), min(len(text), cut+len(v)-1)
			if lo >= hi {
				continue
			}
			if i := strings.Index(text[lo:hi], v); i >= 0 && lo+i < cut {
				cut, moved = lo+i+len(v), true
			}
		}
	}
	return cut
}
