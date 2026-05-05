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
