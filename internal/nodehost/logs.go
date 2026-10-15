package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
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
		out.Attrs = attrsOf(s.cfg.Masker.MaskJSON(rec.Attrs))
	}
	if err := s.persist(out); err != nil {
		s.host(sparkwing.LogRecord{
			Level: "warn", Event: "state_write_failed", Step: out.Step,
			Msg: out.Event + " state write failed", Attrs: map[string]any{"error": err.Error()},
		})
	}
	s.emit(out)
}

// safety: masked attrs that are not an object are stored as one string, so a
// record's attrs are always a map.
func attrsOf(masked []byte) map[string]any {
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(masked))
	dec.UseNumber()
	if dec.Decode(&out) != nil || dec.More() {
		return map[string]any{"masked_attrs": string(masked)}
	}
	return out
}

// safety: a record the host writes is masked as an SDK record is, its attrs
// through their JSON; attrs with no secret keep their Go values.
func maskRecord(m *secrets.Masker, rec sparkwing.LogRecord) sparkwing.LogRecord {
	rec.Msg = m.Mask(rec.Msg)
	if len(rec.Attrs) == 0 {
		return rec
	}
	if raw, err := json.Marshal(rec.Attrs); err != nil {
		rec.Attrs = map[string]any{"masked_attrs": m.Mask(fmt.Sprint(rec.Attrs))}
	} else if masked := m.MaskJSON(raw); !bytes.Equal(masked, raw) {
		rec.Attrs = attrsOf(masked)
	}
	return rec
}

func (s *LogSink) persist(rec sparkwing.LogRecord) error {
	c, run, node := s.cfg, s.cfg.RunID, s.cfg.NodeID
	if c.State == nil {
		return nil
	}
	switch rec.Event {
	case sparkwing.EventNodeAnnotation:
		message := limits.Record(attrString(rec.Attrs, "message", rec.Msg))
		switch {
		case message == "":
			return nil
		case rec.Step != "":
			return c.State.AppendStepAnnotation(s.ctx, run, node, rec.Step, message)
		default:
			return c.State.AppendNodeAnnotation(s.ctx, run, node, message)
		}
	case sparkwing.EventNodeSummary:
		markdown := limits.Record(attrString(rec.Attrs, "markdown", rec.Msg))
		if rec.Step != "" {
			return c.State.SetStepSummary(s.ctx, run, node, rec.Step, markdown)
		}
		return c.State.SetNodeSummary(s.ctx, run, node, markdown)
	}
	return nil
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

func (s *LogSink) host(rec sparkwing.LogRecord) { s.emit(maskRecord(s.cfg.Masker, rec)) }

// MaskingLogger returns a logger that masks each record with m as a node's
// records are masked, then hands it to inner. A run's renderer and envelope
// log sit behind one, as a backstop under each source's own masking.
func MaskingLogger(inner sparkwing.Logger, m *secrets.Masker) sparkwing.Logger {
	if inner == nil {
		return nil
	}
	return maskingLogger{inner: inner, m: m}
}

type maskingLogger struct {
	inner sparkwing.Logger
	m     *secrets.Masker
}

func (l maskingLogger) Log(level, msg string) { l.inner.Log(level, l.m.Mask(msg)) }

func (l maskingLogger) Emit(rec sparkwing.LogRecord) { l.inner.Emit(maskRecord(l.m, rec)) }

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
