package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// RecordWriter stores a node's log records. The orchestrator's node logs
// satisfy it: a run-directory file for a local run, log segments for a
// controller run.
type RecordWriter interface {
	Emit(rec sparkwing.LogRecord)
}

// TextWriter is a [RecordWriter] that takes the stdout and stderr text
// records apart from the SDK's records, so a renderer can show what customer
// code printed differently from how the record is stored.
type TextWriter interface {
	EmitText(rec sparkwing.LogRecord)
}

// NodeState stores the annotations and summaries a node's records carry. The
// orchestrator's state backends satisfy it.
type NodeState interface {
	AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error
	AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error
	SetNodeSummary(ctx context.Context, runID, nodeID, md string) error
	SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error
}

// LogSinkConfig names the node a [LogSink] serves and where its records go.
type LogSinkConfig struct {
	RunID  string
	NodeID string
	Log    RecordWriter
	// State is nil for output with no node row, such as plan code's, whose
	// annotations and summaries are dropped.
	State NodeState
	// Masker is the run's; every secret registered with it before a record
	// arrives is masked in that record.
	Masker *secrets.Masker
	// Now stamps each record; nil means time.Now.
	Now func() time.Time
	// Progress, when set, is called for each record stored.
	Progress func()
}

// LogSink is the [Sink] that stores one node's SDK output: it masks every
// record, stamps each line and record with the host's clock, attributes it to
// the node, and writes node_annotation and node_summary records to the store.
type LogSink struct {
	cfg LogSinkConfig
	ctx context.Context
}

// safety: an allowlist, so an event the SDK invents can never pass as a host lifecycle record.
var sdkEvents = map[string]bool{
	sparkwing.EventNodeAnnotation: true,
	sparkwing.EventNodeSummary:    true,
	"exec_start":                  true,
	"exec_line":                   true,
	"exec_end":                    true,
}

var logLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// NewLogSink returns a sink for cfg's node. Store writes outlive ctx's
// cancellation, so a cancelled node's last annotations still land.
func NewLogSink(ctx context.Context, cfg LogSinkConfig) (*LogSink, error) {
	if cfg.RunID == "" || cfg.NodeID == "" || cfg.Log == nil || cfg.Masker == nil {
		return nil, errors.New("log sink: run, node, log and masker are required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &LogSink{cfg: cfg, ctx: context.WithoutCancel(ctx)}, nil
}

// Text stores one stdout or stderr line, which the session or a [LineWriter]
// has masked, as a plain message at level.
func (s *LogSink) Text(level, line string) {
	if line != "" {
		s.emitText(sparkwing.LogRecord{Level: level, Msg: line})
	}
}

// Record stores one frame-pipe record. A host-reserved or unknown event is
// stored as a plain message, so an SDK cannot forge step or node lifecycle.
func (s *LogSink) Record(rec nodeproto.LogRecord) {
	out := sparkwing.LogRecord{Level: rec.Level, Step: rec.Step, Msg: s.cfg.Masker.Mask(rec.Msg)}
	if !logLevels[out.Level] {
		out.Level = LevelInfo
	}
	if sdkEvents[rec.Event] {
		out.Event = rec.Event
	}
	if len(rec.Attrs) > 0 {
		out.Attrs = s.maskAttrs(rec.Attrs)
	}
	if err := s.persist(out); err != nil {
		s.emit(sparkwing.LogRecord{
			Level: "warn", Event: "state_write_failed", Step: out.Step,
			Msg:   out.Event + " state write failed",
			Attrs: map[string]any{"error": s.cfg.Masker.Mask(err.Error())},
		})
	}
	s.emit(out)
}

// safety: masking the canonical JSON text as a whole reaches keys and numbers
// too; text the masking left unparseable is stored as one masked string.
func (s *LogSink) maskAttrs(raw json.RawMessage) map[string]any {
	text := string(raw)
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) == nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(v) == nil {
			text = strings.TrimSuffix(buf.String(), "\n")
		}
	}
	masked := s.cfg.Masker.Mask(text)
	var out map[string]any
	dec = json.NewDecoder(strings.NewReader(masked))
	dec.UseNumber()
	if dec.Decode(&out) != nil || dec.More() {
		return map[string]any{"masked_attrs": masked}
	}
	return out
}

func (s *LogSink) persist(rec sparkwing.LogRecord) error {
	c, run, node := s.cfg, s.cfg.RunID, s.cfg.NodeID
	if c.State == nil {
		return nil
	}
	switch rec.Event {
	case sparkwing.EventNodeAnnotation:
		message := boundedRecord(attrString(rec.Attrs, "message", rec.Msg), "annotation")
		switch {
		case message == "":
			return nil
		case rec.Step != "":
			return c.State.AppendStepAnnotation(s.ctx, run, node, rec.Step, message)
		default:
			return c.State.AppendNodeAnnotation(s.ctx, run, node, message)
		}
	case sparkwing.EventNodeSummary:
		markdown := boundedRecord(attrString(rec.Attrs, "markdown", rec.Msg), "summary")
		if rec.Step != "" {
			return c.State.SetStepSummary(s.ctx, run, node, rec.Step, markdown)
		}
		return c.State.SetNodeSummary(s.ctx, run, node, markdown)
	}
	return nil
}

// safety: the controller refuses a JSON body over 1 MiB and escaping can grow
// text up to sixfold, so a stored summary or annotation is bounded well below that.
const maxRecordBytes = 128 << 10

// safety: the record is masked before this cut, so no secret is split; the
// text keeps its start, where its headline is, and says what it dropped.
func boundedRecord(text, kind string) string {
	if len(text) <= maxRecordBytes {
		return text
	}
	kept := strings.ToValidUTF8(text[:maxRecordBytes-128], "")
	return fmt.Sprintf("%s\n\n… (%s truncated: %d bytes elided)\n", kept, kind, len(text)-len(kept))
}

func attrString(attrs map[string]any, key, fallback string) string {
	if v, ok := attrs[key].(string); ok {
		return v
	}
	return fallback
}

// safety: the host's clock and node id replace the SDK's, which it cannot be trusted to set.
func (s *LogSink) emit(rec sparkwing.LogRecord) {
	rec.TS = s.cfg.Now()
	rec.JobID = s.cfg.NodeID
	s.cfg.Log.Emit(rec)
	if s.cfg.Progress != nil {
		s.cfg.Progress()
	}
}

func (s *LogSink) emitText(rec sparkwing.LogRecord) {
	tw, ok := s.cfg.Log.(TextWriter)
	if !ok {
		s.emit(rec)
		return
	}
	rec.TS = s.cfg.Now()
	rec.JobID = s.cfg.NodeID
	tw.EmitText(rec)
	if s.cfg.Progress != nil {
		s.cfg.Progress()
	}
}
