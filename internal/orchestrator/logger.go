package orchestrator

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/logpretty"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type nodeLogger struct {
	mu       sync.Mutex
	file     io.WriteCloser
	enc      *json.Encoder
	delegate sparkwing.Logger
	nodeID   string

	closed     bool
	dropCount  int
	dropReason string
}

func newNodeLogger(path, nodeID string, delegate sparkwing.Logger) (*nodeLogger, error) {
	f, err := fssecure.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return nil, err
	}
	return &nodeLogger{
		file:     f,
		enc:      json.NewEncoder(f),
		delegate: delegate,
		nodeID:   nodeID,
	}, nil
}

func (l *nodeLogger) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (l *nodeLogger) Emit(rec sparkwing.LogRecord) {
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	if rec.JobID == "" {
		rec.JobID = l.nodeID
	}
	l.mu.Lock()
	if !l.closed {
		if err := l.enc.Encode(&rec); err != nil {
			l.recordDropLocked(err)
		}
	}
	l.mu.Unlock()
	if l.delegate != nil {
		l.delegate.Emit(rec)
	}
}

// Close is safe to call twice: the executor closes the log before it reads
// [nodeLogger.Drops], and its own deferred close then has nothing left to do.
func (l *nodeLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	err := l.file.Close()
	if err != nil {
		l.recordDropLocked(err)
	}
	return err
}

// Drops reports lines this writer failed to put on disk, so a node whose local
// log file stopped accepting writes fails the same way a lost remote append does.
func (l *nodeLogger) Drops() (int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropCount, l.dropReason
}

func (l *nodeLogger) recordDropLocked(err error) {
	l.dropCount++
	if l.dropReason == "" {
		l.dropReason = err.Error()
	}
}

type PrettyRenderer = logpretty.PrettyRenderer

func NewPrettyRenderer() *PrettyRenderer { return logpretty.NewPrettyRenderer() }

func NewPrettyRendererTo(w io.Writer, useColor bool) *PrettyRenderer {
	return logpretty.NewPrettyRendererTo(w, useColor)
}

type QuietRenderer = logpretty.QuietRenderer

func NewQuietRenderer() *QuietRenderer { return logpretty.NewQuietRenderer() }

func StripANSI(s string) string { return logpretty.StripANSI(s) }

type JSONRenderer struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewJSONRenderer() *JSONRenderer {
	return &JSONRenderer{enc: json.NewEncoder(os.Stdout)}
}

func (j *JSONRenderer) Log(level, msg string) {
	j.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (j *JSONRenderer) Emit(rec sparkwing.LogRecord) {
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.enc.Encode(&rec)
}

type envelopeLogger struct {
	mu       sync.Mutex
	file     io.WriteCloser
	enc      *json.Encoder
	delegate sparkwing.Logger
}

func newEnvelopeLogger(path string, delegate sparkwing.Logger) (*envelopeLogger, error) {
	f, err := fssecure.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return nil, err
	}
	return &envelopeLogger{
		file:     f,
		enc:      json.NewEncoder(f),
		delegate: delegate,
	}, nil
}

func (l *envelopeLogger) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (l *envelopeLogger) Emit(rec sparkwing.LogRecord) {
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	l.mu.Lock()
	_ = l.enc.Encode(&rec)
	l.mu.Unlock()
	if l.delegate != nil {
		l.delegate.Emit(rec)
	}
}

func (l *envelopeLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
