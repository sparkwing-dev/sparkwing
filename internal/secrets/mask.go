package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	mu     sync.RWMutex
	raw    map[string]struct{}
	values []string
	known  map[string]struct{}
}

func NewMasker() *Masker { return &Masker{} }

// ErrInvalidValue is safe to present when discovery cannot classify author output.
var ErrInvalidValue = errors.New("secret value must contain at least 4 characters and must not contain ***; use a plain config variable instead")

// Validate refuses values that cannot be masked safely as independent secrets.
func Validate(value string) error {
	if utf8.RuneCountInString(value) < 4 || strings.Contains(value, "***") {
		return ErrInvalidValue
	}
	return nil
}

// Register protects one secret and its encoded spellings.
func (m *Masker) Register(value string) error {
	if err := Validate(value); err != nil {
		return err
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	return maskPatterns(s, m.values)
}

func maskPatterns(s string, values []string) string {
	for {
		spans := maskSpans(s, values)
		if len(spans) == 0 {
			return s
		}
		s = replaceSpans(s, spans, "***")
	}
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
		if strings.Contains(v, "%") {
			for at := 0; at+len(v) <= len(s); at++ {
				if percentEqual(s[at:at+len(v)], v) {
					found = append(found, span{at, at + len(v)})
				}
			}
		} else {
			for at := 0; ; {
				i := strings.Index(s[at:], v)
				if i < 0 {
					break
				}
				found = append(found, span{at + i, at + i + len(v)})
				at += i + 1
			}
		}
	}
	if strings.IndexByte(s, '\\') >= 0 {
		decoded, units := unescapeJSON(s)
		found = appendDecodedSpans(found, decoded, units, values)
		if strings.IndexByte(decoded, '\\') >= 0 {
			second, secondUnits := unescapeJSON(decoded)
			for i, unit := range secondUnits {
				secondUnits[i] = span{units[unit.lo].lo, units[unit.hi-1].hi}
			}
			found = appendDecodedSpans(found, second, secondUnits, values)
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

func appendDecodedSpans(found []span, decoded string, units []span, values []string) []span {
	for _, value := range values {
		if strings.Contains(value, "%") {
			for at := 0; at+len(value) <= len(decoded); at++ {
				if percentEqual(decoded[at:at+len(value)], value) {
					found = append(found, span{units[at].lo, units[at+len(value)-1].hi})
				}
			}
			continue
		}
		for at := 0; ; {
			i := strings.Index(decoded[at:], value)
			if i < 0 {
				break
			}
			found = append(found, span{units[at+i].lo, units[at+i+len(value)-1].hi})
			at += i + 1
		}
	}
	return found
}

// percentEqual folds only the hex digits of percent escapes. Ordinary letters
// remain case-sensitive, including letters adjacent to an escape.
func percentEqual(text, pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		if text[i] == pattern[i] {
			continue
		}
		if i > 0 && pattern[i-1] == '%' || i > 1 && pattern[i-2] == '%' {
			if text[i]|0x20 == pattern[i]|0x20 && pattern[i]|0x20 >= 'a' && pattern[i]|0x20 <= 'f' {
				continue
			}
		}
		return false
	}
	return true
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

// Stream masks an ordered byte stream. One goroutine owns each Stream.
type Stream struct {
	m        *Masker
	pending  string
	rendered string
}

func (m *Masker) Stream() *Stream { return &Stream{m: m} }

// Mask accepts raw bytes before line splitting and returns only text that can
// no longer join a subsequent chunk into a known spelling.
func (s *Stream) Mask(chunk string) string {
	s.pending += chunk
	if s.m == nil {
		out := s.pending
		s.pending = ""
		return out
	}
	s.m.mu.RLock()
	values := slices.Clone(s.m.values)
	s.m.mu.RUnlock()
	if len(values) == 0 {
		out := s.pending
		s.pending = ""
		return out
	}
	longest := longestPattern(values)
	// Two JSON escape views can expand each pattern byte to six bytes twice.
	// Saturate at the pending size to avoid overflow for exceptionally long values.
	reserve := len(s.pending)
	if longest <= reserve/36 {
		reserve = longest * 36
	}
	cut := max(0, len(s.pending)-reserve)
	if cut == 0 {
		return ""
	}
	for _, sp := range maskSpans(s.pending, values) {
		if sp.lo < cut && cut < sp.hi {
			cut = sp.lo
		}
	}
	if cut == 0 {
		return ""
	}
	sanitized := maskPatterns(s.pending[:cut], values)
	s.pending = s.pending[cut:]
	s.rendered = maskPatterns(s.rendered+sanitized, values)
	emit := max(0, len(s.rendered)-reserve)
	if emit == 0 {
		return ""
	}
	// Keep an entire match when the chosen output boundary falls within it.
	for _, sp := range maskSpans(s.rendered, values) {
		if sp.lo < emit && emit < sp.hi {
			emit = sp.lo
		}
	}
	out := s.rendered[:emit]
	s.rendered = s.rendered[emit:]
	return out
}

// Flush releases the final masked tail at EOF.
func (s *Stream) Flush() string {
	if s.m == nil {
		out := s.rendered + s.pending
		s.rendered, s.pending = "", ""
		return out
	}
	out := s.m.Mask(s.rendered + s.pending)
	s.rendered, s.pending = "", ""
	return out
}

func longestPattern(values []string) int {
	longest := 0
	for _, value := range values {
		longest = max(longest, len(value))
	}
	return longest
}
