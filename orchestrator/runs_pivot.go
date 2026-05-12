// `runs list --by-pipeline` pivot: one row per pipeline with a
// status sparkline across the last N runs. Mirrors the dashboard's
// "By pipeline" view so an agent auditing a repo doesn't have to
// reshape the flat run list itself.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// SparklineStyle picks the glyph set the sparkline uses. "ascii"
// is the default; "block" uses Unicode block dots; "dot" uses bullet
// dots. Agents reading the JSON output get the raw statuses list
// and can render their own.
type SparklineStyle string

const (
	SparkAscii SparklineStyle = "ascii"
	SparkBlock SparklineStyle = "block"
	SparkDot   SparklineStyle = "dot"
)

// PipelinePivotRow is the wire shape for one pipeline row in
// `runs list --by-pipeline -o json`.
type PipelinePivotRow struct {
	Pipeline       string    `json:"pipeline"`
	RecentStatuses []string  `json:"recent_statuses"`
	Total          int       `json:"total"`
	Failures       int       `json:"failures"`
	LastRunID      string    `json:"last_run_id,omitempty"`
	LastStatus     string    `json:"last_status,omitempty"`
	LastStartedAt  time.Time `json:"last_started_at,omitempty"`
}

// pivotByPipeline groups runs by pipeline name, preserving
// most-recent-first order across pipelines.
func pivotByPipeline(runs []*store.Run, sparklineLen int) []PipelinePivotRow {
	idx := map[string]*PipelinePivotRow{}
	for _, r := range runs {
		row, ok := idx[r.Pipeline]
		if !ok {
			row = &PipelinePivotRow{Pipeline: r.Pipeline}
			idx[r.Pipeline] = row
		}
		row.Total++
		if r.Status == "failed" {
			row.Failures++
		}
		if len(row.RecentStatuses) < sparklineLen {
			row.RecentStatuses = append(row.RecentStatuses, r.Status)
		}
		if row.LastStartedAt.IsZero() || r.StartedAt.After(row.LastStartedAt) {
			row.LastStartedAt = r.StartedAt
			row.LastRunID = r.ID
			row.LastStatus = r.Status
		}
	}
	out := make([]PipelinePivotRow, 0, len(idx))
	for _, row := range idx {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastStartedAt.After(out[j].LastStartedAt)
	})
	return out
}

// renderSparkline maps a recent-statuses list to a glyph string
// per the chosen style. Status mapping mirrors the dashboard:
// success=✓, failed=✗, cancelled=⊘, running=⋯, anything else=·.
func renderSparkline(statuses []string, style SparklineStyle) string {
	var b strings.Builder
	for _, s := range statuses {
		b.WriteString(glyphFor(s, style))
	}
	return b.String()
}

func glyphFor(status string, style SparklineStyle) string {
	switch style {
	case SparkBlock:
		switch status {
		case "success":
			return "█"
		case "failed":
			return "▓"
		case "cancelled":
			return "▒"
		case "running":
			return "░"
		default:
			return " "
		}
	case SparkDot:
		switch status {
		case "success":
			return "●"
		case "failed":
			return "○"
		case "cancelled":
			return "◌"
		case "running":
			return "◐"
		default:
			return "·"
		}
	default: // ascii
		switch status {
		case "success":
			return "✓"
		case "failed":
			return "✗"
		case "cancelled":
			return "⊘"
		case "running":
			return "⋯"
		default:
			return "·"
		}
	}
}

// PivotOpts configures the pivot rendering.
type PivotOpts struct {
	SparklineLen int
	Style        SparklineStyle
	JSON         bool
	Quiet        bool
}

// RenderPipelinePivot is the entrypoint the CLI calls with the
// filtered run list. JSON output emits the wire shape; the human
// table prints PIPELINE / RECENT / RUNS / FAIL / LAST columns.
func RenderPipelinePivot(runs []*store.Run, opts PivotOpts, out io.Writer) error {
	rows := pivotByPipeline(runs, opts.SparklineLen)
	if opts.Quiet {
		for _, r := range rows {
			fmt.Fprintln(out, r.Pipeline)
		}
		return nil
	}
	if opts.JSON {
		if rows == nil {
			rows = []PipelinePivotRow{}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no pipelines match the filter")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "PIPELINE\tRECENT (%d)\tRUNS\tFAIL\tLAST\tSTATUS\n", opts.SparklineLen)
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n",
			r.Pipeline,
			renderSparkline(r.RecentStatuses, opts.Style),
			r.Total, r.Failures,
			relativeAge(r.LastStartedAt),
			r.LastStatus,
		)
	}
	return tw.Flush()
}
