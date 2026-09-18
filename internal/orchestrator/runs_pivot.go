package orchestrator

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type SparklineStyle string

const (
	SparkASCII SparklineStyle = "ascii"
	SparkBlock SparklineStyle = "block"
	SparkDot   SparklineStyle = "dot"
)

// PipelinePivotSummary says whether the totals above it cover every matching run.
type PipelinePivotSummary struct {
	Kind      string `json:"kind"`
	Truncated bool   `json:"truncated"`
	Reason    string `json:"reason,omitempty"`
}

type PipelinePivotRow struct {
	Pipeline       string    `json:"pipeline"`
	RecentStatuses []string  `json:"recent_statuses"`
	Total          int       `json:"total"`
	Failures       int       `json:"failures"`
	LastRunID      string    `json:"last_run_id,omitempty"`
	LastStatus     string    `json:"last_status,omitempty"`
	LastStartedAt  time.Time `json:"last_started_at,omitempty"`
}

// perf: a row per pipeline and a bounded sparkline, never the runs themselves,
// so memory is bounded by the pipeline count rather than the run count.
type pipelinePivot struct {
	sparklineLen int
	rows         map[string]*PipelinePivotRow
}

func newPipelinePivot(sparklineLen int) *pipelinePivot {
	return &pipelinePivot{sparklineLen: sparklineLen, rows: map[string]*PipelinePivotRow{}}
}

func (p *pipelinePivot) add(runs []*store.Run) {
	for _, r := range runs {
		row, ok := p.rows[r.Pipeline]
		if !ok {
			row = &PipelinePivotRow{Pipeline: r.Pipeline}
			p.rows[r.Pipeline] = row
		}
		row.Total++
		if r.Status == "failed" {
			row.Failures++
		}
		if len(row.RecentStatuses) < p.sparklineLen {
			row.RecentStatuses = append(row.RecentStatuses, r.Status)
		}
		if row.LastStartedAt.IsZero() || r.StartedAt.After(row.LastStartedAt) {
			row.LastStartedAt = r.StartedAt
			row.LastRunID = r.ID
			row.LastStatus = r.Status
		}
	}
}

func (p *pipelinePivot) sorted() []PipelinePivotRow {
	out := make([]PipelinePivotRow, 0, len(p.rows))
	for _, row := range p.rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastStartedAt.After(out[j].LastStartedAt)
	})
	return out
}

func pivotByPipeline(runs []*store.Run, sparklineLen int) []PipelinePivotRow {
	p := newPipelinePivot(sparklineLen)
	p.add(runs)
	return p.sorted()
}

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
	default:
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

type PivotOpts struct {
	SparklineLen int
	Style        SparklineStyle
	JSON         bool
	Quiet        bool
}

func RenderPipelinePivot(runs []*store.Run, opts PivotOpts, out io.Writer) error {
	return renderPivotRows(pivotByPipeline(runs, opts.SparklineLen), opts, out)
}

func renderPivotRows(rows []PipelinePivotRow, opts PivotOpts, out io.Writer) error {
	if opts.Quiet {
		for _, r := range rows {
			fmt.Fprintln(out, r.Pipeline)
		}
		return nil
	}
	if opts.JSON {
		return writeNDJSON(out, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no pipelines match the filter")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "PIPELINE\tRECENT (%d)\tRUNS\tFAIL\tLAST\tSTATUS\n", opts.SparklineLen)
	for _, r := range rows {
		fmt.Fprintf(
			tw, "%s\t%s\t%d\t%d\t%s\t%s\n",
			r.Pipeline,
			renderSparkline(r.RecentStatuses, opts.Style),
			r.Total, r.Failures,
			relativeAge(r.LastStartedAt),
			r.LastStatus,
		)
	}
	return tw.Flush()
}

func (p PipelinePivotSummary) write(jsonOut bool, stdout, stderr io.Writer) error {
	if jsonOut {
		return writeNDJSON(stdout, []PipelinePivotSummary{p})
	}
	if !p.Truncated {
		return nil
	}
	_, err := fmt.Fprintf(stderr,
		"these totals cover the newest matching runs only: %s\n", p.Reason)
	return err
}
