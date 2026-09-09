package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type maskerCtxKey struct{}

func WithMasker(ctx context.Context, m *Masker) context.Context {
	return context.WithValue(ctx, maskerCtxKey{}, m)
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
	for _, v := range m.values {
		if v == value {
			return
		}
	}
	m.values = append(m.values, value)
	// safety: Mask replaces in slice order, so a shorter secret that prefixes a
	// longer one must not run first or it leaves the longer tail in the clear.
	slices.SortStableFunc(m.values, func(a, b string) int { return len(b) - len(a) })
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

func (m *Masker) Values() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.values))
	copy(out, m.values)
	return out
}
