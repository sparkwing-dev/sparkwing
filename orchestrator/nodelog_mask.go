package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// maskingNodeLog applies secret redaction to every record before it
// reaches inner.
type maskingNodeLog struct {
	inner  NodeLog
	masker *secrets.Masker
}

// wrapNodeLogWithMasker returns inner unchanged when masker is nil.
func wrapNodeLogWithMasker(inner NodeLog, masker *secrets.Masker) NodeLog {
	if inner == nil || masker == nil {
		return inner
	}
	return &maskingNodeLog{inner: inner, masker: masker}
}

func (l *maskingNodeLog) Log(level, msg string) {
	l.inner.Log(level, l.masker.Mask(msg))
}

func (l *maskingNodeLog) Emit(rec sparkwing.LogRecord) {
	rec.Msg = l.masker.Mask(rec.Msg)
	l.inner.Emit(rec)
}

func (l *maskingNodeLog) Close() error { return l.inner.Close() }

// Fatal forwards the inner sink's sticky auth error. Non-http
// NodeLog impls won't satisfy the optional interface; they return
// nil here, matching the no-fatal-state default.
func (l *maskingNodeLog) Fatal() error {
	if f, ok := l.inner.(interface{ Fatal() error }); ok {
		return f.Fatal()
	}
	return nil
}

// Drops forwards the inner sink's drop counter.
func (l *maskingNodeLog) Drops() (int, string) {
	if d, ok := l.inner.(interface{ Drops() (int, string) }); ok {
		return d.Drops()
	}
	return 0, ""
}
