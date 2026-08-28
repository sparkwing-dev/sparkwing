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
	_ = l.enc.Encode(&rec)
	l.mu.Unlock()
	if l.delegate != nil {
		l.delegate.Emit(rec)
	}
}

func (l *nodeLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
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
