package orchestrator

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	compactProgressLimit   = 20
	compactDiagnosticLimit = 5
	compactTextBytes       = 256
)

// compactRunRenderer summarizes display events after the durable log writers.
// Node-process transport keeps the complete JSON renderer.
type compactRunRenderer struct {
	mu                             sync.Mutex
	enc                            *json.Encoder
	err                            error
	runID                          string
	progress, diagnostics, omitted int
	duration                       int64
	started, finished              bool
	counts                         map[string]int
	failures                       []map[string]any
}

func newCompactRunRenderer(w io.Writer) *compactRunRenderer {
	return &compactRunRenderer{enc: json.NewEncoder(w)}
}

func compactText(s string) string {
	if len(s) <= compactTextBytes {
		return s
	}
	const marker = "… [truncated]"
	s = s[:compactTextBytes-len(marker)]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + marker
}

func (r *compactRunRenderer) Log(level, msg string) {
	r.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (r *compactRunRenderer) Emit(rec sparkwing.LogRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	if rec.JobID != "" && strings.HasPrefix(rec.Event, "run_") {
		r.omitted++
		return
	}
	attrs := map[string]any{}
	copyText := func(key string) {
		if v, ok := rec.Attrs[key].(string); ok && v != "" {
			attrs[key] = compactText(v)
		}
	}
	switch rec.Event {
	case "run_start":
		if r.started {
			r.omitted++
			return
		}
		r.started = true
		r.runID, _ = rec.Attrs["run_id"].(string)
		copyText("run_id")
		copyText("pipeline")
		attrs["detail"] = "summary"
	case "run_summary":
		r.counts = map[string]int{}
		r.failures = nil
		r.duration, _ = rec.Attrs["duration_ms"].(int64)
		nodes, _ := rec.Attrs["nodes"].([]any)
		for _, node := range nodes {
			row, ok := node.(map[string]any)
			if !ok {
				continue
			}
			outcome, _ := row["outcome"].(string)
			switch outcome {
			case "success", "failed", "skipped", "cancelled", "cached", "satisfied", "skipped-concurrent", "superseded", "unknown":
			default:
				outcome = "unknown"
			}
			r.counts[outcome]++
			if outcome == "failed" && len(r.failures) < 5 {
				id, _ := row["id"].(string)
				cause, _ := row["error"].(string)
				r.failures = append(r.failures, map[string]any{"node": compactText(id), "error": compactText(cause)})
			}
		}
		return
	case "run_finish":
		r.finished = true
		copyText("run_id")
		copyText("status")
		copyText("error")
		if id, ok := rec.Attrs["run_id"].(string); ok && id != "" {
			r.runID = id
		}
		if r.runID != "" {
			attrs["run_id"] = compactText(r.runID)
		}
		attrs["omitted_events"] = r.omitted
		if r.duration > 0 {
			attrs["duration_ms"] = r.duration
		}
		if r.counts != nil {
			attrs["nodes"] = r.counts
		}
		if len(r.failures) > 0 {
			attrs["failures"] = r.failures
		}
		if r.counts["failed"] > len(r.failures) {
			attrs["omitted_failures"] = r.counts["failed"] - len(r.failures)
		}
		if r.runID != "" {
			attrs["hints"] = map[string]string{"status": "sparkwing runs status --run " + compactText(r.runID), "logs": "sparkwing runs logs --run " + compactText(r.runID) + " --follow"}
		}
	case "node_end":
		if r.progress >= compactProgressLimit {
			r.omitted++
			return
		}
		r.progress++
		copyText("outcome")
		copyText("error")
	default:
		if (rec.Level != "warn" && rec.Level != "error") || rec.Event == "exec_line" || r.diagnostics >= compactDiagnosticLimit {
			r.omitted++
			return
		}
		r.diagnostics++
	}
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	rec.JobID = compactText(rec.JobID)
	rec.Step = compactText(rec.Step)
	rec.Event = compactText(rec.Event)
	rec.Level = compactText(rec.Level)
	rec.Msg = compactText(rec.Msg)
	rec.Attrs = attrs
	if r.err == nil {
		r.err = r.enc.Encode(rec)
	}
}

func (r *compactRunRenderer) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func selectRunRenderer() sparkwing.Logger {
	format := strings.ToLower(os.Getenv("SPARKWING_LOG_FORMAT"))
	if (format == "json" || (format == "" && !isInteractiveStdout())) && os.Getenv("SPARKWING_LOG_LEVEL") != "debug" {
		return newCompactRunRenderer(os.Stdout)
	}
	return selectLocalRenderer()
}

func runDisplayError(renderer sparkwing.Logger, err error) string {
	if _, compact := renderer.(*compactRunRenderer); compact {
		return strings.Join(strings.Fields(compactText(err.Error())), " ")
	}
	return err.Error()
}

// Some planning and setup failures return before emitting a lifecycle event.
func (r *compactRunRenderer) finishResult(result *Result, err error) {
	attrs := map[string]any{"status": "unknown"}
	if result != nil {
		attrs["run_id"] = result.RunID
		attrs["status"] = result.Status
		if err == nil {
			err = result.Error
		}
	}
	if err != nil {
		attrs["error"] = err.Error()
		if result == nil {
			attrs["status"] = "failed"
		}
	}
	r.Emit(sparkwing.LogRecord{Event: "run_finish", Attrs: attrs})
}
