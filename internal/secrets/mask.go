package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type maskerCtxKey struct{}

// WithMasker attaches m to ctx for the run's logs, and for the step command
// lines the session ledger records, which cannot import this package.
func WithMasker(ctx context.Context, m *Masker) context.Context {
	var mask func(string) string
	if m != nil {
		mask = m.Mask
	}
	return sessionledger.WithCommandMask(context.WithValue(ctx, maskerCtxKey{}, m), mask)
}

func MaskerFromContext(ctx context.Context) *Masker {
	if m, ok := ctx.Value(maskerCtxKey{}).(*Masker); ok {
		return m
	}
	return nil
}

func MaskCtx(ctx context.Context, s string) string {
	if m := MaskerFromContext(ctx); m != nil {
		return m.Mask(s)
	}
	return s
}

type WrappedLogger struct {
	inner  sparkwing.Logger
	masker *Masker
}

func MaskingLogger(inner sparkwing.Logger, masker *Masker) sparkwing.Logger {
	if inner == nil || masker == nil {
		return inner
	}
	return &WrappedLogger{inner: inner, masker: masker}
}

func (l *WrappedLogger) Log(level, msg string) {
	l.inner.Log(level, l.masker.Mask(msg))
}

func (l *WrappedLogger) Emit(rec sparkwing.LogRecord) {
	rec.Msg = l.masker.Mask(rec.Msg)
	rec.Attrs = l.masker.MaskAttrs(rec.Attrs)
	l.inner.Emit(rec)
}

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
	if !slices.Contains(m.values, value) {
		shareRegistered(value)
	}
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

func (m *Masker) Mask(s string) string {
	if s == "" {
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

const maskAttrsMaxDepth = 8

const maskedValue = "***"

func (m *Masker) MaskAttrs(attrs map[string]any) map[string]any {
	if m == nil || len(attrs) == 0 {
		return attrs
	}
	m.mu.RLock()
	none := len(m.values) == 0
	m.mu.RUnlock()
	if none {
		return attrs
	}
	out, _ := m.maskAttrs(attrs, 0)
	return out
}

// MaskJSON masks decoded string values so JSON escaping cannot hide a secret.
// Unchanged payloads retain their bytes; invalid JSON uses literal text masking.
func (m *Masker) MaskJSON(payload []byte) []byte {
	if m == nil || len(payload) == 0 {
		return payload
	}
	m.mu.RLock()
	none := len(m.values) == 0
	m.mu.RUnlock()
	if none {
		return payload
	}
	masked, changed := m.maskJSON(json.RawMessage(payload), 0)
	if changed {
		encoded, err := json.Marshal(masked)
		if err != nil {
			// safety: an unexpected encoding error must not restore the original secret-bearing payload.
			return []byte(`"***"`)
		}
		return encoded
	}
	if json.Valid(payload) {
		return payload
	}
	text := m.Mask(string(payload))
	if text == string(payload) {
		return payload
	}
	return []byte(text)
}

func (m *Masker) maskAttrs(attrs map[string]any, depth int) (map[string]any, bool) {
	if len(attrs) == 0 {
		return attrs, false
	}
	var out map[string]any
	for k, v := range attrs {
		mv, changed := m.maskValue(v, depth+1)
		if !changed {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(attrs))
			maps.Copy(out, attrs)
		}
		out[k] = mv
	}
	if out == nil {
		return attrs, false
	}
	return out, true
}

func (m *Masker) maskValue(v any, depth int) (any, bool) {
	if depth > maskAttrsMaxDepth {
		if s, ok := v.(string); ok && s == maskedValue {
			return v, false
		}
		return maskedValue, true
	}
	switch t := v.(type) {
	case json.Number:
		return t, false
	case string:
		masked := m.Mask(t)
		return masked, masked != t
	case []string:
		var out []string
		for i, s := range t {
			masked := m.Mask(s)
			if masked == s {
				continue
			}
			if out == nil {
				out = slices.Clone(t)
			}
			out[i] = masked
		}
		if out == nil {
			return t, false
		}
		return out, true
	case []any:
		var out []any
		for i, e := range t {
			masked, changed := m.maskValue(e, depth+1)
			if !changed {
				continue
			}
			if out == nil {
				out = slices.Clone(t)
			}
			out[i] = masked
		}
		if out == nil {
			return t, false
		}
		return out, true
	case map[string]any:
		return m.maskAttrs(t, depth)
	case []byte:
		masked := m.Mask(string(t))
		if masked == string(t) {
			return t, false
		}
		return []byte(masked), true
	case map[string]string:
		var out map[string]string
		for k, s := range t {
			masked := m.Mask(s)
			if masked == s {
				continue
			}
			if out == nil {
				out = maps.Clone(t)
			}
			out[k] = masked
		}
		if out == nil {
			return t, false
		}
		return out, true
	case error:
		masked := m.Mask(t.Error())
		if masked == t.Error() {
			if _, changed := m.maskJSON(t, depth); !changed {
				return t, false
			}
			masked = maskedValue
		}
		return maskedError{msg: masked, err: t}, true
	default:
		// safety: text and JSON sinks can render different fields of the same value.
		rendered := fmt.Sprint(v)
		masked := m.Mask(rendered)
		if masked == rendered {
			return m.maskJSON(v, depth)
		}
		return masked, true
	}
}

func (m *Masker) maskJSON(v any, depth int) (any, bool) {
	switch v.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr, float32, float64:
		return v, false
	}

	encoded, err := json.Marshal(v)
	if err != nil {
		return v, false
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return v, false
	}
	switch decoded.(type) {
	case map[string]any, []any, string:
		return m.maskValue(decoded, depth)
	default:
		return v, false
	}
}

type maskedError struct {
	msg string
	err error
}

func (e maskedError) Error() string { return e.msg }

func (e maskedError) MarshalJSON() ([]byte, error) { return json.Marshal(e.msg) }

func (e maskedError) Unwrap() error { return e.err }

// Values returns a copy of masking patterns: whole values, nonblank multiline
// components, and their encoded forms.
func (m *Masker) Values() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.values))
	copy(out, m.values)
	return out
}
