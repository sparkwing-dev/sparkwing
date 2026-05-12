// `sparkwing runs timeline` -- ASCII waterfall of a run's nodes and
// (optionally) inner steps, laid out along the run's wall-clock
// span. Mirrors the dashboard's Timeline tab so an agent reading
// run output through a terminal can reason about parallelism and
// the critical path without correlating logs by hand.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TimelineOpts configures `runs timeline`.
type TimelineOpts struct {
	Width        int  // bar width in characters; <=0 picks a default
	IncludeSteps bool // also render per-step rows under each node
	JSON         bool
}

// TimelineRow is one row in the JSON output. Kind is "node" or
// "step"; for step rows NodeID is the parent node and StepID is
// the inner id.
type TimelineRow struct {
	Kind          string `json:"kind"`
	NodeID        string `json:"node_id"`
	StepID        string `json:"step_id,omitempty"`
	StartOffsetMS int64  `json:"start_offset_ms"`
	EndOffsetMS   int64  `json:"end_offset_ms"`
	Status        string `json:"status,omitempty"`
}

// RunTimeline assembles + renders the waterfall for runID. Local
// mode; reads the SQLite store directly.
func RunTimeline(ctx context.Context, paths Paths, runID string, opts TimelineOpts, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	steps, _ := st.ListNodeSteps(ctx, runID)
	return renderTimeline(run, nodes, steps, opts, out)
}

// RunTimelineRemote is the cluster-mode counterpart.
func RunTimelineRemote(ctx context.Context, controllerURL, token, runID string, opts TimelineOpts, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("RunTimelineRemote: controller URL required")
	}
	c := client.NewWithToken(controllerURL, nil, token)
	run, err := c.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := c.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	steps, _ := c.ListNodeSteps(ctx, runID)
	return renderTimeline(run, nodes, steps, opts, out)
}

// renderTimeline does the actual waterfall layout. Pure (no I/O
// beyond writing to out) so it's straightforward to unit test.
func renderTimeline(run *store.Run, nodes []*store.Node, steps []*store.NodeStep, opts TimelineOpts, out io.Writer) error {
	if opts.Width <= 0 {
		opts.Width = 60
	}
	runStart := run.StartedAt
	var runEnd time.Time
	if run.FinishedAt != nil {
		runEnd = *run.FinishedAt
	} else {
		runEnd = time.Now()
	}
	span := runEnd.Sub(runStart)
	if span <= 0 {
		span = time.Millisecond // avoid divide-by-zero on instant runs
	}
	rows := buildTimelineRows(runStart, span, nodes, steps, opts.IncludeSteps)
	if opts.JSON {
		if rows == nil {
			rows = []TimelineRow{}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"run_id":      run.ID,
			"started_at":  run.StartedAt,
			"finished_at": run.FinishedAt,
			"duration_ms": span.Milliseconds(),
			"rows":        rows,
		})
	}
	finishedNote := "(running)"
	if run.FinishedAt != nil {
		finishedNote = "finished " + run.FinishedAt.Local().Format("15:04:05")
	}
	fmt.Fprintf(out, "%s  start %s  %s  span %s\n\n",
		run.ID, runStart.Local().Format("15:04:05"),
		finishedNote, span.Round(time.Millisecond))

	maxLabel := 0
	for _, r := range rows {
		label := timelineLabel(r)
		if n := len(label); n > maxLabel {
			maxLabel = n
		}
	}
	for _, r := range rows {
		bar := waterfallBar(r.StartOffsetMS, r.EndOffsetMS, span.Milliseconds(), opts.Width)
		fmt.Fprintf(out, "  %-*s  %s  %s\n",
			maxLabel, timelineLabel(r), bar, timelineRange(r))
	}
	return nil
}

// buildTimelineRows produces one row per node plus, when
// includeSteps, one row per step nested under its node. Step rows
// inherit the node's row by being printed immediately after.
func buildTimelineRows(runStart time.Time, span time.Duration, nodes []*store.Node, steps []*store.NodeStep, includeSteps bool) []TimelineRow {
	byNode := map[string][]*store.NodeStep{}
	for _, s := range steps {
		byNode[s.NodeID] = append(byNode[s.NodeID], s)
	}
	var rows []TimelineRow
	for _, n := range nodes {
		startMS, endMS := offsetWindow(runStart, span, n.StartedAt, n.FinishedAt)
		rows = append(rows, TimelineRow{
			Kind: "node", NodeID: n.NodeID,
			StartOffsetMS: startMS, EndOffsetMS: endMS,
			Status: n.Status,
		})
		if !includeSteps {
			continue
		}
		for _, s := range byNode[n.NodeID] {
			sStart, sEnd := offsetWindow(runStart, span, s.StartedAt, s.FinishedAt)
			rows = append(rows, TimelineRow{
				Kind: "step", NodeID: n.NodeID, StepID: s.StepID,
				StartOffsetMS: sStart, EndOffsetMS: sEnd,
				Status: s.Status,
			})
		}
	}
	return rows
}

// offsetWindow converts a (started, finished) pair to ms-offsets
// from runStart. nil started -> 0; nil finished -> end-of-span.
func offsetWindow(runStart time.Time, span time.Duration, started, finished *time.Time) (int64, int64) {
	var startMS, endMS int64
	if started != nil {
		d := started.Sub(runStart)
		if d < 0 {
			d = 0
		}
		startMS = d.Milliseconds()
	}
	if finished != nil {
		d := finished.Sub(runStart)
		if d < 0 {
			d = 0
		}
		endMS = d.Milliseconds()
	} else {
		endMS = span.Milliseconds()
	}
	return startMS, endMS
}

func timelineLabel(r TimelineRow) string {
	if r.Kind == "step" {
		return "  ↳ " + r.StepID
	}
	return r.NodeID
}

func timelineRange(r TimelineRow) string {
	return fmt.Sprintf("%s → %s",
		formatOffsetMS(r.StartOffsetMS),
		formatOffsetMS(r.EndOffsetMS))
}

func formatOffsetMS(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	mins := int(d / time.Minute)
	secs := int(d/time.Second) % 60
	return fmt.Sprintf("%02d:%02d", mins, secs)
}

// waterfallBar renders the bar segment for [startMS, endMS] inside
// a span of totalMS, using width characters total.
func waterfallBar(startMS, endMS, totalMS int64, width int) string {
	if totalMS <= 0 || width <= 0 {
		return strings.Repeat(" ", width)
	}
	startCol := int(float64(startMS) / float64(totalMS) * float64(width))
	endCol := int(float64(endMS) / float64(totalMS) * float64(width))
	if endCol < startCol {
		endCol = startCol
	}
	if endCol == startCol && endMS > startMS {
		endCol = startCol + 1
	}
	if endCol > width {
		endCol = width
	}
	if startCol > width {
		startCol = width
	}
	var b strings.Builder
	b.Grow(width)
	for i := 0; i < width; i++ {
		switch {
		case i < startCol:
			b.WriteByte('.')
		case i < endCol:
			b.WriteByte('#')
		default:
			b.WriteByte('.')
		}
	}
	return b.String()
}
