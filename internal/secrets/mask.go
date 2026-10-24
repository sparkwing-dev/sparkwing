package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

type Masker struct {
	parent        *Masker
	mu            sync.RWMutex
	raw           map[string]struct{}
	values        []string
	known         map[string]struct{}
	compiled      *compiledMatcher
	compiledCount int
}

func NewMasker() *Masker { return &Masker{} }

// Scoped sees current host registrations while keeping new registrations local.
func (m *Masker) Scoped() *Masker { return &Masker{parent: m} }

// ShortValueWarning explains the masking limit without revealing a secret.
const ShortValueWarning = "Secret values shorter than 4 characters cannot be masked in logs."

// Maskable reports whether a value is long enough to mask without hiding ordinary log text.
func Maskable(value string) bool { return utf8.RuneCountInString(value) >= 4 }

// Register protects one secret and its encoded spellings. Short values are skipped.
func (m *Masker) Register(value string) error {
	if !Maskable(value) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.raw[value]; ok {
		return nil
	}
	if m.raw == nil {
		m.raw = map[string]struct{}{}
	}
	m.raw[value] = struct{}{}
	m.add(value)
	for _, encoded := range encodedForms(value) {
		m.add(encoded)
	}
	return nil
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
	if other.parent != nil {
		m.Merge(other.parent)
	}
	other.mu.RLock()
	values := slices.Clone(other.values)
	raw := make([]string, 0, len(other.raw))
	for entry := range other.raw {
		raw = append(raw, entry)
	}
	other.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.raw == nil {
		m.raw = map[string]struct{}{}
	}
	for _, entry := range raw {
		m.raw[entry] = struct{}{}
	}
	for _, value := range values {
		m.add(value)
	}
}

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
		if c != value && !slices.Contains(out, c) {
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
	return m.matcher().mask(s)
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

func unescapeJSON(s string, partial bool) (string, []span, int) {
	var b strings.Builder
	units := make([]span, 0, len(s))
	emit := func(text string, lo, hi int) {
		b.WriteString(text)
		for range len(text) {
			units = append(units, span{lo, hi})
		}
	}
	for i := 0; i < len(s); {
		if partial && partialJSONEscape(s[i:]) {
			return b.String(), units, i
		}
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
	return b.String(), units, len(s)
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

const maskedAttributes = `{"masked_attrs":"***"}`

// Attributes masks serialized attribute JSON. An invalid object, including one
// with duplicate keys after masking, becomes a single safe placeholder.
func (m *Masker) Attributes(raw []byte) []byte {
	masked := []byte(m.Mask(string(raw)))
	if !validAttributeObject(masked) {
		return []byte(maskedAttributes)
	}
	return masked
}

func validAttributeObject(raw []byte) bool {
	if !json.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	if !uniqueJSONValue(decoder, '{') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func uniqueJSONValue(decoder *json.Decoder, open json.Delim) bool {
	keys := map[string]struct{}{}
	for decoder.More() {
		if open == '{' {
			key, err := decoder.Token()
			if err != nil {
				return false
			}
			name, ok := key.(string)
			if !ok {
				return false
			}
			if _, seen := keys[name]; seen {
				return false
			}
			keys[name] = struct{}{}
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if child, ok := token.(json.Delim); ok {
			if child != '{' && child != '[' {
				return false
			}
			if !uniqueJSONValue(decoder, child) {
				return false
			}
		}
	}
	close, err := decoder.Token()
	return err == nil && close == json.Delim(map[json.Delim]rune{'{': '}', '[': ']'}[open])
}

// Values returns a copy of all raw and derived masking patterns.
func (m *Masker) Values() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	out := slices.Clone(m.values)
	m.mu.RUnlock()
	if m.parent != nil {
		out = append(out, m.parent.Values()...)
	}
	return out
}

// ContainsSecret reports whether an identifier contains a registered masking pattern.
func (m *Masker) ContainsSecret(identifier string) bool {
	if m == nil {
		return false
	}
	found, _ := m.matcher().scan(identifier, false)
	return len(found) > 0
}

// Stream masks an ordered byte stream. One goroutine owns each Stream.
type Stream struct {
	m        *Masker
	pending  string
	rendered string
	covered  int
}

func (m *Masker) Stream() *Stream { return &Stream{m: m} }

// Mask emits everything except the shortest suffix that can still form a match.
func (s *Stream) Mask(chunk string) string {
	s.pending += chunk
	if s.m == nil {
		out := s.pending
		s.pending = ""
		return out
	}
	matcher := s.m.matcher()
	spans, cut := matcher.scan(s.pending, true)
	if s.covered > 0 {
		spans = append([]span{{0, s.covered}}, spans...)
	}
	if cut == 0 {
		return ""
	}
	text := s.pending[:cut]
	covered := 0
	var complete []span
	for _, sp := range spans {
		if sp.lo < cut {
			complete = append(complete, span{sp.lo, min(cut, sp.hi)})
		}
		if sp.lo < cut && sp.hi > cut {
			covered = max(covered, sp.hi-cut)
		}
	}
	// Copy the unfinished suffix so a small tail cannot retain a large write.
	s.pending = strings.Clone(s.pending[cut:])
	if len(complete) > 0 {
		complete = mergeSpans(complete)
		if s.covered > 0 && complete[0].lo == 0 {
			offset := complete[0].hi
			text = text[offset:]
			complete = complete[1:]
			for i := range complete {
				complete[i].lo -= offset
				complete[i].hi -= offset
			}
		}
		if len(complete) > 0 {
			text = replaceSpans(text, complete, "***")
		}
	}
	s.covered = covered
	if len(spans) == 0 && s.rendered == "" && !matcher.markerSuffix {
		return text
	}
	// Re-scan after replacements because marker edges can create another match.
	s.rendered = matcher.mask(s.rendered + text)
	_, emit := matcher.scan(s.rendered, true)
	if matcher.markerSuffix && emit > 0 {
		prefix := s.rendered[:emit] + "***"
		if matcher.mask(prefix) != prefix {
			// A future redaction could consume this prefix indefinitely. Redact
			// the ambiguous emission now instead of retaining an unbounded chain.
			s.rendered = matcher.mask("***" + s.rendered[emit:])
			_, emit = matcher.scan(s.rendered, true)
		}
	}
	out := s.rendered[:emit]
	s.rendered = strings.Clone(s.rendered[emit:])
	return out
}

// Flush releases the final masked tail at EOF.
func (s *Stream) Flush() string {
	tail := s.pending
	if s.covered > 0 {
		tail = tail[s.covered:]
	}
	out := s.rendered + tail
	if s.m != nil {
		out = s.m.Mask(out)
	}
	s.rendered, s.pending, s.covered = "", "", 0
	return out
}
