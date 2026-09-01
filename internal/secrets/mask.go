package secrets

import (
	"context"
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
	default:
		return v, false
	}
}

func (m *Masker) Values() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.values))
	copy(out, m.values)
	return out
}
