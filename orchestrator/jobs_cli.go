package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ListOpts configures `sparkwing jobs list`.
type ListOpts struct {
	Limit     int
	Pipelines []string
	Statuses  []string
	Since     time.Duration
	JSON      bool

	// Quiet prints only ids (or a JSON id array with JSON).
	Quiet bool
}

// ListJobs prints or emits recent runs filtered by opts.
func ListJobs(ctx context.Context, paths Paths, opts ListOpts, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()

	// Lazy orphan reconciliation: any "running" rows whose orchestrator
	// process is dead get transitioned to "failed" before we read.
	// Errors are swallowed -- a stale-heartbeat sweep failing mustn't
	// break the list itself.
	_, _ = reconcileOrphanedLocalRuns(ctx, st, 0)

	filter := store.RunFilter{
		Limit:     opts.Limit,
		Pipelines: opts.Pipelines,
		Statuses:  opts.Statuses,
	}
	if opts.Since > 0 {
		filter.Since = time.Now().Add(-opts.Since)
	}
	runs, err := st.ListRuns(ctx, filter)
	if err != nil {
		return err
	}
	return renderRunList(runs, opts, out)
}

// renderRunList prints quiet/JSON/table output.
func renderRunList(runs []*store.Run, opts ListOpts, out io.Writer) error {
	if opts.Quiet {
		if opts.JSON {
			ids := make([]string, 0, len(runs))
			for _, r := range runs {
				ids = append(ids, r.ID)
			}
			return writeJSON(out, ids)
		}
		for _, r := range runs {
			fmt.Fprintln(out, r.ID)
		}
		return nil
	}

	if opts.JSON {
		if runs == nil {
			runs = []*store.Run{}
		}
		return writeJSON(out, runs)
	}

	if len(runs) == 0 {
		fmt.Fprintln(out, "no runs yet — invoke one via `wing <pipeline>`")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tPIPELINE\tSTATUS\tSTARTED\tDURATION")
	for _, r := range runs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Pipeline, r.Status,
			formatStartedAt(r.StartedAt),
			formatRunDuration(r),
		)
	}
	return tw.Flush()
}

// StatusOpts configures `sparkwing jobs status`.
type StatusOpts struct {
	JSON   bool
	Follow bool // poll until the run reaches a terminal state
}

// JobStatus prints DAG + per-node state. Follow re-renders on poll.
func JobStatus(ctx context.Context, paths Paths, runID string, opts StatusOpts, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()

	// Lazy orphan reconciliation -- see ListJobs.
	_, _ = reconcileOrphanedLocalRuns(ctx, st, 0)

	if opts.JSON {
		return writeRunDetailJSON(ctx, st, runID, out)
	}

	if !opts.Follow {
		return renderStatus(ctx, st, runID, out, false)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	first := true
	for {
		if !first {
			fmt.Fprint(out, "\033[H\033[J")
		}
		first = false
		if err := renderStatus(ctx, st, runID, out, true); err != nil {
			return err
		}
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if isTerminalStatus(run.Status) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func renderStatus(ctx context.Context, st *store.Store, runID string, out io.Writer, followBanner bool) error {
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}

	if followBanner {
		fmt.Fprintf(out, "# following %s (ctrl-c to stop)\n\n", runID)
	}

	fmt.Fprintf(out, "run:       %s\n", run.ID)
	fmt.Fprintf(out, "pipeline:  %s\n", run.Pipeline)
	fmt.Fprintf(out, "status:    %s\n", run.Status)
	fmt.Fprintf(out, "trigger:   %s\n", orDash(run.TriggerSource))
	fmt.Fprintf(out, "started:   %s  (%s)\n",
		run.StartedAt.Local().Format("2006-01-02 15:04:05"),
		relativeAge(run.StartedAt),
	)
	if run.FinishedAt != nil {
		fmt.Fprintf(out, "finished:  %s  (duration %s)\n",
			run.FinishedAt.Local().Format("2006-01-02 15:04:05"),
			run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond))
	} else {
		fmt.Fprintf(out, "elapsed:   %s\n", time.Since(run.StartedAt).Round(100*time.Millisecond))
	}
	if run.Error != "" {
		fmt.Fprintf(out, "error:     %s\n", run.Error)
	}
	if run.GitBranch != "" || run.GitSHA != "" {
		fmt.Fprintf(out, "git:       %s @ %s\n", run.GitBranch, shortSHA(run.GitSHA))
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "nodes (%d total, %d done):\n", len(nodes), countFinished(nodes))
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ID\tSTATUS\tOUTCOME\tDURATION\tDEPS")
	for _, n := range nodes {
		outcome := n.Outcome
		if outcome == "" {
			outcome = "-"
		}
		deps := strings.Join(n.Deps, ",")
		if deps == "" {
			deps = "-"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			n.NodeID, n.Status, outcome, formatNodeDuration(n), deps)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	for _, n := range nodes {
		if len(n.Output) > 0 {
			pretty, ok := prettyJSON(n.Output)
			if ok {
				fmt.Fprintf(out, "\n%s output:\n%s\n", n.NodeID, indent(pretty, "  "))
			}
		}
	}

	// Skip "upstream-failed" — root cause is already printed.
	for _, n := range nodes {
		if n.Error != "" && n.Error != "upstream-failed" {
			fmt.Fprintf(out, "\n%s error:\n  %s\n", n.NodeID, indent(n.Error, "  "))
		}
	}
	return nil
}

// LogsOpts configures `sparkwing jobs logs`.
type LogsOpts struct {
	Node   string
	JSON   bool
	Follow bool

	// Format: "json", "pretty", "plain", or "" (auto).
	Format string

	// Line filters; Tail wins over Head when both set.
	Tail  int
	Head  int
	Lines string // "A:B" inclusive 1-indexed
	Grep  string

	// Tree merges descendant-run logs (local mode only).
	Tree bool

	// Since filters by node StartedAt; node-level granularity.
	Since time.Duration

	// EventsOnly filters output to the run-level envelope events
	// (run_start, run_plan, node_start, node_end, run_summary,
	// run_finish, plan_warn, etc.) -- the same NDJSON the dispatcher
	// streams to stdout today. exec_line records are excluded since
	// they're really tagged body output. Mutually exclusive with
	// NoEvents.
	EventsOnly bool

	// NoEvents filters output to per-node body output only -- the
	// pre-IMP-010 behavior of `runs logs`. Useful as an explicit opt-
	// out when scripts depend on the legacy shape.
	NoEvents bool
}

// applyClientFilters is the local-mode equivalent of pkg/logs filters.
func (o LogsOpts) applyClientFilters(data []byte) []byte {
	if o.Tail == 0 && o.Head == 0 && o.Lines == "" && o.Grep == "" {
		return data
	}
	text := string(data)
	if text == "" {
		return data
	}
	hadTrailingNL := strings.HasSuffix(text, "\n")
	if hadTrailingNL {
		text = strings.TrimSuffix(text, "\n")
	}
	lines := strings.Split(text, "\n")
	if o.Grep != "" {
		kept := lines[:0:0]
		for _, l := range lines {
			if strings.Contains(l, o.Grep) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	if o.Lines != "" {
		a, b := parseLinesRange1(o.Lines)
		if a >= 1 {
			if a > len(lines) {
				lines = nil
			} else {
				if b == 0 || b > len(lines) {
					b = len(lines)
				}
				lines = lines[a-1 : b]
			}
		}
	}
	if o.Tail > 0 && len(lines) > o.Tail {
		lines = lines[len(lines)-o.Tail:]
	} else if o.Head > 0 && len(lines) > o.Head {
		lines = lines[:o.Head]
	}
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// parseLinesRange1 returns (0,0) on parse error.
func parseLinesRange1(spec string) (int, int) {
	i := strings.IndexByte(spec, ':')
	if i < 0 {
		return 0, 0
	}
	a, err := parseInt(spec[:i])
	if err != nil || a < 1 {
		return 0, 0
	}
	if spec[i+1:] == "" {
		return a, 0
	}
	b, err := parseInt(spec[i+1:])
	if err != nil || b < a {
		return 0, 0
	}
	return a, b
}

func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// JobLogs streams a run's logs. Empty Node = all nodes in sequence.
func JobLogs(ctx context.Context, paths Paths, runID string, opts LogsOpts, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	if opts.EventsOnly && opts.NoEvents {
		return fmt.Errorf("jobs logs: --events-only and --no-events are mutually exclusive")
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()

	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}

	target := nodes
	if opts.Node != "" {
		target = nil
		for _, n := range nodes {
			if n.NodeID == opts.Node {
				target = append(target, n)
				break
			}
		}
		if len(target) == 0 {
			return fmt.Errorf("node %q not found in run %s", opts.Node, runID)
		}
	}
	target = filterNodesBySince(target, opts.Since)

	if opts.Tree {
		return writeLogsTreeLocal(paths, runID, opts, out)
	}

	// IMP-010: when the envelope file exists (post-rewrite runs) and
	// the user hasn't asked for the legacy body-only view or pinned
	// to a single node, the envelope file IS the merged stream --
	// the dispatcher tees every run-wide event into it, including
	// exec_line body lines. Read it directly. Pre-IMP-010 runs (no
	// envelope file) fall back to the per-node path so historical
	// runs stay readable.
	if !opts.NoEvents && opts.Node == "" && envelopeExists(paths, runID) {
		if !opts.Follow {
			return writeLogsFromEnvelope(paths, runID, opts, out)
		}
		return followFromEnvelope(ctx, st, paths, runID, opts, out)
	}
	if opts.EventsOnly {
		// Envelope file missing on a pre-IMP-010 run; nothing to show.
		return nil
	}

	if !opts.Follow {
		return writeLogsText(paths, runID, target, opts, out)
	}
	return followLogs(ctx, st, paths, runID, target, opts, out)
}

// envelopeExists returns true when the run has an envelope file
// (post-IMP-010). Old runs predating the tee fall back to per-node
// reads.
func envelopeExists(paths Paths, runID string) bool {
	_, err := os.Stat(paths.EnvelopeLog(runID))
	return err == nil
}

// writeLogsFromEnvelope streams the run's _envelope.ndjson, optionally
// filtered to events-only, then renders per opts.Format.
func writeLogsFromEnvelope(paths Paths, runID string, opts LogsOpts, out io.Writer) error {
	path := paths.EnvelopeLog(runID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	// Apply filters (grep / tail / head / lines + events-only) by
	// buffering; envelope files are bounded by run duration and tend
	// to be small relative to body output. For very large runs the
	// follow path is the right tool anyway.
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if opts.EventsOnly {
		data = filterEventsOnly(data)
	}
	if opts.Tail > 0 || opts.Head > 0 || opts.Lines != "" || opts.Grep != "" {
		data = opts.applyClientFilters(data)
	}
	return renderJSONLStream(bytes.NewReader(data), opts, out)
}

// filterEventsOnly drops lines whose Event is empty or "exec_line".
// exec_line is technically an envelope record (the dispatcher emits
// it) but it carries body output, not a state transition; the
// `--events-only` user wants the bracketing events for grepping.
func filterEventsOnly(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	out := make([]byte, 0, len(data))
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		var rec sparkwing.LogRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// Unparseable; preserve so debugging stays possible.
			out = append(out, line...)
			out = append(out, '\n')
			continue
		}
		if rec.Event == "" || rec.Event == "exec_line" {
			continue
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out
}

// followFromEnvelope tails _envelope.ndjson until the run terminates,
// applying the same filters as the non-follow path on the fly.
func followFromEnvelope(ctx context.Context, st *store.Store, paths Paths, runID string, opts LogsOpts, out io.Writer) error {
	path := paths.EnvelopeLog(runID)
	jsonOut := opts.JSON || opts.Format == "json"
	plainOut := opts.Format == "plain"
	var offset int64
	var partial []byte
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		f, err := os.Open(path)
		if err == nil {
			if _, serr := f.Seek(offset, io.SeekStart); serr == nil {
				buf := make([]byte, 32*1024)
				var chunk []byte
				for {
					n2, rerr := f.Read(buf)
					if n2 > 0 {
						chunk = append(chunk, buf[:n2]...)
						offset += int64(n2)
					}
					if rerr != nil {
						break
					}
				}
				if len(chunk) > 0 {
					combined := append(partial, chunk...)
					lastNL := bytes.LastIndexByte(combined, '\n')
					if lastNL >= 0 {
						complete := combined[:lastNL+1]
						partial = append([]byte(nil), combined[lastNL+1:]...)
						if opts.EventsOnly {
							complete = filterEventsOnly(complete)
						}
						if opts.Grep != "" {
							complete = (LogsOpts{Grep: opts.Grep}).applyClientFilters(complete)
						}
						if err := emitFollowChunk(complete, jsonOut, plainOut, out); err != nil {
							f.Close()
							return err
						}
					} else {
						partial = combined
					}
				}
			}
			f.Close()
		}
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if isTerminalStatus(run.Status) {
			// One final drain pass after terminal: file may still
			// have a trailing run_finish that landed between the last
			// read and FinishRun's commit. Re-open and drain to EOF.
			return drainEnvelopeAfterTerminal(path, offset, partial, opts, out)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// drainEnvelopeAfterTerminal flushes any remaining bytes once the run
// reaches a terminal state. Mirrors followFromEnvelope's per-tick drain
// but runs once.
func drainEnvelopeAfterTerminal(path string, offset int64, partial []byte, opts LogsOpts, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	rest, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if len(rest) == 0 && len(partial) == 0 {
		return nil
	}
	jsonOut := opts.JSON || opts.Format == "json"
	plainOut := opts.Format == "plain"
	combined := append(partial, rest...)
	lastNL := bytes.LastIndexByte(combined, '\n')
	if lastNL < 0 {
		// No complete line; emit what we have so the operator at
		// least sees the partial.
		if len(combined) == 0 {
			return nil
		}
		combined = append(combined, '\n')
	} else {
		combined = combined[:lastNL+1]
	}
	if opts.EventsOnly {
		combined = filterEventsOnly(combined)
	}
	if opts.Grep != "" {
		combined = (LogsOpts{Grep: opts.Grep}).applyClientFilters(combined)
	}
	return emitFollowChunk(combined, jsonOut, plainOut, out)
}

func writeLogsText(paths Paths, runID string, target []*store.Node, opts LogsOpts, out io.Writer) error {
	// JSON = flat JSONL; no banner lines, no wrapper.
	jsonOut := opts.JSON || opts.Format == "json"
	for i, n := range target {
		if len(target) > 1 && !jsonOut {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "=== %s (%s) ===\n", n.NodeID, orDash(n.Outcome))
		}
		if n.StartedAt == nil {
			// Skip read entirely; silent in JSONL.
			if len(target) > 1 && !jsonOut {
				fmt.Fprintln(out, "(did not execute)")
			}
			continue
		}
		path := paths.NodeLog(runID, n.NodeID)
		if err := writeFile(path, opts, out); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, opts LogsOpts, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	// Sniff: JSONL canonical log files start with `{`. Old pre-JSONL
	// text files start with a digit (timestamp) -- fall back to the
	// plain-text printer so pre-rewrite runs stay readable.
	hdr := make([]byte, 1)
	_, _ = f.Read(hdr)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	isJSONL := len(hdr) == 1 && hdr[0] == '{'

	// When no filters are active we stream through the scanner to keep
	// memory flat on large logs. With filters active we must buffer
	// the whole file because grep / tail / lines windows are line-set
	// operations. The buffer cap matches the scanner's (4 MiB) so the
	// two paths handle the same maximum line length.
	if opts.Tail == 0 && opts.Head == 0 && opts.Lines == "" && opts.Grep == "" {
		if isJSONL {
			return renderJSONLStream(f, opts, out)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			fmt.Fprintln(out, sc.Text())
		}
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	filtered := opts.applyClientFilters(data)
	if isJSONL {
		return renderJSONLStream(bytes.NewReader(filtered), opts, out)
	}
	_, err = out.Write(filtered)
	return err
}

// renderJSONLStream parses r as JSONL records and renders per Format:
// json (passthrough), plain (ANSI-stripped one-liners), pretty (default).
// Unparseable lines pass through raw.
func renderJSONLStream(r io.Reader, opts LogsOpts, out io.Writer) error {
	wantJSON := opts.JSON || opts.Format == "json"
	wantPlain := opts.Format == "plain"
	var pr *PrettyRenderer
	if !wantJSON && !wantPlain {
		pr = NewPrettyRendererTo(out, os.Getenv("NO_COLOR") == "")
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if wantJSON {
			out.Write(line)
			out.Write([]byte{'\n'})
			continue
		}
		var rec sparkwing.LogRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			out.Write(line)
			out.Write([]byte{'\n'})
			continue
		}
		if wantPlain {
			fmt.Fprintln(out, formatPlain(rec))
			continue
		}
		pr.Emit(rec)
	}
	return sc.Err()
}

// emitFollowChunk writes one tick's worth of whole lines. Fresh
// renderer state per call so node_start banners aren't re-emitted.
func emitFollowChunk(data []byte, wantJSON, wantPlain bool, out io.Writer) error {
	if wantJSON {
		_, err := out.Write(data)
		return err
	}
	// Pre-JSONL legacy logs (no `{` prefix) pass through verbatim.
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		_, err := out.Write(data)
		return err
	}
	pr := NewPrettyRendererTo(out, !wantPlain && os.Getenv("NO_COLOR") == "")
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec sparkwing.LogRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			out.Write(line)
			out.Write([]byte{'\n'})
			continue
		}
		if wantPlain {
			fmt.Fprintln(out, formatPlain(rec))
			continue
		}
		pr.Emit(rec)
	}
	return sc.Err()
}

// formatPlain renders one record as an ANSI-stripped line.
func formatPlain(rec sparkwing.LogRecord) string {
	ts := rec.TS.Format(time.RFC3339Nano)
	lvl := rec.Level
	if lvl == "" {
		lvl = "info"
	}
	prefix := ts + " " + lvl
	if rec.Node != "" {
		prefix += " " + rec.Node
	}
	if rec.Event != "" {
		prefix += " [" + rec.Event + "]"
	}
	msg := StripANSI(rec.Msg)
	if msg == "" && rec.Attrs != nil {
		b, _ := json.Marshal(rec.Attrs)
		msg = string(b)
	}
	return prefix + " " + msg
}

// followLogs tails each node's log file until the run terminates and
// every target file has drained. Partial tails carry across ticks so
// JSONL records aren't sliced mid-line.
func followLogs(ctx context.Context, st *store.Store, paths Paths, runID string, target []*store.Node, opts LogsOpts, out io.Writer) error {
	offsets := make(map[string]int64, len(target))
	partials := make(map[string][]byte, len(target))
	banners := make(map[string]bool, len(target))
	jsonOut := opts.JSON || opts.Format == "json"
	plainOut := opts.Format == "plain"
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, n := range target {
			path := paths.NodeLog(runID, n.NodeID)
			f, err := os.Open(path)
			if err != nil {
				continue // file may not exist yet
			}
			if _, err := f.Seek(offsets[n.NodeID], io.SeekStart); err != nil {
				f.Close()
				continue
			}
			if !banners[n.NodeID] && len(target) > 1 && !jsonOut {
				fmt.Fprintf(out, "=== %s ===\n", n.NodeID)
				banners[n.NodeID] = true
			}
			buf := make([]byte, 32*1024)
			// Drain available bytes; split on \n; hold trailing
			// partial for the next tick.
			var chunk []byte
			for {
				n2, rerr := f.Read(buf)
				if n2 > 0 {
					chunk = append(chunk, buf[:n2]...)
					offsets[n.NodeID] += int64(n2)
				}
				if rerr != nil {
					break
				}
			}
			f.Close()
			if len(chunk) == 0 {
				continue
			}
			combined := append(partials[n.NodeID], chunk...)
			lastNL := bytes.LastIndexByte(combined, '\n')
			if lastNL < 0 {
				partials[n.NodeID] = combined
				continue
			}
			complete := combined[:lastNL+1]
			partials[n.NodeID] = append([]byte(nil), combined[lastNL+1:]...)

			if err := emitFollowChunk(complete, jsonOut, plainOut, out); err != nil {
				return err
			}
		}
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if isTerminalStatus(run.Status) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// writeLogsTreeLocal merges a root run and every descendant run's
// per-node logs into one chronological stream prefixed with the
// short run id. Remote mode does not support --tree today (no
// RunAndAwait child relationship is currently surfaced through
// a single controller endpoint); the CLI guards against that at the
// flag layer.
func writeLogsTreeLocal(paths Paths, rootID string, opts LogsOpts, out io.Writer) error {
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	ids, err := descendantRunIDs(ctx, st, rootID)
	if err != nil {
		return err
	}

	type entry struct {
		ts    time.Time
		runID string
		node  string
		line  string
	}
	var merged []entry
	for _, id := range ids {
		nodes, err := st.ListNodes(ctx, id)
		if err != nil {
			return fmt.Errorf("list nodes for %s: %w", id, err)
		}
		for _, n := range nodes {
			data, err := os.ReadFile(paths.NodeLog(id, n.NodeID))
			if err != nil {
				continue
			}
			// Grep per node; tail/head once at the end.
			if opts.Grep != "" {
				filtered := LogsOpts{Grep: opts.Grep}.applyClientFilters(data)
				data = filtered
			}
			base := n.StartedAt
			anchor := time.Time{}
			if base != nil {
				anchor = *base
			}
			for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
				if line == "" && i == 0 {
					continue
				}
				merged = append(merged, entry{
					ts:    anchor.Add(time.Duration(i) * time.Nanosecond),
					runID: id,
					node:  n.NodeID,
					line:  line,
				})
			}
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].ts.Before(merged[j].ts)
	})
	if opts.Tail > 0 && len(merged) > opts.Tail {
		merged = merged[len(merged)-opts.Tail:]
	} else if opts.Head > 0 && len(merged) > opts.Head {
		merged = merged[:opts.Head]
	}
	for _, e := range merged {
		fmt.Fprintf(out, "%s|%s: %s\n", shortRunID(e.runID), e.node, e.line)
	}
	return nil
}

// descendantRunIDs BFS-walks ParentRunID from rootID.
func descendantRunIDs(ctx context.Context, st *store.Store, rootID string) ([]string, error) {
	order := []string{rootID}
	seen := map[string]bool{rootID: true}
	queue := []string{rootID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		runs, err := st.ListRuns(ctx, store.RunFilter{ParentRunID: parent, Limit: 1000})
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			order = append(order, r.ID)
			queue = append(queue, r.ID)
		}
	}
	return order, nil
}

// shortRunID returns the trailing random suffix of a run id.
func shortRunID(id string) string {
	idx := strings.LastIndex(id, "-")
	if idx < 0 || idx == len(id)-1 {
		return id
	}
	return id[idx+1:]
}

// JobErrors prints failed nodes only; suppresses upstream-failed.
func JobErrors(ctx context.Context, paths Paths, runID string, asJSON bool, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()

	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}

	type failedNode struct {
		Node    string `json:"node"`
		Outcome string `json:"outcome"`
		Error   string `json:"error"`
	}
	var failed []failedNode
	for _, n := range nodes {
		if n.Outcome == string(sparkwingFailedStr) && n.Error != "" {
			failed = append(failed, failedNode{Node: n.NodeID, Outcome: n.Outcome, Error: n.Error})
		}
	}

	if asJSON {
		return writeJSON(out, failed)
	}
	if len(failed) == 0 {
		fmt.Fprintln(out, "no failing nodes")
		return nil
	}
	for _, f := range failed {
		fmt.Fprintf(out, "%s:\n  %s\n\n", f.Node, indent(f.Error, "  "))
	}
	return nil
}

// filterNodesBySince drops never-started or too-old nodes.
func filterNodesBySince(nodes []*store.Node, since time.Duration) []*store.Node {
	if since <= 0 {
		return nodes
	}
	cutoff := time.Now().Add(-since)
	out := nodes[:0:0]
	for _, n := range nodes {
		if n.StartedAt == nil {
			continue
		}
		if n.StartedAt.Before(cutoff) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// --- helpers ---

// sparkwingFailedStr mirrors sparkwing.Failed.
const sparkwingFailedStr = "failed"

func isTerminalStatus(s string) bool {
	switch s {
	case "success", "failed", "cancelled":
		return true
	}
	return false
}

// formatStartedAt prints "21:52:12 (3s ago)" or "2026-04-18 (2d ago)".
func formatStartedAt(t time.Time) string {
	age := time.Since(t)
	if age > 24*time.Hour {
		return fmt.Sprintf("%s (%s)", t.Local().Format("2006-01-02"), relativeAge(t))
	}
	return fmt.Sprintf("%s (%s)", t.Local().Format("15:04:05"), relativeAge(t))
}

func relativeAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatRunDuration(r *store.Run) string {
	if r.FinishedAt != nil {
		return r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond).String()
	}
	return "running (" + time.Since(r.StartedAt).Round(100*time.Millisecond).String() + ")"
}

// staleHeartbeatThreshold is how long a "running" node can go without a
// heartbeat before status flags it as stale. Local in-process runs
// don't refresh last_heartbeat after the initial stamp -- the node
// either completes or the process dies -- so the threshold also
// covers the "orphaned local run" case where the wing process
// crashed and left the row hanging in "running". The value is
// intentionally generous (well above the cluster heartbeat cadence
// of 5s) so a slow runner pause doesn't false-positive.
const staleHeartbeatThreshold = 30 * time.Second

func formatNodeDuration(n *store.Node) string {
	if n.StartedAt != nil && n.FinishedAt != nil {
		return n.FinishedAt.Sub(*n.StartedAt).Round(time.Millisecond).String()
	}
	if n.StartedAt != nil {
		base := "running " + time.Since(*n.StartedAt).Round(100*time.Millisecond).String()
		// Surface staleness for "running" nodes whose last heartbeat
		// is older than the threshold. Matches the dashboard's
		// liveness indicator so CLI and UI report the same orphan.
		if n.LastHeartbeat != nil {
			since := time.Since(*n.LastHeartbeat)
			if since > staleHeartbeatThreshold {
				base += "  (stale: no heartbeat " + since.Round(time.Second).String() + ")"
			}
		}
		return base
	}
	if n.Status == "done" {
		return "—"
	}
	return "pending"
}

func countFinished(nodes []*store.Node) int {
	n := 0
	for _, node := range nodes {
		if node.Status == "done" {
			n++
		}
	}
	return n
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if i > 0 {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func prettyJSON(raw []byte) (string, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", false
	}
	return string(b), true
}

// writeJSON encodes v to out with pretty indentation.
func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeRunDetailJSON(ctx context.Context, st *store.Store, runID string, out io.Writer) error {
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"run": run, "nodes": nodes})
}
