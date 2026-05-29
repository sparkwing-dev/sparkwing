// Package logpretty renders sparkwing.LogRecord streams as
// TTY-friendly output and provides a small set of helpers
// (StripANSI, markdown summary rendering) the dashboard pulls in.
//
// Extracted from internal/orchestrator so thin binaries (sparkwing-web
// in particular) can render logs without dragging in the dispatch
// engine.
package logpretty

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

	pendingNodeStart  *sparkwing.LogRecord
	pendingStepEnd    *sparkwing.LogRecord
	pendingRunSummary *sparkwing.LogRecord
	pendingRunStart   *sparkwing.LogRecord
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

var nodePalette = []string{
	"\x1b[38;5;214m",
	"\x1b[38;5;117m",
	"\x1b[38;5;114m",
	"\x1b[38;5;212m",
	"\x1b[38;5;208m",
	"\x1b[38;5;141m",
	"\x1b[38;5;173m",
	"\x1b[38;5;109m",
	"\x1b[38;5;183m",
	"\x1b[38;5;115m",
	"\x1b[38;5;174m",
	"\x1b[38;5;147m",
	"\x1b[38;5;178m",
	"\x1b[38;5;108m",
	"\x1b[38;5;176m",
	"\x1b[38;5;202m",
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
	nodeHue := p.hueFor(rec.JobID)

	if rec.Event == "step_start" && p.pendingNodeStart != nil && p.pendingNodeStart.JobID == rec.JobID {
		nh := p.hueFor(p.pendingNodeStart.JobID)
		head := p.color("▶ "+p.pendingNodeStart.JobID, ansiBold+nh)
		stepGlyph := p.color("●", ansiBlue)
		stepName := p.color(rec.Msg, ansiBlue)
		fmt.Fprintln(sink, head+"  "+stepGlyph+" "+stepName)
		p.pendingNodeStart = nil
		return
	}
	if p.pendingStepEnd != nil &&
		rec.Level == "error" &&
		rec.JobID == p.pendingStepEnd.JobID &&
		(rec.Step == "" || rec.Step == p.pendingStepEnd.Msg) &&
		rec.Event == "" {
		fmt.Fprint(sink, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(sink, p.levelize(rec.Level, rec.Msg))
		return
	}

	if rec.Event == "run_plan" && p.pendingRunStart != nil {
		p.writeSetupBlock(sink, p.pendingRunStart, &rec)
		p.pendingRunStart = nil
		return
	}

	if rec.Event == "node_end" && p.pendingStepEnd != nil && p.pendingStepEnd.JobID == rec.JobID {
		outcome, _ := rec.Attrs["outcome"].(string)
		durMS, _ := asMillis(rec.Attrs["duration_ms"])
		glyph, code := outcomeIcon(outcome)
		nh := p.hueFor(rec.JobID)
		line := p.color(glyph, code) + " " + p.color(rec.JobID, nh) +
			p.color(" › ", ansiDim) + p.color(p.pendingStepEnd.Msg, ansiDim)
		if durMS > 0 {
			line += " " + p.color(fmt.Sprintf("(%s)", fmtDuration(durMS)), code)
		}
		fmt.Fprintln(sink, line)
		if errMsg, ok := p.pendingStepEnd.Attrs["error"].(string); ok && errMsg != "" {
			for _, errLine := range strings.Split(strings.TrimRight(errMsg, "\n"), "\n") {
				fmt.Fprintln(sink, "    "+p.color(errLine, ansiRed))
			}
		}
		p.pendingStepEnd = nil
		return
	}

	p.flushPending(sink)

	switch rec.Event {
	case "node_start":
		p.nodeStart[rec.JobID] = rec.TS
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
		name := p.color(rec.JobID, nodeHue)
		tail := p.color(fmt.Sprintf("(%s)", fmtDuration(durMS)), code)
		fmt.Fprintln(sink, head+" "+name+" "+tail)
	case "step_start":
		p.writeStepStart(sink, rec, nodeHue)
	case "step_end":
		recCopy := rec
		p.pendingStepEnd = &recCopy
		return
	case "step_skipped":
		p.writeStepSkipped(sink, rec, nodeHue)
	case "retry":
		fmt.Fprintln(sink, p.color(fmt.Sprintf("  ↻ %s", rec.Msg), ansiYellow))
	case "approval_requested":
		fmt.Fprintln(sink, "  "+p.color("⏸ approval requested", ansiBold+ansiYellow)+p.color(" › "+rec.Msg, ansiDim))
	case "approval_resolved":
		resolution, _ := rec.Attrs["resolution"].(string)
		via, _ := rec.Attrs["via"].(string)
		icon, code := "🔓", ansiGreen
		switch resolution {
		case "denied":
			icon, code = "✗", ansiRed
		case "timed_out":
			icon, code = "⏱", ansiRed
		}
		head := p.color(icon+" "+rec.Msg, ansiBold+code)
		if via != "" {
			head += p.color(" ["+via+"]", ansiDim)
		}
		fmt.Fprintln(sink, "  "+head)
	case "exec_line":
		fmt.Fprint(sink, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(sink, rec.Msg)
	case "run_start":
		recCopy := rec
		p.pendingRunStart = &recCopy
		return
	case "run_plan":
		p.writeSetupBlock(sink, p.pendingRunStart, &rec)
		p.pendingRunStart = nil
	case "run_summary":
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

func (p *PrettyRenderer) flushPending(sink io.Writer) {
	if p.pendingRunStart != nil {
		p.writeSetupBlock(sink, p.pendingRunStart, nil)
		p.pendingRunStart = nil
	}
	if p.pendingNodeStart != nil {
		nh := p.hueFor(p.pendingNodeStart.JobID)
		fmt.Fprintln(sink, p.color("▶ "+p.pendingNodeStart.JobID, ansiBold+nh))
		p.pendingNodeStart = nil
	}
	if p.pendingStepEnd != nil {
		nh := p.hueFor(p.pendingStepEnd.JobID)
		p.writeStepEnd(sink, *p.pendingStepEnd, nh)
		p.pendingStepEnd = nil
	}
}

// Flush drains any buffered events. Callers rendering one record at a
// time (HTTP pretty-printers, batched log rendering) must invoke this
// at end-of-stream so a buffered node_start or step_end is not dropped
// when no follow-up event arrives.
func (p *PrettyRenderer) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushPending(p.w)
	if p.pendingRunSummary != nil {
		p.writeRunBlock(p.w, p.pendingRunSummary, nil)
		p.pendingRunSummary = nil
	}
}

func (p *PrettyRenderer) breadcrumb(rec sparkwing.LogRecord, nodeHue string) string {
	if rec.JobID == "" && rec.Step == "" {
		return ""
	}
	var b strings.Builder
	if rec.JobID != "" {
		b.WriteString(p.color(rec.JobID, nodeHue))
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

func (p *PrettyRenderer) writePlan(w io.Writer, rec sparkwing.LogRecord) {
	raw, _ := rec.Attrs["nodes"].([]any)
	type stepRow struct {
		id     string
		deps   []string
		groups []string
	}
	type node struct {
		id         string
		deps       []string
		groupDeps  []string
		inline     bool
		dynamic    bool
		approval   bool
		groups     []string
		steps      []stepRow
		skipReason string
	}
	byID := make(map[string]*node, len(raw))
	order := make([]*node, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
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
		skipReason, _ := m["skip_reason"].(string)
		n := &node{id: id, deps: deps, groupDeps: groupDeps, inline: inline, dynamic: dynamic, approval: approval, groups: groups, steps: steps, skipReason: skipReason}
		byID[id] = n
		order = append(order, n)
	}
	if len(order) == 0 {
		return
	}

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

	maxName := 0
	for _, n := range sorted {
		maxName = max(maxName, len(n.id))
	}
	maxName = min(maxName, 40)

	fmt.Fprintln(w, p.color(fmt.Sprintf("Plan (%d nodes)", len(sorted)), ansiBold))
	for _, n := range sorted {
		var name, bullet string
		if n.skipReason != "" {
			name = p.color(fmt.Sprintf("%-*s", maxName, n.id), ansiDim)
			bullet = p.color("○", ansiDim)
		} else {
			name = p.color(fmt.Sprintf("%-*s", maxName, n.id), p.hueFor(n.id))
			bullet = p.color("●", p.hueFor(n.id))
		}
		var arrow string
		depParts := make([]string, 0, len(n.deps)+len(n.groupDeps))
		for _, d := range n.deps {
			if n.skipReason != "" {
				depParts = append(depParts, p.color(d, ansiDim))
			} else {
				depParts = append(depParts, p.color(d, p.hueFor(d)))
			}
		}
		for _, d := range n.groupDeps {
			if n.skipReason != "" {
				depParts = append(depParts, p.color(d+"*", ansiDim))
			} else {
				depParts = append(depParts, p.color(d+"*", p.hueFor(d)))
			}
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
		if n.skipReason != "" {
			tags += "  " + p.color("[skip: target]", ansiDim)
		}
		fmt.Fprintln(w, "  "+bullet+" "+name+arrow+tags)
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

const runBlockWidth = 60

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

	// Lead with the root cause: the node(s) that actually errored
	// (outcome=failed) and a one-line error tail, with cascaded
	// cancellations summarized separately so they don't read as
	// failures.
	if status == "failed" && summary != nil {
		nodes, _ := summary.Attrs["nodes"].([]any)
		p.writeFailureHeadline(w, nodes)
	}

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

	if finish != nil {
		if errMsg, ok := finish.Attrs["error"].(string); ok && errMsg != "" && !strings.HasPrefix(errMsg, "nodes failed:") {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "  "+p.color("error  ", ansiDim)+" "+p.color(errMsg, ansiRed))
		}
	}

	if summary != nil {
		nodes, _ := summary.Attrs["nodes"].([]any)
		p.writeRunBlockSummaries(w, nodes)
	}

	if runID != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("Tips", ansiBold))
		fmt.Fprintln(w, "  "+p.color("status ", ansiDim)+" "+p.color("sparkwing runs status --run "+runID, ansiCyan))
		fmt.Fprintln(w, "  "+p.color("logs   ", ansiDim)+" "+p.color("sparkwing runs logs --run "+runID, ansiCyan))
		if status == "failed" {
			fmt.Fprintln(w, "  "+p.color("retry  ", ansiDim)+" "+p.color("sparkwing runs retry --failed --run "+runID, ansiCyan))
		}
	}

	fmt.Fprintln(w, p.sectionRule(""))
}

func (p *PrettyRenderer) writeRunBlockSummaries(w io.Writer, nodes []any) {
	type entry struct {
		nodeID string
		stepID string
		md     string
	}
	var entries []entry
	for _, n := range nodes {
		m, ok := n.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		if md, _ := m["summary"].(string); md != "" {
			entries = append(entries, entry{nodeID: id, md: md})
		}
		steps, _ := m["step_summaries"].([]any)
		for _, s := range steps {
			sm, ok := s.(map[string]any)
			if !ok {
				continue
			}
			stepID, _ := sm["step_id"].(string)
			md, _ := sm["summary"].(string)
			if md != "" {
				entries = append(entries, entry{nodeID: id, stepID: stepID, md: md})
			}
		}
	}
	if len(entries) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, p.color("Summaries", ansiBold))
	for i, e := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		hue := p.hueFor(e.nodeID)
		header := p.color(e.nodeID, ansiBold+hue)
		if e.stepID != "" {
			header += p.color(" › ", ansiDim) + p.color(e.stepID, ansiBold)
		}
		fmt.Fprintln(w, "  "+header)
		RenderMarkdownSummary(w, "    ", e.md)
	}
}

// writeFailureHeadline names the root-cause node(s) -- those whose
// outcome is "failed", i.e. they ran and errored -- with a one-line
// error tail, then summarizes how many downstream nodes were cancelled
// by that failure. The cancelled count is a cascade, not a set of
// independent failures, so it's reported separately rather than mixed
// into the failure list.
func (p *PrettyRenderer) writeFailureHeadline(w io.Writer, nodes []any) {
	cancelled := 0
	var failed []map[string]any
	for _, n := range nodes {
		m, ok := n.(map[string]any)
		if !ok {
			continue
		}
		switch m["outcome"] {
		case "failed":
			failed = append(failed, m)
		case "cancelled":
			cancelled++
		}
	}
	for _, m := range failed {
		id, _ := m["id"].(string)
		line := "  " + p.color("cause  ", ansiDim) + " " + p.color(id, p.hueFor(id))
		if msg, _ := m["error"].(string); msg != "" {
			_, body := splitStepErrorPrefix(msg)
			if tail := errorTail(body); tail != "" {
				line += p.color(" │ ", ansiDim) + p.color(tail, ansiRed)
			}
		}
		fmt.Fprintln(w, line)
	}
	if cancelled > 0 {
		fmt.Fprintln(w, "  "+p.color("cascade", ansiDim)+" "+
			p.color(fmt.Sprintf("%d node%s cancelled by the failure", cancelled, pluralS(cancelled)), ansiYellow))
	}
}

// errorTail collapses a (possibly multi-line) error to its last
// non-empty line, truncated for the one-line headline. The last line
// is usually the proximate cause; the full text stays in the Errors
// block below.
func errorTail(body string) string {
	const max = 120
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	tail := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			tail = strings.TrimSpace(lines[i])
			break
		}
	}
	r := []rune(tail)
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return tail
}

func (p *PrettyRenderer) writeRunBlockNodeRow(w io.Writer, m map[string]any) {
	id, _ := m["id"].(string)
	oc, _ := m["outcome"].(string)
	nodeIcon, nodeCode := outcomeIcon(oc)
	const nameWidth = 24
	line := fmt.Sprintf(
		"  %s %s",
		p.color(nodeIcon, nodeCode),
		p.color(fmt.Sprintf("%-*s", nameWidth, id), p.hueFor(id)),
	)
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

type summaryCounts struct {
	total     int
	passed    int
	failed    int
	cancelled int
	skipped   int
	other     int
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
		case "cancelled":
			// Upstream-failure cascade; kept distinct from skipped (a
			// SkipIf / filter decision) so the tally doesn't read as
			// "everything downstream also broke."
			c.cancelled++
		case "skipped", "skipped-concurrent":
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
	if c.cancelled > 0 {
		parts = append(parts, p.color(fmt.Sprintf("%d cancelled", c.cancelled), ansiYellow))
	}
	if c.skipped > 0 {
		parts = append(parts, p.color(fmt.Sprintf("%d skipped", c.skipped), ansiDim))
	}
	if c.other > 0 {
		parts = append(parts, p.color(fmt.Sprintf("%d other", c.other), ansiYellow))
	}
	return strings.Join(parts, p.color(" · ", ansiDim))
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

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

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

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

func (p *PrettyRenderer) writeStepStart(w io.Writer, rec sparkwing.LogRecord, nodeHue string) {
	_ = nodeHue
	glyph := "●"
	code := ansiBlue
	fmt.Fprintln(w, p.color(fmt.Sprintf("  %s %s", glyph, rec.Msg), code))
}

func (p *PrettyRenderer) writeStepEnd(w io.Writer, rec sparkwing.LogRecord, nodeHue string) {
	_ = nodeHue
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
	fmt.Fprintln(w, p.color(fmt.Sprintf("  %s %s", glyph, rec.Msg), code)+tail)
	if errMsg, ok := rec.Attrs["error"].(string); ok && errMsg != "" {
		for _, errLine := range strings.Split(strings.TrimRight(errMsg, "\n"), "\n") {
			fmt.Fprintln(w, "      "+p.color(errLine, ansiRed))
		}
	}
}

func (p *PrettyRenderer) writeStepSkipped(w io.Writer, rec sparkwing.LogRecord, nodeHue string) {
	_ = nodeHue
	glyph, code := outcomeIcon("skipped")
	reason, _ := rec.Attrs["reason"].(string)
	tail := ""
	if reason != "" {
		tail = " " + p.color("["+reason+"]", ansiDim)
	}
	fmt.Fprintln(w, p.color(fmt.Sprintf("  %s %s", glyph, rec.Msg), code)+tail)
}

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
	if inputsHash != "" {
		fmt.Fprintln(w, "  "+p.color("inputs  ", ansiDim)+" "+shortHash(inputsHash))
	}
	if planHash != "" {
		fmt.Fprintln(w, "  "+p.color("plan    ", ansiDim)+" "+shortHash(planHash))
	}
	if reproducer != "" {
		fmt.Fprintln(w, "  "+p.color("rerun   ", ansiDim)+" "+p.color(reproducer, ansiCyan))
	}

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

	if runStart != nil {
		p.writeProfileBlock(w, runStart)
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
	fmt.Fprintln(w, p.sectionRule("Logs"))
}

// writeProfileBlock renders the resolved-profile banner before the plan
// when run_start carries a profile/backends object (i.e. the run was
// driven by --profile). Omitted entirely otherwise, so legacy runs and
// historical envelopes without these keys render unchanged. Never prints
// the token or controller URL.
func (p *PrettyRenderer) writeProfileBlock(w io.Writer, runStart *sparkwing.LogRecord) {
	prof := anyMap(runStart.Attrs["profile"])
	if len(prof) == 0 {
		return
	}
	backends := anyMap(runStart.Attrs["backends"])
	name, _ := prof["name"].(string)
	source, _ := prof["source"].(string)
	detectVia, _ := prof["detect_via"].(string)
	mirrorLocal, _ := prof["mirror_local"].(bool)
	state, _ := backends["state"].(string)
	logs, _ := backends["logs"].(string)
	cache, _ := backends["cache"].(string)

	fmt.Fprintln(w)
	fmt.Fprintln(w, p.color("profile:", ansiDim)+"  "+name)
	fmt.Fprintln(w, "  "+p.color(padRight("via:", 7), ansiDim)+" "+profileViaPhrase(source, detectVia))
	fmt.Fprintln(w, "  "+p.color(padRight("state:", 7), ansiDim)+" "+state)
	fmt.Fprintln(w, "  "+p.color(padRight("logs:", 7), ansiDim)+" "+logs)
	fmt.Fprintln(w, "  "+p.color(padRight("cache:", 7), ansiDim)+" "+cache)
	// The mirror only engages for non-local (non-sqlite) state, so the
	// line is noise for a local profile; omit it there.
	if !strings.HasPrefix(state, "sqlite") {
		mirror := "off"
		if mirrorLocal {
			mirror = "on"
		}
		fmt.Fprintln(w, "  "+p.color(padRight("mirror:", 7), ansiDim)+" "+mirror)
	}
}

// profileViaPhrase maps a ChainSource to the human phrase shown on
// the banner's `via:` line. detectVia is retained as a no-op
// parameter for caller-call-site compatibility.
func profileViaPhrase(source, _ string) string {
	switch source {
	case string(profile.ChainSourceFlag):
		return "--profile flag"
	case string(profile.ChainSourceProject):
		return "project hint (.sparkwing/sparkwing.yaml profile:)"
	case string(profile.ChainSourceDefault):
		return "default (" + profile.DisplayDefaultPath() + ")"
	case string(profile.ChainSourceBuiltin):
		return "built-in fallback"
	default:
		return source
	}
}

func outcomeIcon(outcome string) (icon, code string) {
	switch outcome {
	case "success", "Success":
		return "✓", ansiGreen
	case "failed", "Failed":
		return "✗", ansiRed
	case "skipped", "Skipped":
		return "⊘", ansiDim
	case "skipped-concurrent":
		return "⊚", ansiDim
	case "cached", "Cached":
		return "◈", ansiCyan
	case "cancelled", "Cancelled":
		return "⊘", ansiYellow
	case "superseded":
		return "⟳", ansiYellow
	default:
		return "·", ""
	}
}

func summaryStatusIcon(status string) (icon, code string) {
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

// RenderMarkdownSummary pretty-prints a markdown blob for terminal
// readers. Headings, emphasis, checklists, and tables get light
// styling; everything else passes through as-is. Each output line is
// indented by prefix so the block visually nests under its node/step
// header.
//
// Color emission auto-disables when stdout isn't a TTY (pkg/color),
// so agents and pipes get plain text -- the styling never bleeds
// into logs.
func RenderMarkdownSummary(out io.Writer, prefix, md string) {
	body := strings.TrimRight(md, "\n")
	lines := strings.Split(body, "\n")

	var table []string
	flushTable := func() {
		if len(table) == 0 {
			return
		}
		writeMarkdownTable(out, prefix, table)
		table = nil
	}

	for _, raw := range lines {
		line := raw
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "|") && strings.HasSuffix(trim, "|") {
			if !isTableSeparator(trim) {
				table = append(table, trim)
			}
			continue
		}
		flushTable()
		writeMarkdownLine(out, prefix, line)
	}
	flushTable()
}

func isTableSeparator(line string) bool {
	inner := strings.Trim(line, "|")
	if inner == "" {
		return false
	}
	hasDash := false
	for _, r := range inner {
		switch r {
		case '-':
			hasDash = true
		case ':', '|', ' ':
		default:
			return false
		}
	}
	return hasDash
}

func writeMarkdownTable(out io.Writer, prefix string, rows []string) {
	if len(rows) == 0 {
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, row := range rows {
		cells := splitTableRow(row)
		styled := make([]string, len(cells))
		for j, c := range cells {
			s := renderInlineMarkdown(c)
			if i == 0 {
				s = color.Bold(s)
			}
			styled[j] = s
		}
		fmt.Fprintf(tw, "%s%s\n", prefix, strings.Join(styled, "\t"))
	}
	_ = tw.Flush()
}

func splitTableRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	parts := strings.Split(row, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

var (
	reH3        = regexp.MustCompile(`^(\s*)###\s+(.*)$`)
	reH2        = regexp.MustCompile(`^(\s*)##\s+(.*)$`)
	reH1        = regexp.MustCompile(`^(\s*)#\s+(.*)$`)
	reChecked   = regexp.MustCompile(`^(\s*)-\s+\[[xX]\]\s+(.*)$`)
	reUnchecked = regexp.MustCompile(`^(\s*)-\s+\[\s\]\s+(.*)$`)
	reBullet    = regexp.MustCompile(`^(\s*)-\s+(.*)$`)
)

func writeMarkdownLine(out io.Writer, prefix, line string) {
	switch {
	case strings.TrimSpace(line) == "":
		fmt.Fprintln(out)
		return
	}
	if m := reH3.FindStringSubmatch(line); m != nil {
		fmt.Fprintf(out, "%s%s%s\n", prefix, m[1], color.Bold(renderInlineMarkdown(m[2])))
		return
	}
	if m := reH2.FindStringSubmatch(line); m != nil {
		writeHeadingWithUnderline(out, prefix+m[1], m[2])
		return
	}
	if m := reH1.FindStringSubmatch(line); m != nil {
		writeHeadingWithUnderline(out, prefix+m[1], m[2])
		return
	}
	if m := reChecked.FindStringSubmatch(line); m != nil {
		glyph := color.Green("✓")
		fmt.Fprintf(out, "%s%s%s %s\n", prefix, m[1], glyph, renderInlineMarkdown(m[2]))
		return
	}
	if m := reUnchecked.FindStringSubmatch(line); m != nil {
		glyph := color.Dim("☐")
		fmt.Fprintf(out, "%s%s%s %s\n", prefix, m[1], glyph, renderInlineMarkdown(m[2]))
		return
	}
	if m := reBullet.FindStringSubmatch(line); m != nil {
		fmt.Fprintf(out, "%s%s• %s\n", prefix, m[1], renderInlineMarkdown(m[2]))
		return
	}
	fmt.Fprintf(out, "%s%s\n", prefix, renderInlineMarkdown(line))
}

func writeHeadingWithUnderline(out io.Writer, prefix, text string) {
	styled := color.Bold(renderInlineMarkdown(text))
	width := utf8.RuneCountInString(text)
	fmt.Fprintf(out, "%s%s\n", prefix, styled)
	fmt.Fprintf(out, "%s%s\n", prefix, color.Dim(strings.Repeat("─", width)))
}

func renderInlineMarkdown(s string) string {
	var buf bytes.Buffer
	i := 0
	for i < len(s) {
		switch s[i] {
		case '`':
			end := strings.IndexByte(s[i+1:], '`')
			if end < 0 {
				buf.WriteByte(s[i])
				i++
				continue
			}
			buf.WriteString(color.Cyan(s[i+1 : i+1+end]))
			i += end + 2
		case '*':
			if i+1 < len(s) && s[i+1] == '*' {
				end := strings.Index(s[i+2:], "**")
				if end < 0 {
					buf.WriteByte(s[i])
					i++
					continue
				}
				buf.WriteString(color.Bold(s[i+2 : i+2+end]))
				i += end + 4
				continue
			}
			buf.WriteByte(s[i])
			i++
		default:
			buf.WriteByte(s[i])
			i++
		}
	}
	return buf.String()
}

// ansiMagenta is kept here even though no current call site uses it;
// it sits with the other CSI constants so future renderer additions
// don't have to relocate the palette.
var _ = ansiMagenta
