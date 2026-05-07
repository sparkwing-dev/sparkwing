// Package secrets implements the unified secret-resolution surface for
// : lazy on-demand resolution of named secrets, a per-run cache
// and masker, and source adapters for the local dotenv file
// (~/.config/sparkwing/secrets.env) and the controller HTTP API.
package secrets

import (
	"context"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
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

// Emit satisfies sparkwing.Logger.
func (l *WrappedLogger) Emit(rec sparkwing.LogRecord) {
	rec.Msg = l.masker.Mask(rec.Msg)
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

// Values returns a snapshot of the registered values. Primarily for
// tests; callers shouldn't need this in production code.
func (m *Masker) Values() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.values))
	copy(out, m.values)
	return out
}
