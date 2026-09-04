package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type maskingNodeLog struct {
	inner  NodeLog
	masker *secrets.Masker
}

func maskEventPayload(masker *secrets.Masker, payload []byte) []byte {
	if masker == nil || len(payload) == 0 {
		return payload
	}
	masked := masker.Mask(string(payload))
	if masked == string(payload) {
		return payload
	}
	return []byte(masked)
}

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
	rec.Attrs = l.masker.MaskAttrs(rec.Attrs)
	l.inner.Emit(rec)
}

func (l *maskingNodeLog) Close() error { return l.inner.Close() }

func (l *maskingNodeLog) BindExecutionAttempt(ordinal int) error {
	return bindNodeLogExecutionAttempt(l.inner, ordinal)
}

func (l *maskingNodeLog) FlushExecutionAttempt() error {
	return flushNodeLogExecutionAttempt(l.inner)
}

func (l *maskingNodeLog) Fatal() error {
	if f, ok := l.inner.(interface{ Fatal() error }); ok {
		return f.Fatal()
	}
	return nil
}

func (l *maskingNodeLog) Drops() (int, string) {
	if d, ok := l.inner.(interface{ Drops() (int, string) }); ok {
		return d.Drops()
	}
	return 0, ""
}
