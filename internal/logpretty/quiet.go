package logpretty

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type QuietRenderer struct {
	mu       sync.Mutex
	w        io.Writer
	errW     io.Writer
	useColor bool

	pendingSummary *sparkwing.LogRecord
}

func NewQuietRenderer() *QuietRenderer {
	return &QuietRenderer{
		w:        os.Stdout,
		errW:     os.Stderr,
		useColor: os.Getenv("NO_COLOR") == "",
	}
}

func NewQuietRendererTo(w io.Writer, useColor bool) *QuietRenderer {
	return &QuietRenderer{w: w, errW: w, useColor: useColor}
}

func (q *QuietRenderer) color(s, code string) string {
	if !q.useColor {
		return s
	}
	return code + s + ansiReset
}

func (q *QuietRenderer) Log(level, msg string) {
	q.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (q *QuietRenderer) Emit(rec sparkwing.LogRecord) {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch rec.Event {
	case "run_start":
		q.writeStart(rec)
	case "run_summary":
		recCopy := rec
		q.pendingSummary = &recCopy
	case "run_finish":
		q.writeFinish(rec)
		q.pendingSummary = nil
	}
}

func (q *QuietRenderer) Flush() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pendingSummary != nil {
		q.writeFinish(sparkwing.LogRecord{})
		q.pendingSummary = nil
	}
}

func (q *QuietRenderer) writeStart(rec sparkwing.LogRecord) {
	pipeline, _ := rec.Attrs["pipeline"].(string)
	if pipeline == "" {
		pipeline = "pipeline"
	}
	runID, _ := rec.Attrs["run_id"].(string)
	line := q.color("▶", ansiBlue) + " " + q.color(pipeline, ansiBold) + q.color(" running…", ansiDim)

	if name := profileName(rec.Attrs); name != "" {
		line += q.color("  profile "+name, ansiDim)
	}
	if runID != "" {
		line += q.color("  run "+runID, ansiDim)
	}
	fmt.Fprintln(q.w, line)
}

func profileName(attrs map[string]any) string {
	p, ok := attrs["profile"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := p["name"].(string)
	return name
}

func (q *QuietRenderer) writeFinish(finish sparkwing.LogRecord) {
	runID, _ := finish.Attrs["run_id"].(string)
	status, _ := finish.Attrs["status"].(string)

	var dms int64
	var nodes []any
	if q.pendingSummary != nil {
		if status == "" {
			status, _ = q.pendingSummary.Attrs["status"].(string)
		}
		if v, ok := asMillis(q.pendingSummary.Attrs["duration_ms"]); ok {
			dms = v
		}
		nodes, _ = q.pendingSummary.Attrs["nodes"].([]any)
	}

	sink := q.w
	if status == "failed" {
		sink = q.errW
	}

	icon, code := summaryStatusIcon(status)
	line := q.color(icon+" "+statusWord(status), code)
	if dms > 0 {
		line += " " + q.color("("+fmtDuration(dms)+")", ansiDim)
	}
	if runID != "" {
		line += q.color("  run "+runID, ansiDim)
	}
	fmt.Fprintln(sink, line)

	if status == "failed" {
		q.writeFailureDetail(sink, nodes, runID)
	}
}

func (q *QuietRenderer) writeFailureDetail(sink io.Writer, nodes []any, runID string) {
	for _, n := range nodes {
		m, ok := n.(map[string]any)
		if !ok || m["outcome"] != "failed" {
			continue
		}
		id, _ := m["id"].(string)
		errMsg, _ := m["error"].(string)
		stepID, body := splitStepErrorPrefix(errMsg)
		crumb := q.color(id, ansiBold+ansiRed)
		if stepID != "" {
			crumb += q.color(" › "+stepID, ansiDim)
		}
		lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		for i, l := range lines {
			if i == 0 {
				fmt.Fprintln(sink, "  "+crumb+q.color(" │ ", ansiDim)+q.color(l, ansiRed))
			} else {
				fmt.Fprintln(sink, "    "+q.color(l, ansiRed))
			}
		}
	}
	if runID != "" {
		fmt.Fprintln(sink, "  "+q.color("full log", ansiDim)+"  "+q.color("sparkwing runs logs --run "+runID, ansiCyan))
	}
}

func statusWord(status string) string {
	switch status {
	case "success":
		return "passed"
	case "failed":
		return "failed"
	case "":
		return "ended"
	default:
		return status
	}
}
