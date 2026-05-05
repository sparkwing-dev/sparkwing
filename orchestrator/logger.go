package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	switch rec.Event {
	case "node_start":
		p.nodeStart[rec.Node] = rec.TS
		fmt.Fprintln(sink, p.color(fmt.Sprintf("▶ %s", rec.Node), ansiBold+nodeHue))
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
	case "step":
		// error-level steps (StepErr) get ✗ red instead of ● blue.
		glyph := "●"
		code := ansiBlue
		if rec.Level == "error" {
			glyph = "✗"
			code = ansiRed
		}
		if rec.Job != "" {
			fmt.Fprint(sink, p.breadcrumb(rec, nodeHue))
			fmt.Fprintln(sink, p.color(glyph+" "+rec.Msg, code))
		} else {
			fmt.Fprintln(sink, p.color(fmt.Sprintf("  %s %s", glyph, rec.Msg), code))
		}
	case "retry":
		fmt.Fprintln(sink, p.color(fmt.Sprintf("  ↻ %s", rec.Msg), ansiYellow))
	case "exec_line":
		fmt.Fprint(sink, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(sink, rec.Msg)
	case "run_start":
		p.writeRunStart(sink, rec)
	case "run_plan":
		p.writePlan(sink, rec)
	case "run_summary":
		p.writeSummary(sink, rec)
	case "run_finish":
		p.writeRunFinish(sink, rec)
	default:
		fmt.Fprint(sink, p.breadcrumb(rec, nodeHue))
		fmt.Fprintln(sink, p.levelize(rec.Level, rec.Msg))
	}
}

// breadcrumb renders "<node> › <parent>... › <job> │ ".
func (p *PrettyRenderer) breadcrumb(rec sparkwing.LogRecord, nodeHue string) string {
	if rec.Node == "" && rec.Job == "" {
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
	type node struct {
		id        string
		deps      []string
		groupDeps []string
		inline    bool
		dynamic   bool
		approval  bool
		groups    []string
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
		n := &node{id: id, deps: deps, groupDeps: groupDeps, inline: inline, dynamic: dynamic, approval: approval, groups: groups}
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

	header := p.color(fmt.Sprintf("plan (%d nodes):", len(sorted)), ansiBold)
	fmt.Fprintln(w, header)
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
	}
	fmt.Fprintln(w)
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

func (p *PrettyRenderer) writeSummary(w io.Writer, rec sparkwing.LogRecord) {
	fmt.Fprintln(w)
	status, _ := rec.Attrs["status"].(string)
	icon, code := summaryStatusIcon(status)
	header := fmt.Sprintf("%s run %s", icon, status)
	if dms, ok := asMillis(rec.Attrs["duration_ms"]); ok {
		header += fmt.Sprintf(" (%s)", fmtDuration(dms))
	}
	fmt.Fprintln(w, p.color(header, code+ansiBold))
	nodes, _ := rec.Attrs["nodes"].([]any)
	type failure struct {
		id  string
		err string
	}
	var failures []failure
	for _, n := range nodes {
		m, ok := n.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		oc, _ := m["outcome"].(string)
		icon, code := outcomeIcon(oc)
		leftGlyph := p.color(icon, code)
		name := p.color(fmt.Sprintf("%-32s", id), p.hueFor(id))
		outcomeWord := p.color(oc, code)
		line := fmt.Sprintf("  %s %s %s", leftGlyph, name, outcomeWord)
		if dms, ok := asMillis(m["duration_ms"]); ok && dms > 0 {
			line += "  " + p.color(fmtDuration(dms), ansiDim)
		}
		if dyn, _ := m["dynamic"].(bool); dyn {
			line += "  " + p.rainbow("[dynamic]")
		}
		fmt.Fprintln(w, line)
		if errMsg, ok := m["error"].(string); ok && errMsg != "" {
			failures = append(failures, failure{id: id, err: errMsg})
		}
	}
	if len(failures) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("errors:", ansiBold+ansiRed))
		for _, f := range failures {
			head := p.color("✗ "+f.id, p.hueFor(f.id))
			fmt.Fprintln(w, "  "+head)
			for _, line := range strings.Split(strings.TrimRight(f.err, "\n"), "\n") {
				fmt.Fprintln(w, "    "+line)
			}
		}
	}
}

// writeRunStart renders the run-id breadcrumb at the top of a run.
func (p *PrettyRenderer) writeRunStart(w io.Writer, rec sparkwing.LogRecord) {
	runID, _ := rec.Attrs["run_id"].(string)
	if runID == "" {
		return
	}
	pipeline, _ := rec.Attrs["pipeline"].(string)
	header := p.color("▶ "+runID, ansiCyan)
	if pipeline != "" {
		header += "  " + p.color("("+pipeline+")", ansiDim)
	}
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, p.color("  follow logs:", ansiDim)+"  "+p.color("sparkwing runs logs --run "+runID+" --follow", ansiCyan))
	fmt.Fprintln(w, p.color("  status:     ", ansiDim)+"  "+p.color("sparkwing runs status --run "+runID, ansiCyan))
	fmt.Fprintln(w)
}

// writeRunFinish renders the terminal run_finish record.
func (p *PrettyRenderer) writeRunFinish(w io.Writer, rec sparkwing.LogRecord) {
	runID, _ := rec.Attrs["run_id"].(string)
	status, _ := rec.Attrs["status"].(string)
	icon, code := summaryStatusIcon(status)
	fmt.Fprintln(w)
	fmt.Fprintln(w, p.color(icon, code)+" "+p.color("run: "+runID, ansiDim))
	fmt.Fprintln(w, p.color(icon, code)+" "+p.color("status: "+status, ansiDim))
	if errMsg, ok := rec.Attrs["error"].(string); ok && errMsg != "" {
		fmt.Fprintln(w, p.color("error: "+errMsg, ansiRed))
	}
	if runID != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.color("  status:  ", ansiDim)+"  "+p.color("sparkwing runs status --run "+runID, ansiCyan))
		fmt.Fprintln(w, p.color("  logs:    ", ansiDim)+"  "+p.color("sparkwing runs logs --run "+runID, ansiCyan))
		if status == "failed" {
			fmt.Fprintln(w, p.color("  retry:   ", ansiDim)+"  "+p.color("sparkwing runs retry --run "+runID, ansiCyan))
		}
	}
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
