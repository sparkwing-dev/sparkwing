package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// maskingNodeLog applies secret redaction to every record before it
// reaches inner.
type maskingNodeLog struct {
	inner  NodeLog
	masker *secrets.Masker
}

// maskEventPayload runs an audit-event payload through the run's
// masker before it is appended.
//
// Audit events are a read surface -- `GET /api/v1/runs/{id}/events`
// serves them and the dashboard streams them over SSE -- but nothing
// routes them through the log masker, which only rewrites a log
// record's Msg. child_run_start in particular carries the args a
// parent forwards to a child, so a parent's secret arriving in a
// child's argument list would otherwise be published verbatim.
//
// Value-anchored like the log masker, so it also covers a secret
// embedded inside a larger argument rather than passed as one.
// Returns payload unchanged when masker is nil or has no values.
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
