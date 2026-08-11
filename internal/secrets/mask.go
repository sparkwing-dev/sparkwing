// Package secrets implements the unified secret-resolution surface for
// : lazy on-demand resolution of named secrets, a per-run cache
// and masker, and source adapters for the local dotenv file
// (~/.config/sparkwing/secrets.env) and the controller HTTP API.
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

// WithMasker installs masker on ctx so loggers / exec captures can
// reach it without threading another argument through every call
// site. Nil-safe: callers don't need to check before reading.
func WithMasker(ctx context.Context, m *Masker) context.Context {
	return context.WithValue(ctx, maskerCtxKey{}, m)
}

// MaskerFromContext returns the masker installed on ctx or nil when
// none is present. Use through the package-level Mask helper to skip
// the nil check at call sites.
func MaskerFromContext(ctx context.Context) *Masker {
	if m, ok := ctx.Value(maskerCtxKey{}).(*Masker); ok {
		return m
	}
	return nil
}

// MaskCtx is the nil-safe convenience: returns s unchanged when no
// masker is installed, or m.Mask(s) otherwise.
func MaskCtx(ctx context.Context, s string) string {
	if m := MaskerFromContext(ctx); m != nil {
		return m.Mask(s)
	}
	return s
}

// WrappedLogger is a sparkwing.Logger that masks rec.Msg via masker
// before forwarding to inner. Used by the orchestrator to wrap the
// per-node Logger returned from OpenNodeLog so resolved secret
// values are redacted in both the persisted log records and any
// downstream renderer output.
type WrappedLogger struct {
	inner  sparkwing.Logger
	masker *Masker
}

// MaskingLogger returns inner unchanged when masker is nil; otherwise
// wraps it so every Emit / Log call routes the Msg through the masker.
// Concurrent-safe (the underlying masker is).
func MaskingLogger(inner sparkwing.Logger, masker *Masker) sparkwing.Logger {
	if inner == nil || masker == nil {
		return inner
	}
	return &WrappedLogger{inner: inner, masker: masker}
}

// Log satisfies sparkwing.Logger.
func (l *WrappedLogger) Log(level, msg string) {
	l.inner.Log(level, l.masker.Mask(msg))
}

// Emit satisfies sparkwing.Logger. Both the message and the record's
// structured attributes are redacted: step_end carries the failing
// command's output in attrs["error"], which is exactly the place a
// secret that reached a command line surfaces.
func (l *WrappedLogger) Emit(rec sparkwing.LogRecord) {
	rec.Msg = l.masker.Mask(rec.Msg)
	rec.Attrs = l.masker.MaskAttrs(rec.Attrs)
	l.inner.Emit(rec)
}

// Masker replaces registered secret values with `***` in arbitrary
// text. Designed for log emission and exec output capture: the
// per-run masker accumulates values as they're resolved, and any
// downstream writer that runs text through Mask() gets redaction
// for free.
type Masker struct {
	mu     sync.RWMutex
	values []string
}

// NewMasker returns a fresh masker with no values registered.
func NewMasker() *Masker { return &Masker{} }

// Register adds value to the redaction set. No-op for empty values
// (would otherwise rewrite every byte of output to ***). Duplicate
// values are ignored so callers can register freely.
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

// Mask returns s with every registered value replaced by `***`.
// Returns s unchanged when no values are registered (the common case
// for runs that don't read any secrets).
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

// maskAttrsMaxDepth bounds recursion into nested attribute values.
// Log attributes are shallow JSON-ish data in practice; the cap is
// insurance against a pathological (or cyclic) map rather than a
// meaningful limit.
//
// Past the cap the value is replaced by maskedValue rather than passed
// through. A redaction pass that gives up must give up closed: emitting
// data it declined to inspect is the one outcome that turns a depth
// guard into a leak.
const maskAttrsMaxDepth = 8

// maskedValue is what a redacted (or un-inspectable) value becomes.
const maskedValue = "***"

// MaskAttrs returns attrs with every string value redacted, recursing
// into the container shapes an attribute value takes: []string, []any,
// map[string]any, map[string]string. Anything nested deeper than
// maskAttrsMaxDepth is replaced wholesale with "***".
//
// Deliberately not inspected, and therefore passed through unchanged:
//
//   - numbers, bools, nil -- cannot carry a secret value
//   - structs and pointers to them, []byte, map[any]byte and other
//     non-string-keyed maps, error, fmt.Stringer
//
// The second group is a real (if narrow) gap: a struct or []byte
// attribute holding a secret is emitted as-is. It is left open on
// purpose -- reflecting over arbitrary values on every log record is
// the wrong cost, and no emitter in the tree puts one in Attrs. Any new
// emitter that wants to must pre-render the value to a string, which is
// then covered here. Callers building attributes from untrusted or
// unaudited data should mask before emitting rather than rely on this.
//
// Copy-on-write: the input map is never mutated, and when nothing
// needed redacting the input map itself is returned, so the common
// case (no secrets registered, or none present in this record) costs
// one map lookup and no allocation. That matters because attribute
// maps are shared -- run_start hands the same invocation map to the
// log record and to the persisted run row.
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

// maskAttrs is MaskAttrs's recursive half; the bool reports whether
// anything was rewritten so callers can skip the copy. Depth is
// checked by maskValue, which fails closed; this half only guards the
// empty case.
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

// maskValue redacts one attribute value, reporting whether it changed.
// Past the depth cap it fails closed: the whole sub-value is replaced
// by "***" rather than emitted uninspected.
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

// Values returns a snapshot of the registered values. Primarily for
// tests; callers shouldn't need this in production code.
func (m *Masker) Values() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.values))
	copy(out, m.values)
	return out
}
