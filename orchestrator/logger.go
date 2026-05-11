package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

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
	if rec.Node == "" {
		rec.Node = l.nodeID
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

// PrettyRenderer is the TTY-facing Logger. Child-process ANSI in Msg
// passes through verbatim. Each node gets a palette slot on first
// appearance.
type PrettyRenderer struct {
	mu        sync.Mutex
	w         io.Writer
	errW      io.Writer
	useColor  bool
	nodeStart map[string]time.Time
	nodeSlot  map[string]int
	nextSlot  int

	// Single-step collapse: a node_start whose immediate next event is
	// a step_start for the same node renders as one combined line
	// (▶ node › ● step). Symmetrically, a step_end whose immediate
	// next event is a node_end for the same node is absorbed into
	// that node_end (✗ node › step (dur)). Other events flush the
	// pending record on its own.
	pendingNodeStart *sparkwing.LogRecord
	pendingStepEnd   *sparkwing.LogRecord

	// pendingRunSummary buffers the run_summary record so the
	// trailing run_finish event can fold its run-id / status /
	// hints into the same Summary block instead of dribbling them
	// out below the closing rule. Flush renders the block alone if
	// run_finish never arrives (truncated stream).
	pendingRunSummary *sparkwing.LogRecord

	// pendingRunStart buffers the run_start record so the trailing
	// run_plan event can fold its DAG into the same Setup block. If
	// run_plan never arrives (cold-cache pipeline failure before
	// plan emission, truncated stream), flushPending renders a
	// Plan-less Setup block.
	pendingRunStart *sparkwing.LogRecord
}

// NewPrettyRenderer writes to stdout/stderr with color unless NO_COLOR is set.
func NewPrettyRenderer() *PrettyRenderer {
	return &PrettyRenderer{
		w:         os.Stdout,
		errW:      os.Stderr,
		useColor:  os.Getenv("NO_COLOR") == "",
		nodeStart: map[string]time.Time{},
		nodeSlot:  map[string]int{},
	}
}

// NewPrettyRendererTo writes all output to w with color forced via useColor.
func NewPrettyRendererTo(w io.Writer, useColor bool) *PrettyRenderer {
	return &PrettyRenderer{
		w:         w,
		errW:      w,
		useColor:  useColor,
		nodeStart: map[string]time.Time{},
		nodeSlot:  map[string]int{},
	}
}

// hueFor returns name's palette color. Must be called under p.mu.
func (p *PrettyRenderer) hueFor(name string) string {
	if name == "" {
		return nodePalette[0]
	}
	slot, ok := p.nodeSlot[name]
	if !ok {
		slot = p.nextSlot % len(nodePalette)
		p.nodeSlot[name] = slot
		p.nextSlot++
	}
	return nodePalette[slot]
}

func (p *PrettyRenderer) Log(level, msg string) {
	p.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

const (
	ansiReset   = "\x1b[0m"
	ansiDim     = "\x1b[2m"
	ansiBold    = "\x1b[1m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiBlue    = "\x1b[34m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

// nodePalette: well-spaced 256-color hues, mid-brightness for both
// dark and light terminals.
var nodePalette = []string{
	"\x1b[38;5;214m", // gold
	"\x1b[38;5;117m", // sky blue
	"\x1b[38;5;114m", // mint green
	"\x1b[38;5;212m", // hot pink
	"\x1b[38;5;208m", // orange
	"\x1b[38;5;141m", // lavender
	"\x1b[38;5;173m", // terracotta
	"\x1b[38;5;109m", // steel blue
	"\x1b[38;5;183m", // light violet
	"\x1b[38;5;115m", // sea green
	"\x1b[38;5;174m", // salmon
	"\x1b[38;5;147m", // periwinkle
	"\x1b[38;5;178m", // dark yellow
	"\x1b[38;5;108m", // sage
	"\x1b[38;5;176m", // pink
	"\x1b[38;5;202m", // red-orange
}

func (p *PrettyRenderer) color(s, code string) string {
	if !p.useColor {
		return s
	}
	return code + s + ansiReset
}

func (p *PrettyRenderer) Emit(rec sparkwing.LogRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sink := p.w
	if rec.Level == "error" {
		sink = p.errW
	}
	nodeHue := p.hueFor(rec.Node)

	// Decide collapse before flushing: if this event is the merge
	// candidate for a buffered one, render the combined line and
	// return without flushing or re-handling.
	if rec.Event == "step_start" && p.pendingNodeStart != nil && p.pendingNodeStart.Node == rec.Node {
		nh := p.hueFor(p.pendingNodeStart.Node)
		head := p.color("▶ "+p.pendingNodeStart.Node, ansiBold+nh)
		stepGlyph := p.color("●", ansiBlue)
		stepName := p.color(rec.Msg, ansiBlue)
		fmt.Fprintln(sink, head+"  "+stepGlyph+" "+stepName)
		p.pendingNodeStart = nil
		return
	}
	// When a buffered step_end is followed by an inline error log
	// from the same step (the runner emits one for each failed step
	// attempt -- see inprocess_runner.go), render the error but
	// KEEP the step_end buffered so it can still merge with the
	// node_end that is about to arrive. Otherwise a single-step
	// failed node leaves a redundant stand-alone `✗ <step> (dur)`
	// line wedged between the error and the node_end. The match is
	// keyed on (node, step==step_end's Msg) so an unrelated error
	// from a different step still flushes normally.
	if p.pendingStepEnd != nil &&
		rec.Level == "error" &&
		rec.Node == p.pendingStepEnd.Node &&
		(rec.Step == "" || rec.Step == p.pendingStepEnd.Msg) &&
		rec.Event == "" {
		fmt.Fprint(sink, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(sink, p.levelize(rec.Level, rec.Msg))
		return
	}

	if rec.Event == "run_plan" && p.pendingRunStart != nil {
		// Merge: render the unified Setup block with both records.
		// Doing this before flushPending is what keeps the Setup
		// section from getting split across two rules (one with
		// run/pipeline rows, another with just the Plan).
		p.writeSetupBlock(sink, p.pendingRunStart, &rec)
		p.pendingRunStart = nil
		return
	}

	if rec.Event == "node_end" && p.pendingStepEnd != nil && p.pendingStepEnd.Node == rec.Node {
		// node_end's outcome wins for the glyph (it's the authoritative
		// node-level result); the step name from pendingStepEnd is
		// appended as a dim suffix so the operator sees what the node
		// was doing without a redundant step_end line above.
		outcome, _ := rec.Attrs["outcome"].(string)
		durMS, _ := asMillis(rec.Attrs["duration_ms"])
		glyph, code := outcomeIcon(outcome)
		nh := p.hueFor(rec.Node)
		line := p.color(glyph, code) + " " + p.color(rec.Node, nh) +
			p.color(" › ", ansiDim) + p.color(p.pendingStepEnd.Msg, ansiDim)
		if durMS > 0 {
			line += " " + p.color(fmt.Sprintf("(%s)", fmtDuration(durMS)), code)
		}
		fmt.Fprintln(sink, line)
		// Surface the structured error from step_end.attrs.error
		// directly under the merged line. This replaces the runner's
		// previous inline-error log emission (now removed to avoid
		// duplication in JSON output): the error is read from the
		// step_end record's attrs and rendered once, here, where the
		// human eye is already focused on the failure marker.
		if errMsg, ok := p.pendingStepEnd.Attrs["error"].(string); ok && errMsg != "" {
			for _, errLine := range strings.Split(strings.TrimRight(errMsg, "\n"), "\n") {
				fmt.Fprintln(sink, "    "+p.color(errLine, ansiRed))
			}
		}
		p.pendingStepEnd = nil
		return
	}

	// Any other event flushes the pending buffers (rendered as their
	// stand-alone forms) before being handled.
	p.flushPending(sink)

	switch rec.Event {
	case "node_start":
		p.nodeStart[rec.Node] = rec.TS
		// Buffer; flushed by the next event (collapsed if it's
		// step_start for the same node, stand-alone otherwise).
		recCopy := rec
		p.pendingNodeStart = &recCopy
		return
	case "node_end":
		outcome, _ := rec.Attrs["outcome"].(string)
		durMS, _ := rec.Attrs["duration_ms"].(int64)
		if durMS == 0 {
			if v, ok := rec.Attrs["duration_ms"].(float64); ok {
				durMS = int64(v)
			}
		}
		icon, code := outcomeIcon(outcome)
		head := p.color(icon, code)
		name := p.color(rec.Node, nodeHue)
		tail := p.color(fmt.Sprintf("(%s)", fmtDuration(durMS)), code)
		fmt.Fprintln(sink, head+" "+name+" "+tail)
	case "step_start":
		p.writeStepStart(sink, rec, nodeHue)
	case "step_end":
		// Buffer; if the next event is node_end for the same node we
		// absorb this into a combined node-end line, otherwise the
		// next flush renders it stand-alone.
		recCopy := rec
		p.pendingStepEnd = &recCopy
		return
	case "step_skipped":
		p.writeStepSkipped(sink, rec, nodeHue)
	case "retry":
		fmt.Fprintln(sink, p.color(fmt.Sprintf("  ↻ %s", rec.Msg), ansiYellow))
	case "exec_line":
		fmt.Fprint(sink, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(sink, rec.Msg)
	case "run_start":
		// Buffer; the trailing run_plan event folds the DAG into the
		// same Setup block. Lone run_starts (no plan emitted) flush
		// from flushPending as a Plan-less header.
		recCopy := rec
		p.pendingRunStart = &recCopy
		return
	case "run_plan":
		p.writeSetupBlock(sink, p.pendingRunStart, &rec)
		p.pendingRunStart = nil
	case "run_summary":
		// Buffer; the trailing run_finish event folds its run-id,
		// status, hints, and run-level error into the same block.
		recCopy := rec
		p.pendingRunSummary = &recCopy
		return
	case "run_finish":
		p.writeRunBlock(sink, p.pendingRunSummary, &rec)
		p.pendingRunSummary = nil
	default:
		fmt.Fprint(sink, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(sink, p.levelize(rec.Level, rec.Msg))
	}
}

// flushPending renders any buffered node_start / step_end records as
// their stand-alone forms. Called from Emit before handling a record
// that does not match the merge candidates above. Must be called with
// p.mu held.
func (p *PrettyRenderer) flushPending(sink io.Writer) {
	if p.pendingRunStart != nil {
		// run_plan never arrived (plan-build failure or truncated
		// stream). Render the Setup block without a Plan section so
		// the run-id and tips still appear -- losing the DAG is
		// strictly less bad than losing the section entirely.
		p.writeSetupBlock(sink, p.pendingRunStart, nil)
		p.pendingRunStart = nil
	}
	if p.pendingNodeStart != nil {
		nh := p.hueFor(p.pendingNodeStart.Node)
		fmt.Fprintln(sink, p.color("▶ "+p.pendingNodeStart.Node, ansiBold+nh))
		p.pendingNodeStart = nil
	}
	if p.pendingStepEnd != nil {
		nh := p.hueFor(p.pendingStepEnd.Node)
		p.writeStepEnd(sink, *p.pendingStepEnd, nh)
		p.pendingStepEnd = nil
	}
}

// Flush drains any buffered events to the writer. Callers that render
// a single record at a time (HTTP pretty-printers, batched log
// rendering) must invoke this at end-of-stream so a buffered node_start
// or step_end is not silently dropped when no follow-up event arrives.
// Streaming live runs reach the same drain naturally via run_finish.
func (p *PrettyRenderer) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushPending(p.w)
	if p.pendingRunSummary != nil {
		// No run_finish arrived (truncated stream / log file without
		// the terminal envelope event). Render the summary block on
		// its own so the operator still sees it.
		p.writeRunBlock(p.w, p.pendingRunSummary, nil)
		p.pendingRunSummary = nil
	}
}

// breadcrumb renders "<node> › <parent>... › <job> › <step> │ ".
// Step is the leaf so a reader's eye lands on the most-specific
// frame just before the message; Job/JobStack frames are ancestors
// of either the step or the message itself when there's no step.
func (p *PrettyRenderer) breadcrumb(rec sparkwing.LogRecord, nodeHue string) string {
	if rec.Node == "" && rec.Job == "" && rec.Step == "" {
		return ""
	}
	var b strings.Builder
	if rec.Node != "" {
		b.WriteString(p.color(rec.Node, nodeHue))
	}
	for _, frame := range rec.JobStack {
		b.WriteString(p.color(" › ", ansiDim))
		b.WriteString(p.color(frame, ansiDim))
	}
	if rec.Job != "" {
		b.WriteString(p.color(" › ", ansiDim))
		b.WriteString(p.color(rec.Job, ansiDim))
	}
	if rec.Step != "" {
		b.WriteString(p.color(" › ", ansiDim))
		b.WriteString(p.color(rec.Step, ansiDim))
	}
	b.WriteString(p.color(" │ ", ansiDim))
	return b.String()
}

func (p *PrettyRenderer) levelize(level, msg string) string {
	switch level {
	case "error":
		return p.color("ERROR ", ansiRed+ansiBold) + msg
	case "warn":
		return p.color("WARN  ", ansiYellow) + msg
	case "debug":
		return p.color("DEBUG "+msg, ansiDim)
	default:
		return msg
	}
}

// writePlan renders the DAG as a topologically-sorted node list.
func (p *PrettyRenderer) writePlan(w io.Writer, rec sparkwing.LogRecord) {
	raw, _ := rec.Attrs["nodes"].([]any)
	type stepRow struct {
		id     string
		deps   []string
		groups []string
	}
	type node struct {
		id        string
		deps      []string
		groupDeps []string
		inline    bool
		dynamic   bool
		approval  bool
		groups    []string
		steps     []stepRow
	}
	byID := make(map[string]*node, len(raw))
	order := make([]*node, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		// deps is []string in-process, []any after JSON roundtrip.
		var deps []string
		switch d := m["deps"].(type) {
		case []string:
			deps = append(deps, d...)
		case []any:
			for _, x := range d {
				if s, ok := x.(string); ok {
					deps = append(deps, s)
				}
			}
		}
		inline, _ := m["inline"].(bool)
		dynamic, _ := m["dynamic"].(bool)
		approval, _ := m["approval"].(bool)
		var groups []string
		switch g := m["groups"].(type) {
		case []string:
			groups = append(groups, g...)
		case []any:
			for _, x := range g {
				if s, ok := x.(string); ok {
					groups = append(groups, s)
				}
			}
		}
		var groupDeps []string
		switch d := m["group_deps"].(type) {
		case []string:
			groupDeps = append(groupDeps, d...)
		case []any:
			for _, x := range d {
				if s, ok := x.(string); ok {
					groupDeps = append(groupDeps, s)
				}
			}
		}
		// run_plan delivers `steps` as []map[string]any in-process and
		// as []any post-JSON-roundtrip; accept both shapes.
		var rawSteps []map[string]any
		switch v := m["steps"].(type) {
		case []map[string]any:
			rawSteps = v
		case []any:
			for _, x := range v {
				if sm, ok := x.(map[string]any); ok {
					rawSteps = append(rawSteps, sm)
				}
			}
		}
		var steps []stepRow
		for _, sm := range rawSteps {
			sid, _ := sm["id"].(string)
			sr := stepRow{id: sid}
			switch d := sm["deps"].(type) {
			case []string:
				sr.deps = append(sr.deps, d...)
			case []any:
				for _, x := range d {
					if s, ok := x.(string); ok {
						sr.deps = append(sr.deps, s)
					}
				}
			}
			switch g := sm["groups"].(type) {
			case []string:
				sr.groups = append(sr.groups, g...)
			case []any:
				for _, x := range g {
					if s, ok := x.(string); ok {
						sr.groups = append(sr.groups, s)
					}
				}
			}
			steps = append(steps, sr)
		}
		n := &node{id: id, deps: deps, groupDeps: groupDeps, inline: inline, dynamic: dynamic, approval: approval, groups: groups, steps: steps}
		byID[id] = n
		order = append(order, n)
	}
	if len(order) == 0 {
		return
	}

	// Group deps (ExpandFrom fan-in) contribute to in-degree like
	// static deps; otherwise tests-post sorts before its predecessors.
	indeg := make(map[string]int, len(order))
	children := make(map[string][]string, len(order))
	for _, n := range order {
		indeg[n.id] = len(n.deps) + len(n.groupDeps)
		for _, d := range n.deps {
			children[d] = append(children[d], n.id)
		}
		for _, d := range n.groupDeps {
			children[d] = append(children[d], n.id)
		}
	}
	sorted := make([]*node, 0, len(order))
	var queue []string
	for _, n := range order {
		if indeg[n.id] == 0 {
			queue = append(queue, n.id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if n, ok := byID[id]; ok {
			sorted = append(sorted, n)
		}
		for _, c := range children[id] {
			indeg[c]--
			if indeg[c] == 0 {
				queue = append(queue, c)
			}
		}
	}
	// Cycle fallback: leftover nodes appended in original order.
	if len(sorted) < len(order) {
		seen := make(map[string]bool, len(sorted))
		for _, n := range sorted {
			seen[n.id] = true
		}
		for _, n := range order {
			if !seen[n.id] {
				sorted = append(sorted, n)
			}
		}
	}

	// Capped so a long name can't push arrows off the right edge.
	maxName := 0
	for _, n := range sorted {
		maxName = max(maxName, len(n.id))
	}
	maxName = min(maxName, 40)

	fmt.Fprintln(w, p.color(fmt.Sprintf("Plan (%d nodes)", len(sorted)), ansiBold))
	for _, n := range sorted {
		name := p.color(fmt.Sprintf("%-*s", maxName, n.id), p.hueFor(n.id))
		var arrow string
		// Group deps get a trailing `*`.
		depParts := make([]string, 0, len(n.deps)+len(n.groupDeps))
		for _, d := range n.deps {
			depParts = append(depParts, p.color(d, p.hueFor(d)))
		}
		for _, d := range n.groupDeps {
			depParts = append(depParts, p.color(d+"*", p.hueFor(d)))
		}
		if len(depParts) > 0 {
			arrow = "  " + p.color("←", ansiDim) + " " + strings.Join(depParts, p.color(", ", ansiDim))
		}
		var tags string
		if n.inline {
			tags += "  " + p.color("[inline]", ansiDim)
		}
		if n.dynamic {
			tags += "  " + p.rainbow("[dynamic]")
		}
		if n.approval {
			tags += "  " + p.color("[approval]", ansiBold+ansiYellow)
		}
		if len(n.groups) > 0 {
			tags += "  " + p.color("(group: "+strings.Join(n.groups, ",")+")", ansiDim)
		}
		fmt.Fprintln(w, "  "+p.color("●", p.hueFor(n.id))+" "+name+arrow+tags)
		if len(n.steps) > 0 {
			stepMaxName := 0
			for _, s := range n.steps {
				stepMaxName = max(stepMaxName, len(s.id))
			}
			stepMaxName = min(stepMaxName, 40)
			for _, s := range n.steps {
				sname := p.color(fmt.Sprintf("%-*s", stepMaxName, s.id), p.hueFor(n.id))
				var sarrow string
				if len(s.deps) > 0 {
					deps := make([]string, len(s.deps))
					for i, d := range s.deps {
						deps[i] = p.color(d, p.hueFor(n.id))
					}
					sarrow = "  " + p.color("←", ansiDim) + " " + strings.Join(deps, p.color(", ", ansiDim))
				}
				var stags string
				if len(s.groups) > 0 {
					stags += "  " + p.color("(group: "+strings.Join(s.groups, ",")+")", ansiDim)
				}
				fmt.Fprintln(w, "    "+p.color("└", ansiDim)+" "+sname+sarrow+stags)
			}
		}
	}
}

// rainbow paints each rune of s a different palette hue.
func (p *PrettyRenderer) rainbow(s string) string {
	if !p.useColor {
		return s
	}
	var b strings.Builder
	i := 0
	for _, r := range s {
		b.WriteString(nodePalette[i%len(nodePalette)])
		b.WriteRune(r)
		b.WriteString(ansiReset)
		i++
	}
	return b.String()
}

// writeRunBlock renders the unified end-of-run section, folding the
// (optional) run_summary record and the run_finish record into a
// single bracketed block so the operator's eye doesn't have to track
// two separate output groups. Either input may be nil:
//
//   - summary nil, finish set: a thin block with just run-id, status,
//     and action hints (no per-node table because the orchestrator
//     never emitted a summary -- e.g., a failure before plan dispatch).
//   - summary set, finish nil: drained from Flush at end-of-stream
//     when the run was truncated; renders the table without hints.
const runBlockWidth = 60

// sectionRule renders a horizontal rule with a left-aligned label
// embedded after three leading dashes:  "─── Setup ────────...".
// Each labeled rule both closes the previous section and opens the
// new one -- there is exactly one rule line between adjacent
// sections, never a stacked pair. An empty label produces a plain
// rule (used as the trailing close after the last section).
func (p *PrettyRenderer) sectionRule(label string) string {
	const lead = 3
	if label == "" {
		return p.color(strings.Repeat("─", runBlockWidth), ansiDim)
	}
	mid := " " + label + " "
	pad := runBlockWidth - lead - len([]rune(mid))
	if pad < 0 {
		pad = 0
	}
	return p.color(strings.Repeat("─", lead), ansiDim) +
		p.color(mid, ansiBold) +
		p.color(strings.Repeat("─", pad), ansiDim)
}

func (p *PrettyRenderer) writeRunBlock(w io.Writer, summary, finish *sparkwing.LogRecord) {
	// Header rows: run-id and status come from finish if present,
	// otherwise fall back to summary. Status icon is colored to
	// match the outcome so a failed run reads red without needing
	// to read the word.
	var runID, status string
	var dms int64
	if finish != nil {
		runID, _ = finish.Attrs["run_id"].(string)
		status, _ = finish.Attrs["status"].(string)
		if v, ok := asMillis(finish.Attrs["duration_ms"]); ok {
			dms = v
		}
	}
	if summary != nil {
		if status == "" {
			status, _ = summary.Attrs["status"].(string)
		}
		if dms == 0 {
			if v, ok := asMillis(summary.Attrs["duration_ms"]); ok {
				dms = v
			}
		}
	}
	statusIcon, statusCode := summaryStatusIcon(status)

	fmt.Fprintln(w)
	fmt.Fprintln(w, p.sectionRule("Summary"))
	if runID != "" {
		fmt.Fprintln(w, "  "+p.color("run    ", ansiDim)+" "+runID)
	}
	if status != "" {
		statusLine := p.color(statusIcon+" "+status, statusCode)
		if dms > 0 {
			statusLine += " " + p.color("("+fmtDuration(dms)+")", ansiDim)
		}
		fmt.Fprintln(w, "  "+p.color("status ", ansiDim)+" "+statusLine)
	}

	// Collect per-node errors here while rendering the table so the
	// trailing Errors section can render them with their node tag.
	type nodeErr struct {
		nodeID string
		errMsg string
	}
	var errs []nodeErr

	if summary != nil {
		nodes, _ := summary.Attrs["nodes"].([]any)
		tally := summaryTally(nodes)
		if tally.total > 1 {
			fmt.Fprintln(w, "  "+p.color("nodes  ", ansiDim)+" "+tally.format(p))
		}

		if len(nodes) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, p.color("Jobs", ansiBold))
			for _, n := range nodes {
				m, ok := n.(map[string]any)
				if !ok {
					continue
				}
				p.writeRunBlockNodeRow(w, m)
				if msg, ok := m["error"].(string); ok && msg != "" {
					id, _ := m["id"].(string)
					errs = append(errs, nodeErr{nodeID: id, errMsg: msg})
				}
			}
		}
	}

	// Errors section: dedicated heading + indented bodies. Each
	// error's first line gets a `<node> › <step> │` breadcrumb that
	// matches the streaming-log breadcrumb shape -- so the same
	// failure surfaces with the same address in both views. When the
	// error string starts with `step "X": ` we lift X into the
	// breadcrumb and strip the prefix from the body to avoid
	// repeating it.
	if len(errs) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("Errors", ansiBold+ansiRed))
		for _, e := range errs {
			stepID, body := splitStepErrorPrefix(e.errMsg)
			lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
			crumb := p.color(e.nodeID, p.hueFor(e.nodeID))
			if stepID != "" {
				crumb += p.color(" › ", ansiDim) + p.color(stepID, ansiDim)
			}
			crumb += p.color(" │ ", ansiDim)
			for i, line := range lines {
				prefix := "  "
				if i == 0 {
					fmt.Fprintln(w, prefix+crumb+p.color(line, ansiRed))
				} else {
					fmt.Fprintln(w, prefix+"  "+p.color(line, ansiRed))
				}
			}
		}
	}

	// Run-level error (e.g. "nodes failed: [build test deploy]") --
	// surfaced as its own labeled row when it's distinct from the
	// per-node errors. Skipped when the message just enumerates the
	// nodes we already listed above.
	if finish != nil {
		if errMsg, ok := finish.Attrs["error"].(string); ok && errMsg != "" && !strings.HasPrefix(errMsg, "nodes failed:") {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "  "+p.color("error  ", ansiDim)+" "+p.color(errMsg, ansiRed))
		}
	}

	// Tips section: column-0 heading inside the Summary block so the
	// "what to do next" hints share the same bracketed scope as the
	// outcome (operator + agent both find them there). The duplicate
	// hints in the Setup block at run start are intentional -- this
	// is the post-run reference copy.
	if runID != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("Tips", ansiBold))
		fmt.Fprintln(w, "  "+p.color("status ", ansiDim)+" "+p.color("sparkwing runs status --run "+runID, ansiCyan))
		fmt.Fprintln(w, "  "+p.color("logs   ", ansiDim)+" "+p.color("sparkwing runs logs --run "+runID, ansiCyan))
		if status == "failed" {
			fmt.Fprintln(w, "  "+p.color("retry  ", ansiDim)+" "+p.color("sparkwing runs retry --run "+runID, ansiCyan))
		}
	}

	fmt.Fprintln(w, p.sectionRule(""))
}

// writeRunBlockNodeRow renders one node row inside the Summary block.
// Format: glyph, padded name, outcome word (for every outcome other
// than plain success, since the glyph alone is ambiguous), duration.
// Per-node error rendering moved to the dedicated Errors section.
func (p *PrettyRenderer) writeRunBlockNodeRow(w io.Writer, m map[string]any) {
	id, _ := m["id"].(string)
	oc, _ := m["outcome"].(string)
	nodeIcon, nodeCode := outcomeIcon(oc)
	const nameWidth = 24
	line := fmt.Sprintf("  %s %s",
		p.color(nodeIcon, nodeCode),
		p.color(fmt.Sprintf("%-*s", nameWidth, id), p.hueFor(id)),
	)
	// Outcome word: shown for every outcome except plain success.
	// "success" leaves a clean ✓ <name> <dur> line; everything else
	// (cached, failed, skipped, cancelled, superseded) gets the word
	// so a glance at the row tells the operator what happened.
	if oc != "" && oc != "success" {
		line += "  " + p.color(oc, nodeCode)
	}
	if dms, ok := asMillis(m["duration_ms"]); ok && dms > 0 {
		line += "  " + p.color(fmtDuration(dms), ansiDim)
	}
	if dyn, _ := m["dynamic"].(bool); dyn {
		line += "  " + p.rainbow("[dynamic]")
	}
	fmt.Fprintln(w, line)
}

// summaryCounts holds the per-outcome tally rendered above the per-
// node rows in the summary section.
type summaryCounts struct {
	total   int
	passed  int
	failed  int
	skipped int
	other   int // anything not in the buckets above (cancelled, superseded, etc.)
}

func summaryTally(nodes []any) summaryCounts {
	var c summaryCounts
	for _, n := range nodes {
		m, ok := n.(map[string]any)
		if !ok {
			continue
		}
		c.total++
		oc, _ := m["outcome"].(string)
		switch oc {
		case "success", "cached":
			c.passed++
		case "failed":
			c.failed++
		case "skipped", "skipped-concurrent", "cancelled":
			// Cancelled bundles with skipped here -- both mean the
			// node didn't execute its body. The per-row outcome word
			// still distinguishes the two for the operator.
			c.skipped++
		default:
			c.other++
		}
	}
	return c
}

func (c summaryCounts) format(p *PrettyRenderer) string {
	parts := []string{fmt.Sprintf("%d node%s", c.total, pluralS(c.total))}
	if c.passed > 0 {
		parts = append(parts, p.color(fmt.Sprintf("%d passed", c.passed), ansiGreen))
	}
	if c.failed > 0 {
		parts = append(parts, p.color(fmt.Sprintf("%d failed", c.failed), ansiRed))
	}
	if c.skipped > 0 {
		parts = append(parts, p.color(fmt.Sprintf("%d skipped", c.skipped), ansiDim))
	}
	if c.other > 0 {
		parts = append(parts, p.color(fmt.Sprintf("%d other", c.other), ansiYellow))
	}
	return strings.Join(parts, p.color(" · ", ansiDim))
}

// pluralS returns "s" unless n == 1.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// anyMap normalizes a record-attr map field into map[string]any. The
// SDK emits these as map[string]string or map[string]any in-process;
// after a JSONL roundtrip they're always map[string]any. Returns nil
// for missing/wrong-type fields so callers can guard with len().
func anyMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, vv := range m {
			out[k] = vv
		}
		return out
	}
	return nil
}

// anySlice normalizes a record-attr slice field. Same shape concerns
// as anyMap (in-process []string vs post-JSONL []any).
func anySlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []string:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	}
	return nil
}

// sortedKeys returns the keys of m in lexical order so renderings
// are stable across invocations.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// padRight right-pads s with spaces to width w. Used to align the
// dim-colored label column inside a Setup sub-section.
func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// formatAttr renders a single attr value for human display. Strings
// pass through; bools/numbers stringify; collections stringify via
// fmt.Sprint. Empty strings show as the dim placeholder "(empty)" so
// `--start-at=` doesn't render as a bare row.
func formatAttr(v any) string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return "(empty)"
		}
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return "(none)"
	default:
		return fmt.Sprint(v)
	}
}

// shortHash trims a `sha256:<hex>` to its first 12 hex chars for
// human display. The full value remains in attrs for agent
// comparisons; the renderer only needs enough to eyeball "is this
// the same hash I saw before?".
func shortHash(h string) string {
	const prefix = "sha256:"
	if !strings.HasPrefix(h, prefix) {
		return h
	}
	hex := h[len(prefix):]
	if len(hex) <= 12 {
		return h
	}
	return prefix + hex[:12]
}

// splitStepErrorPrefix lifts a `step "X": ` prefix off an error string
// so the renderer can put the step ID in the breadcrumb instead of
// repeating it inside the body. Returns ("", original) when the
// prefix isn't present (run-level errors, hook errors, etc.). The
// step error format is the one written by sparkwing.StepError.Error
// over in the SDK.
func splitStepErrorPrefix(s string) (stepID, body string) {
	const prefix = `step "`
	if !strings.HasPrefix(s, prefix) {
		return "", s
	}
	rest := s[len(prefix):]
	end := strings.Index(rest, `": `)
	if end < 0 {
		return "", s
	}
	return rest[:end], rest[end+len(`": `):]
}

// writeStepStart renders a `step_start` event as `  ● <name>` (or with
// the breadcrumb prefix when the step originated inside a Job spawn,
// which is the only case that carries a non-empty rec.Job).
func (p *PrettyRenderer) writeStepStart(w io.Writer, rec sparkwing.LogRecord, nodeHue string) {
	glyph := "●"
	code := ansiBlue
	if rec.Job != "" {
		fmt.Fprint(w, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(w, p.color(glyph+" "+rec.Msg, code))
		return
	}
	fmt.Fprintln(w, p.color(fmt.Sprintf("  %s %s", glyph, rec.Msg), code))
}

// writeStepEnd renders a `step_end` event with the outcome glyph and
// duration. Success/cached steps render dim (the operator already saw
// step_start; this line just marks completion); failed steps render
// red so the failure point is obvious in a long log. When attrs.error
// is set the message is rendered indented underneath, so a failed
// step in a multi-step node carries its own failure body inline
// instead of relying on the run-summary section to surface it.
func (p *PrettyRenderer) writeStepEnd(w io.Writer, rec sparkwing.LogRecord, nodeHue string) {
	outcome, _ := rec.Attrs["outcome"].(string)
	glyph, code := outcomeIcon(outcome)
	if outcome == "success" || outcome == "cached" {
		code = ansiDim
	}
	dms, _ := asMillis(rec.Attrs["duration_ms"])
	tail := ""
	if dms > 0 {
		tail = " " + p.color("("+fmtDuration(dms)+")", ansiDim)
	}
	if rec.Job != "" {
		fmt.Fprint(w, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(w, p.color(glyph+" "+rec.Msg, code)+tail)
	} else {
		fmt.Fprintln(w, p.color(fmt.Sprintf("  %s %s", glyph, rec.Msg), code)+tail)
	}
	if errMsg, ok := rec.Attrs["error"].(string); ok && errMsg != "" {
		for _, errLine := range strings.Split(strings.TrimRight(errMsg, "\n"), "\n") {
			fmt.Fprintln(w, "      "+p.color(errLine, ansiRed))
		}
	}
}

// writeStepSkipped renders a `step_skipped` event with the optional
// reason in brackets so a `--start-at` filter is distinguishable from
// a user-authored SkipIf predicate.
func (p *PrettyRenderer) writeStepSkipped(w io.Writer, rec sparkwing.LogRecord, nodeHue string) {
	glyph, code := outcomeIcon("skipped")
	reason, _ := rec.Attrs["reason"].(string)
	tail := ""
	if reason != "" {
		tail = " " + p.color("["+reason+"]", ansiDim)
	}
	if rec.Job != "" {
		fmt.Fprint(w, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(w, p.color(glyph+" "+rec.Msg, code)+tail)
		return
	}
	fmt.Fprintln(w, p.color(fmt.Sprintf("  %s %s", glyph, rec.Msg), code)+tail)
}

// writeSetupBlock renders the labeled pre-run section combining
// run_start (run-id, pipeline, binary source) with the run_plan DAG
// and live-tail hints. The block is delimited by a "─── Setup ───"
// header rule and a "─── Logs ───" trailing rule -- the latter both
// closes Setup and opens the streaming Logs section. Either input
// may be nil:
//
//   - run_start nil, plan set: replay paths only.
//   - run_start set, plan nil: lone run_start that never got a plan
//     (e.g. plan-build failure); renders Setup + Tips, skips Plan.
func (p *PrettyRenderer) writeSetupBlock(w io.Writer, runStart, plan *sparkwing.LogRecord) {
	if runStart == nil && plan == nil {
		return
	}

	var runID, pipeline, binarySrc, cwd, inputsHash, reproducer string
	var args map[string]any
	var flags map[string]any
	var triggerEnvKeys []any
	if runStart != nil {
		runID, _ = runStart.Attrs["run_id"].(string)
		pipeline, _ = runStart.Attrs["pipeline"].(string)
		binarySrc, _ = runStart.Attrs["binary_source"].(string)
		cwd, _ = runStart.Attrs["cwd"].(string)
		inputsHash, _ = runStart.Attrs["inputs_hash"].(string)
		reproducer, _ = runStart.Attrs["reproducer"].(string)
		// args / flags arrive as map[string]any after JSON roundtrip,
		// or map[string]string / map[string]any in-process. Normalize
		// so the renderer doesn't have to branch.
		args = anyMap(runStart.Attrs["args"])
		flags = anyMap(runStart.Attrs["flags"])
		triggerEnvKeys = anySlice(runStart.Attrs["trigger_env_keys"])
	}
	var planHash string
	if plan != nil {
		planHash, _ = plan.Attrs["plan_hash"].(string)
	}

	fmt.Fprintln(w, p.sectionRule("Setup"))
	if runID != "" {
		fmt.Fprintln(w, "  "+p.color("run     ", ansiDim)+" "+runID)
	}
	if pipeline != "" {
		fmt.Fprintln(w, "  "+p.color("pipeline", ansiDim)+" "+pipeline)
	}
	if binarySrc != "" {
		fmt.Fprintln(w, "  "+p.color("binary  ", ansiDim)+" "+binarySrc)
	}
	if cwd != "" {
		fmt.Fprintln(w, "  "+p.color("cwd     ", ansiDim)+" "+cwd)
	}
	// Hashes share a single labeled row each. Trim the sha256: prefix
	// for display so the column lines up; full value is in attrs.
	if inputsHash != "" {
		fmt.Fprintln(w, "  "+p.color("inputs  ", ansiDim)+" "+shortHash(inputsHash))
	}
	if planHash != "" {
		fmt.Fprintln(w, "  "+p.color("plan    ", ansiDim)+" "+shortHash(planHash))
	}
	if reproducer != "" {
		fmt.Fprintln(w, "  "+p.color("rerun   ", ansiDim)+" "+p.color(reproducer, ansiCyan))
	}

	// Args and flags as their own sub-sections so a reader can
	// reproduce the run by transcribing the values back onto the
	// command line. Trigger-env shows names only -- values may carry
	// secrets and aren't safe to surface in either display or JSON.
	if len(args) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("Args", ansiBold))
		for _, k := range sortedKeys(args) {
			fmt.Fprintln(w, "  "+p.color(padRight(k, 8), ansiDim)+" "+formatAttr(args[k]))
		}
	}
	if len(flags) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("Flags", ansiBold))
		// Auto-size the label column so allow_destructive (longest
		// real flag name today) and shorter names line up cleanly.
		labelW := 0
		for k := range flags {
			if len(k) > labelW {
				labelW = len(k)
			}
		}
		for _, k := range sortedKeys(flags) {
			fmt.Fprintln(w, "  "+p.color(padRight(k, labelW), ansiDim)+" "+formatAttr(flags[k]))
		}
	}
	if len(triggerEnvKeys) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("Env", ansiBold)+p.color("  (names only; values omitted)", ansiDim))
		for _, k := range triggerEnvKeys {
			if s, ok := k.(string); ok {
				fmt.Fprintln(w, "  "+s)
			}
		}
	}

	if plan != nil {
		fmt.Fprintln(w)
		p.writePlan(w, *plan)
	}

	if runID != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("Tips", ansiBold))
		fmt.Fprintln(w, "  "+p.color("follow ", ansiDim)+" "+p.color("sparkwing runs logs --run "+runID+" --follow", ansiCyan))
		fmt.Fprintln(w, "  "+p.color("status ", ansiDim)+" "+p.color("sparkwing runs status --run "+runID, ansiCyan))
	}
	// Trailing rule labeled "Logs" -- this is the opening rule of
	// the streaming logs section, so the streaming records flow
	// directly under it without a stacked rule pair.
	fmt.Fprintln(w, p.sectionRule("Logs"))
}

func outcomeIcon(outcome string) (icon string, code string) {
	switch outcome {
	case "success", "Success":
		return "✓", ansiGreen
	case "failed", "Failed":
		return "✗", ansiRed
	case "skipped", "Skipped":
		return "⊘", ansiDim
	case "skipped-concurrent":
		// OnLimit:Skip; distinct from SkipIf's skipped.
		return "⊚", ansiDim
	case "cached", "Cached":
		return "◈", ansiCyan
	case "cancelled", "Cancelled":
		return "⊘", ansiYellow
	case "superseded":
		// OnLimit:CancelOthers eviction.
		return "⟳", ansiYellow
	default:
		return "·", ""
	}
}

func summaryStatusIcon(status string) (icon string, code string) {
	switch status {
	case "success":
		return "✓", ansiGreen
	case "failed":
		return "✗", ansiRed
	default:
		return "·", ""
	}
}

func fmtDuration(ms int64) string {
	ms = max(ms, 0)
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	secs := float64(ms) / 1000.0
	if secs < 60 {
		return fmt.Sprintf("%.1fs", secs)
	}
	mins := int(secs) / 60
	rem := int(secs) % 60
	return fmt.Sprintf("%dm%02ds", mins, rem)
}

func asMillis(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	}
	return 0, false
}

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

// StripANSI removes ANSI CSI/SGR escape sequences from s.
func StripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				if c >= 0x40 && c <= 0x7e {
					j++
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
