package orchestrator

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/logpretty"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// nodeLogger persists one node's LogRecords as JSONL and tees to an
// optional live delegate. Implements sparkwing.Logger.
type nodeLogger struct {
	mu       sync.Mutex
	file     io.WriteCloser
	enc      *json.Encoder
	delegate sparkwing.Logger // optional tee, may be nil
	nodeID   string
}

// newNodeLogger opens path for append. Caller must Close.
func newNodeLogger(path, nodeID string, delegate sparkwing.Logger) (*nodeLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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

// PrettyRenderer is the TTY-facing Logger.
type PrettyRenderer = logpretty.PrettyRenderer

// NewPrettyRenderer writes to stdout/stderr with color unless NO_COLOR is set.
func NewPrettyRenderer() *PrettyRenderer { return logpretty.NewPrettyRenderer() }

// NewPrettyRendererTo writes all output to w with color forced via useColor.
func NewPrettyRendererTo(w io.Writer, useColor bool) *PrettyRenderer {
	return logpretty.NewPrettyRendererTo(w, useColor)
}

// StripANSI removes ANSI CSI/SGR escape sequences from s.
func StripANSI(s string) string { return logpretty.StripANSI(s) }

// JSONRenderer prints one record per line as JSON.
type JSONRenderer struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewJSONRenderer writes to os.Stdout regardless of level.
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

// envelopeLogger persists run-level envelope events as JSONL and
// tees to the user-facing delegate. Envelope events (run_start,
// run_plan, run_finish, plan_warn, validation warnings,
// the run_summary, etc.) used to live only on the dispatcher's
// stdout; this tee is the storage half that lets `sparkwing runs
// logs --follow` reconstruct the same event stream a remote operator
// would never see otherwise. Per-node body output keeps writing to
// the node's own log file via nodeLogger -- the merged-stream reader
// in jobs_cli.go interleaves the two by timestamp.
//
// Records that already carry a Node are written verbatim (so a
// node-tagged plan_warn still threads through the envelope file
// where the merged reader can find it). Records without a Node are
// pure run-level events.
type envelopeLogger struct {
	mu       sync.Mutex
	file     io.WriteCloser
	enc      *json.Encoder
	delegate sparkwing.Logger // optional tee, may be nil
}

// newEnvelopeLogger opens path for append. Caller must Close.
func newEnvelopeLogger(path string, delegate sparkwing.Logger) (*envelopeLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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
