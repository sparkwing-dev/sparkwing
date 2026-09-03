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

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/logpretty"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func readBackendFor(ctx context.Context, paths Paths, p *profile.Profile) (backend.Backend, io.Closer, error) {
	if p != nil {
		return OpenReadBackendForProfile(ctx, paths, p)
	}
	return OpenReadBackend(ctx, paths)
}

type ListOpts struct {
	Limit     int
	Pipelines []string
	Statuses  []string
	Since     time.Duration
	JSON      bool

	Profile *profile.Profile

	Quiet bool

	Filter CompiledFilter

	ByPipeline bool
	Pivot      PivotOpts
}

func ListJobs(ctx context.Context, paths Paths, opts ListOpts, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	b, closer, err := readBackendFor(ctx, paths, opts.Profile)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	if st := localStore(b); st != nil {
		_, _ = ReconcileOrphanedLocalRuns(ctx, st, 0)
	}

	clientFilter := opts.Filter
	clientFilter.Branches = nil
	clientFilter.SHAPrefixes = nil
	filter := store.RunFilter{
		Limit:          listFetchLimitForFilter(opts.Limit, clientFilter),
		Pipelines:      opts.Pipelines,
		Statuses:       opts.Statuses,
		GitBranches:    opts.Filter.Branches,
		GitSHAPrefixes: opts.Filter.SHAPrefixes,
	}
	if opts.Since > 0 {
		filter.Since = time.Now().Add(-opts.Since)
	}
	runs, err := b.ListRuns(ctx, filter)
	if err != nil {
		return err
	}
	rows := TagShared(runs)
	var notes []string
	if localStore(b) != nil {
		standalone := OpenStandaloneStores(ctx, paths)
		defer func() { _ = standalone.Close() }()
		rows = MergeTaggedRuns(append(rows, standalone.ListRuns(ctx, filter)...))
		notes = standalone.Notes()
	}
	rows = applyClientFiltersTagged(rows, clientFilter)
	if opts.ByPipeline {
		opts.Pivot.JSON = opts.JSON
		opts.Pivot.Quiet = opts.Quiet
		if err := RenderPipelinePivot(untagRuns(rows), opts.Pivot, out); err != nil {
			return err
		}
		WriteStandaloneNotes(os.Stderr, notes)
		return nil
	}
	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}
	admissionStatus := func(runID string) (admissionWaitDetail, bool) {
		return latestAdmissionWait(ctx, b, runID)
	}
	if err := renderRunList(rows, opts, out, admissionStatus); err != nil {
		return err
	}
	WriteStandaloneNotes(os.Stderr, notes)
	return nil
}

func listFetchLimit(opts ListOpts) int {
	return listFetchLimitForFilter(opts.Limit, opts.Filter)
}

func listFetchLimitForFilter(limit int, filter CompiledFilter) int {
	if !filter.HasAny() {
		return limit
	}
	const overFetch = 1000
	if limit <= 0 || limit > overFetch {
		return overFetch
	}
	return overFetch
}

func renderRunList(
	rows []TaggedRun,
	opts ListOpts,
	out io.Writer,
	admissionStatus func(runID string) (admissionWaitDetail, bool),
) error {
	if opts.Quiet {
		if opts.JSON {
			ids := make([]string, 0, len(rows))
			for _, r := range rows {
				ids = append(ids, r.ID)
			}

			return writeNDJSON(out, ids)
		}
		for _, r := range rows {
			fmt.Fprintln(out, r.ID)
		}
		return nil
	}

	if opts.JSON {
		return writeNDJSON(out, redactTaggedRuns(rows))
	}

	if len(rows) == 0 {
		fmt.Fprintln(out, "no runs yet -- invoke one via `sparkwing run <pipeline>`")
		return nil
	}
	header := "RUN\tPIPELINE\tSTATUS\tSTARTED\tDURATION"
	showStore := anyStandalone(rows)
	if showStore {
		header += "\tSTORE"
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		status := r.Status
		if admissionStatus != nil && r.Store == SharedStoreLabel && runCanDisplayAdmissionWait(r.Run) {
			if detail, ok := admissionStatus(r.ID); ok {
				status = detail.listStatus()
			}
		}
		line := fmt.Sprintf(
			"%s\t%s\t%s\t%s\t%s",
			r.ID, r.Pipeline, status,
			formatStartedAt(r.StartedAt),
			formatRunDuration(r.Run),
		)
		if showStore {
			line += "\t" + r.Store
		}
		fmt.Fprintln(tw, line)
	}
	return tw.Flush()
}

func anyStandalone(rows []TaggedRun) bool {
	for _, r := range rows {
		if r.Store != SharedStoreLabel {
			return true
		}
	}
	return false
}

func untagRuns(rows []TaggedRun) []*store.Run {
	out := make([]*store.Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Run)
	}
	return out
}

func redactTaggedRuns(rows []TaggedRun) []TaggedRun {
	out := make([]TaggedRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, TaggedRun{Run: store.RedactedRun(r.Run), Store: r.Store})
	}
	return out
}

func applyClientFiltersTagged(rows []TaggedRun, f CompiledFilter) []TaggedRun {
	if !f.HasAny() {
		return rows
	}
	out := rows[:0:0]
	for _, r := range rows {
		if f.Matches(r.Run) {
			out = append(out, r)
		}
	}
	return out
}

type StatusOpts struct {
	JSON   bool
	Follow bool

	Steps bool

	Profile *profile.Profile
}

type nodeWithSteps struct {
	*store.Node
	Steps []*store.NodeStep `json:"steps,omitempty"`

	LogExcerpt          string `json:"log_excerpt,omitempty"`
	LogExcerptTruncated *bool  `json:"log_excerpt_truncated,omitempty"`

	LogExcerptUnavailable bool `json:"log_excerpt_unavailable,omitempty"`
}

func withFailureExcerpts(nodes []nodeWithSteps, ix failureExcerptIndex) []nodeWithSteps {
	for i := range nodes {
		if nodes[i].Node == nil {
			continue
		}
		if ex, ok := ix.Get(nodes[i].NodeID); ok {
			truncated := ex.Truncated
			nodes[i].LogExcerpt = ex.LogExcerpt
			nodes[i].LogExcerptTruncated = &truncated
			continue
		}
		if nodes[i].Outcome == string(sparkwing.Failed) && ix.Unavailable(nodes[i].NodeID) {
			nodes[i].LogExcerptUnavailable = true
		}
	}
	return nodes
}

func groupStepsByNode(steps []*store.NodeStep) map[string][]*store.NodeStep {
	idx := make(map[string][]*store.NodeStep, len(steps))
	for _, s := range steps {
		idx[s.NodeID] = append(idx[s.NodeID], s)
	}
	return idx
}

func joinStepsByNode(nodes []*store.Node, steps []*store.NodeStep) []nodeWithSteps {
	idx := groupStepsByNode(steps)
	out := make([]nodeWithSteps, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeWithSteps{Node: n, Steps: idx[n.NodeID]})
	}
	return out
}

func JobStatus(ctx context.Context, paths Paths, runID string, opts StatusOpts, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	b, closer, err := readBackendFor(ctx, paths, opts.Profile)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	if st := localStore(b); st != nil {
		_, _ = ReconcileOrphanedLocalRuns(ctx, st, 0)
	}

	storeLabel := SharedStoreLabel
	if st := localStore(b); st != nil {
		if _, gerr := st.GetRun(ctx, runID); gerr != nil {
			standalone := OpenStandaloneStores(ctx, paths)
			defer func() { _ = standalone.Close() }()
			if held, label, _, ok := standalone.Find(ctx, runID); ok {
				b = backend.NewStoreBackend(held, paths, nil)
				storeLabel = label
			}
			defer func() { WriteStandaloneNotes(os.Stderr, standalone.Notes()) }()
		}
	}

	if opts.JSON {
		if st := localStore(b); st != nil {
			return writeRunDetailJSON(ctx, st, runID, storeLabel, out)
		}
		run, err := b.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		nodes, err := b.ListNodes(ctx, runID)
		if err != nil {
			return err
		}
		wrapped := withFailureExcerpts(joinStepsByNode(nodes, nil),
			failureExcerptsFor(ctx, b, runID, failedNodeIDs(nodes)))
		payload := map[string]any{"run": store.RedactedRun(run), "nodes": wrapped, "store": storeLabel}
		if p := runLogPath(run); p != "" {
			payload["log_path"] = p
		}
		addRunStandalone(payload, run)
		return writeJSON(out, payload)
	}

	if !opts.Follow {
		return renderStatus(ctx, b, runID, storeLabel, out, false, opts.Steps)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	first := true
	for {
		if !first {
			fmt.Fprint(out, "\033[H\033[J")
		}
		first = false
		if err := renderStatus(ctx, b, runID, storeLabel, out, true, opts.Steps); err != nil {
			return err
		}
		run, err := b.GetRun(ctx, runID)
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

func renderStatus(
	ctx context.Context,
	b backend.Backend,
	runID, storeLabel string,
	out io.Writer,
	followBanner, includeSteps bool,
) error {
	run, err := b.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := b.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	var steps []*store.NodeStep
	if st := localStore(b); st != nil {
		steps, _ = st.ListNodeSteps(ctx, runID)
	}
	stepsByNode := groupStepsByNode(steps)

	if followBanner {
		fmt.Fprintf(out, "# following %s (ctrl-c to stop)\n\n", runID)
	}

	label := func(k string) string { return color.Dim(k) }
	fmt.Fprintf(out, "%s %s\n", label("run:      "), run.ID)
	fmt.Fprintf(out, "%s %s\n", label("pipeline: "), run.Pipeline)
	fmt.Fprintf(out, "%s %s\n", label("status:   "), colorStatus(run.Status))
	fmt.Fprintf(out, "%s %s\n", label("trigger:  "), orDash(run.TriggerSource))
	fmt.Fprintf(
		out, "%s %s  %s\n", label("started:  "),
		run.StartedAt.Local().Format("2006-01-02 15:04:05"),
		color.Dim("("+relativeAge(run.StartedAt)+")"),
	)
	if run.FinishedAt != nil {
		fmt.Fprintf(out, "%s %s  %s\n", label("finished: "),
			run.FinishedAt.Local().Format("2006-01-02 15:04:05"),
			color.Dim("(duration "+run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond).String()+")"))
	} else {
		fmt.Fprintf(out, "%s %s\n", label("elapsed:  "),
			time.Since(run.StartedAt).Round(100*time.Millisecond))
	}
	if run.Error != "" {
		fmt.Fprintf(out, "%s %s\n", label("error:    "), color.Red(run.Error))
	}
	if run.GitBranch != "" || run.GitSHA != "" {
		fmt.Fprintf(out, "%s %s @ %s\n", label("git:      "), run.GitBranch, shortSHA(run.GitSHA))
	}
	if p := runLogPath(run); p != "" {
		line := p
		if _, err := os.Stat(p); err != nil {
			line += color.Dim(" (not present on this machine)")
		}
		fmt.Fprintf(out, "%s %s\n", label("log_path: "), line)
	}
	if on, reason := runStandalone(run); on {
		line := "yes"
		if reason != "" {
			line = "yes (" + reason + ")"
		}
		fmt.Fprintf(out, "%s %s\n", label("standalone:"), line)
	}
	if storeLabel != "" && storeLabel != SharedStoreLabel {
		fmt.Fprintf(out, "%s %s\n", label("store:    "), storeLabel)
	}
	if runCanDisplayAdmissionWait(run) {
		if detail, ok := latestAdmissionWait(ctx, b, runID); ok {
			fmt.Fprintf(out, "%s %s\n", label("admission:"), detail.statusLine())
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "nodes (%d total, %d done):\n", len(nodes), countFinished(nodes))
	renderNodesWithSteps(out, nodes, stepsByNode, includeSteps)

	for _, n := range nodes {
		if len(n.Output) > 0 {
			pretty, ok := prettyJSON(n.Output)
			if ok {
				fmt.Fprintf(out, "\n%s output:\n%s\n", n.NodeID, indent(pretty, "  "))
			}
		}
	}

	for _, n := range nodes {
		if n.Error != "" && n.Error != "upstream-failed" {
			fmt.Fprintf(out, "\n%s error:\n  %s\n", n.NodeID, indent(n.Error, "  "))
		}
	}

	if st := localStore(b); st != nil {
		approvals, err := st.ListApprovalsForRun(ctx, runID)
		if err == nil {
			renderApprovalsSection(out, approvals)
		}
	}
	return nil
}

// safety: a standalone run is written to a store `sparkwing runs` never
// reads, so a row that does surface here came from that store being asked for
// by path; saying so is what keeps its absence from the machine's own list
// from reading as a lost run.
func addRunStandalone(payload map[string]any, run *store.Run) {
	on, reason := runStandalone(run)
	if !on {
		return
	}
	payload["standalone"] = true
	if reason != "" {
		payload["standalone_reason"] = reason
	}
}

func runStandalone(run *store.Run) (bool, string) {
	if run == nil {
		return false, ""
	}
	on, _ := run.Invocation["standalone"].(bool)
	reason, _ := run.Invocation["standalone_reason"].(string)
	return on, reason
}

func runLogPath(run *store.Run) string {
	if run == nil {
		return ""
	}
	p, _ := run.Invocation["log_path"].(string)
	return p
}

type admissionWaitDetail struct {
	Position     int `json:"position"`
	QueueLength  int `json:"queue_length"`
	WaitingNodes int `json:"-"`
}

type activeAdmissionWait struct {
	detail    admissionWaitDetail
	requestID string
}

func latestAdmissionWait(ctx context.Context, b backend.Backend, runID string) (admissionWaitDetail, bool) {
	const rootParticipant = "\x00root"
	waits := map[string]activeAdmissionWait{}
	legacyWaits := map[string]bool{}
	var after int64
	for {
		events, err := b.ListEventsAfter(ctx, runID, after, 500)
		if err != nil {
			return admissionWaitDetail{}, false
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			after = event.Seq
			eventNodeID := event.NodeID
			participant := event.NodeID
			var payload struct {
				Position    int    `json:"position"`
				QueueLength int    `json:"queue_length"`
				RequestID   string `json:"request_id"`
			}
			if len(event.Payload) > 0 {
				_ = json.Unmarshal(event.Payload, &payload)
				if participant == "" {
					participant = payload.RequestID
				}
			}
			if participant == "" || participant == runID || participant == runID+localPlanSemsID {
				participant = rootParticipant
			}
			switch event.Kind {
			case "admission_wait":
				waits[participant] = activeAdmissionWait{
					detail:    admissionWaitDetail{Position: payload.Position, QueueLength: payload.QueueLength},
					requestID: payload.RequestID,
				}
				if eventNodeID == "" && payload.RequestID != "" && participant != rootParticipant {
					legacyWaits[participant] = true
				}
			case "admission_granted", "admission_cancelled", "admission_queue_timeout":
				if eventNodeID == "" && payload.RequestID == "" {
					delete(waits, rootParticipant)
					for legacyParticipant := range legacyWaits {
						delete(waits, legacyParticipant)
						delete(legacyWaits, legacyParticipant)
					}
					continue
				}
				wait, ok := waits[participant]
				if ok && (payload.RequestID == "" || wait.requestID == payload.RequestID) {
					delete(waits, participant)
					delete(legacyWaits, participant)
				}
			}
		}
	}
	if root, ok := waits[rootParticipant]; ok {
		return root.detail, true
	}
	if len(waits) == 0 {
		return admissionWaitDetail{}, false
	}
	return admissionWaitDetail{WaitingNodes: len(waits)}, true
}

func (d admissionWaitDetail) listStatus() string {
	if d.WaitingNodes > 0 {
		return fmt.Sprintf("running (%d admission-waiting)", d.WaitingNodes)
	}
	if d.Position > 0 && d.QueueLength > 0 {
		return fmt.Sprintf("queued (%d/%d)", d.Position, d.QueueLength)
	}
	return "queued"
}

func (d admissionWaitDetail) statusLine() string {
	if d.WaitingNodes == 1 {
		return "1 node waiting for local admission"
	}
	if d.WaitingNodes > 1 {
		return fmt.Sprintf("%d nodes waiting for local admission", d.WaitingNodes)
	}
	if d.Position > 0 && d.QueueLength > 0 {
		return fmt.Sprintf("queued for local admission (position %d of %d)", d.Position, d.QueueLength)
	}
	return "queued for local admission"
}

func runCanDisplayAdmissionWait(run *store.Run) bool {
	return run.Status == "running" && run.FinishedAt == nil
}

func renderNodesWithSteps(out io.Writer, nodes []*store.Node, stepsByNode map[string][]*store.NodeStep, force bool) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  "+color.Dim("ID\tSTATUS\tOUTCOME\tDURATION\tDEPS"))
	for _, n := range nodes {
		deps := strings.Join(n.Deps, ",")
		if deps == "" {
			deps = "-"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			n.NodeID, n.Status, colorOutcome(n.Outcome),
			formatNodeDuration(n), color.Dim(deps))
	}
	_ = tw.Flush()
	for _, n := range nodes {
		steps := stepsByNode[n.NodeID]
		annotations := n.Annotations
		shouldRender := force || len(annotations) > 0 || n.Summary != "" || n.StatusDetail != "" || hasNonPassedStep(steps) || hasStepSummary(steps)
		if !shouldRender {
			continue
		}
		fmt.Fprintf(out, "    %s\n", color.Bold(n.NodeID+":"))
		if n.StatusDetail != "" {
			fmt.Fprintf(out, "      %s %s\n", color.Dim("↳"), n.StatusDetail)
		}
		for _, a := range annotations {
			fmt.Fprintf(out, "      %s %s\n", color.Dim("@"), a)
		}
		if n.Summary != "" {
			writeIndentedSummary(out, "      ", n.Summary)
		}
		// hack: tabwriter can't flush across interleaved annotation/summary rows, so we compute column widths manually
		idWidth := 0
		for _, s := range steps {
			if n := len(s.StepID); n > idWidth {
				idWidth = n
			}
		}
		for _, s := range steps {
			pad := strings.Repeat(" ", idWidth-len(s.StepID))
			fmt.Fprintf(out, "      %s  %s%s  %s\n",
				colorStepGlyph(s.Status),
				s.StepID, pad,
				color.Dim(formatStepDuration(s)))
			for _, a := range s.Annotations {
				fmt.Fprintf(out, "        %s %s\n", color.Dim("@"), a)
			}
			if s.Summary != "" {
				writeIndentedSummary(out, "        ", s.Summary)
			}
		}
	}
}

func writeIndentedSummary(out io.Writer, prefix, md string) {
	fmt.Fprintf(out, "%s%s\n", prefix, color.Dim("summary:"))
	logpretty.RenderMarkdownSummary(out, prefix+"  ", md)
}

func hasStepSummary(steps []*store.NodeStep) bool {
	for _, s := range steps {
		if s.Summary != "" {
			return true
		}
	}
	return false
}

func hasNonPassedStep(steps []*store.NodeStep) bool {
	for _, s := range steps {
		if s.Status != store.StepPassed {
			return true
		}
	}
	return false
}

func stepGlyph(status string) string {
	switch status {
	case store.StepPassed:
		return "✓"
	case store.StepFailed:
		return "✗"
	case store.StepCancelled:
		return "⊘"
	case store.StepSkipped:
		return "↷"
	case store.StepRunning:
		return "…"
	default:
		return "·"
	}
}

func formatStepDuration(s *store.NodeStep) string {
	if s.StartedAt != nil && s.FinishedAt != nil {
		return s.FinishedAt.Sub(*s.StartedAt).Round(time.Millisecond).String()
	}
	if s.StartedAt != nil {
		return "running " + time.Since(*s.StartedAt).Round(100*time.Millisecond).String()
	}
	return "--"
}

func renderApprovalsSection(out io.Writer, approvals []*store.Approval) {
	if len(approvals) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "approvals:")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  NODE\tSTATUS\tPOLICY\tAPPROVER\tWAITED")
	for _, a := range approvals {
		status := "pending"
		if a.ResolvedAt != nil {
			status = a.Resolution
		}
		var waited string
		if a.ResolvedAt != nil {
			waited = a.ResolvedAt.Sub(a.RequestedAt).Round(time.Second).String()
		} else {
			waited = time.Since(a.RequestedAt).Round(time.Second).String() + " (running)"
		}
		policy := a.OnTimeout
		if policy == "" {
			policy = "-"
		}
		approver := a.Approver
		if approver == "" {
			approver = "-"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			a.NodeID, status, policy, approver, waited)
	}
	_ = tw.Flush()
	for _, a := range approvals {
		if a.Comment != "" {
			fmt.Fprintf(out, "  %s comment: %s\n", a.NodeID, a.Comment)
		}
		if a.Message != "" && a.ResolvedAt == nil {
			fmt.Fprintf(out, "  %s message: %s\n", a.NodeID, a.Message)
		}
	}
}

type LogsOpts struct {
	Node   string
	JSON   bool
	Follow bool

	Format string

	Tail  int
	Head  int
	Lines string
	Grep  string

	Tree bool

	Since time.Duration

	EventsOnly bool

	NoEvents bool

	Profile *profile.Profile
}

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

func JobLogs(ctx context.Context, paths Paths, runID string, opts LogsOpts, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	if opts.EventsOnly && opts.NoEvents {
		return fmt.Errorf("runs logs: --events-only and --no-events are mutually exclusive")
	}
	b, closer, berr := readBackendFor(ctx, paths, opts.Profile)
	if berr != nil {
		return berr
	}
	defer func() { _ = closer.Close() }()

	if st := localStore(b); st == nil {
		if opts.Tree {
			return fmt.Errorf("--tree is only supported against local SQLite state; " +
				"unset --tree to read this run from the configured remote backend")
		}
		if opts.EventsOnly {
			return writeEventsViaBackend(ctx, b, runID, opts, out)
		}
		nodes, err := b.ListNodes(ctx, runID)
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
		if opts.Follow {
			return followLogsViaBackend(ctx, b, runID, opts.Node, out)
		}
		return writeLogsViaBackend(ctx, b, runID, target, opts, out)
	}

	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

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

	if !opts.NoEvents && opts.Node == "" && envelopeExists(paths, runID) {
		if !opts.Follow {
			return writeLogsFromEnvelope(paths, runID, opts, out)
		}
		return followFromEnvelope(ctx, st, paths, runID, opts, out)
	}
	if opts.EventsOnly {
		return nil
	}

	if !opts.Follow {
		return writeLogsText(paths, runID, target, opts, out)
	}
	return followLogs(ctx, st, paths, runID, target, opts, out)
}

func envelopeExists(paths Paths, runID string) bool {
	_, err := os.Stat(paths.EnvelopeLog(runID))
	return err == nil
}

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
			return drainEnvelopeAfterTerminal(path, offset, partial, opts, out)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

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
	jsonOut := opts.JSON || opts.Format == "json"
	for i, n := range target {
		if len(target) > 1 && !jsonOut {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "=== %s (%s) ===\n", n.NodeID, orDash(n.Outcome))
		}
		if n.StartedAt == nil {
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

	hdr := make([]byte, 1)
	_, _ = f.Read(hdr)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	isJSONL := len(hdr) == 1 && hdr[0] == '{'

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
			_, _ = out.Write(line)
			_, _ = out.Write([]byte{'\n'})
			continue
		}
		var rec sparkwing.LogRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			_, _ = out.Write(line)
			_, _ = out.Write([]byte{'\n'})
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

func emitFollowChunk(data []byte, wantJSON, wantPlain bool, out io.Writer) error {
	if wantJSON {
		_, err := out.Write(data)
		return err
	}
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
			_, _ = out.Write(line)
			_, _ = out.Write([]byte{'\n'})
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

func formatPlain(rec sparkwing.LogRecord) string {
	ts := rec.TS.Format(time.RFC3339Nano)
	lvl := rec.Level
	if lvl == "" {
		lvl = "info"
	}
	prefix := ts + " " + lvl
	if rec.JobID != "" {
		if rec.Step != "" {
			prefix += " " + logpretty.StripInline(rec.JobID) + "/" + logpretty.StripInline(rec.Step)
		} else {
			prefix += " " + logpretty.StripInline(rec.JobID)
		}
	}
	if rec.Event != "" {
		prefix += " [" + rec.Event + "]"
	}
	msg := StripANSI(rec.Msg)
	if msg == "" && rec.Attrs != nil {
		b, _ := json.Marshal(rec.Attrs)
		msg = string(b)
	}
	// safety: indenting the continuation lines keeps a newline in pipeline output from starting a
	// forged log line at column zero.
	return prefix + " " + strings.ReplaceAll(msg, "\n", "\n    ")
}

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
				continue
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

func writeLogsTreeLocal(paths Paths, rootID string, opts LogsOpts, out io.Writer) error {
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

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

func shortRunID(id string) string {
	idx := strings.LastIndex(id, "-")
	if idx < 0 || idx == len(id)-1 {
		return id
	}
	return id[idx+1:]
}

func JobErrors(ctx context.Context, paths Paths, runID string, asJSON bool, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}

	var excerpts failureExcerptIndex
	if asJSON {
		excerpts = failureExcerptsFor(ctx, st, runID, failedNodeIDs(nodes))
	}
	failed := failedNodeReports(nodes, excerpts)

	if asJSON {
		return writeNDJSON(out, failed)
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

type failedNodeReport struct {
	Node                string `json:"node"`
	Outcome             string `json:"outcome"`
	Error               string `json:"error"`
	LogExcerpt          string `json:"log_excerpt,omitempty"`
	LogExcerptTruncated *bool  `json:"log_excerpt_truncated,omitempty"`

	LogExcerptUnavailable bool `json:"log_excerpt_unavailable,omitempty"`
}

func failedNodeReports(nodes []*store.Node, ix failureExcerptIndex) []failedNodeReport {
	var out []failedNodeReport
	for _, n := range nodes {
		if n.Outcome != string(sparkwingFailedStr) || n.Error == "" {
			continue
		}
		row := failedNodeReport{Node: n.NodeID, Outcome: n.Outcome, Error: n.Error}
		if ex, ok := ix.Get(n.NodeID); ok {
			truncated := ex.Truncated
			row.LogExcerpt = ex.LogExcerpt
			row.LogExcerptTruncated = &truncated
		} else if ix.Unavailable(n.NodeID) {
			row.LogExcerptUnavailable = true
		}
		out = append(out, row)
	}
	return out
}

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

const sparkwingFailedStr = "failed"

func isTerminalStatus(s string) bool {
	switch s {
	case "success", "failed", "cancelled":
		return true
	}
	return false
}

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

const staleHeartbeatThreshold = 30 * time.Second

func formatNodeDuration(n *store.Node) string {
	if n.StartedAt != nil && n.FinishedAt != nil {
		return n.FinishedAt.Sub(*n.StartedAt).Round(time.Millisecond).String()
	}
	if n.StartedAt != nil {
		base := "running " + time.Since(*n.StartedAt).Round(100*time.Millisecond).String()
		if n.LastHeartbeat != nil {
			since := time.Since(*n.LastHeartbeat)
			if since > staleHeartbeatThreshold {
				base += "  (stale: no heartbeat " + since.Round(time.Second).String() + ")"
			}
		}
		return base
	}
	if n.Status == "done" {
		return "--"
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

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeNDJSON[T any](out io.Writer, records []T) error {
	return ndjson.Write(out, records)
}

func writeRunDetailJSON(ctx context.Context, st *store.Store, runID, storeLabel string, out io.Writer) error {
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	steps, _ := st.ListNodeSteps(ctx, runID)
	wrapped := withFailureExcerpts(joinStepsByNode(nodes, steps),
		failureExcerptsFor(ctx, st, runID, failedNodeIDs(nodes)))
	payload := map[string]any{"run": store.RedactedRun(run), "nodes": wrapped, "store": storeLabel}
	if p := runLogPath(run); p != "" {
		payload["log_path"] = p
	}
	addRunStandalone(payload, run)
	if approvals, err := st.ListApprovalsForRun(ctx, runID); err == nil && len(approvals) > 0 {
		payload["approvals"] = approvals
	}
	return writeJSON(out, payload)
}

func RunStatus(ctx context.Context, paths Paths, p *profile.Profile, runID string) (string, error) {
	if err := paths.EnsureRoot(); err != nil {
		return "", err
	}
	b, closer, err := readBackendFor(ctx, paths, p)
	if err != nil {
		return "", err
	}
	defer func() { _ = closer.Close() }()
	run, err := b.GetRun(ctx, runID)
	if err == nil {
		return run.Status, nil
	}
	if localStore(b) == nil {
		return "", err
	}
	standalone := OpenStandaloneStores(ctx, paths)
	defer func() { _ = standalone.Close() }()
	if _, _, held, ok := standalone.Find(ctx, runID); ok {
		return held.Status, nil
	}
	return "", err
}
