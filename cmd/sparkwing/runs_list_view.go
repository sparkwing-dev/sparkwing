package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func renderRunList(ctx context.Context, reader controllerwire.RunReader, out, diagnostics io.Writer, page controllerwire.RunPage, o runListOptions) error {
	if o.pivot {
		return renderPipelineList(ctx, reader, out, diagnostics, page, o)
	}
	if o.group != "" {
		if err := renderRunFailures(ctx, reader, out, page.Runs, o); err != nil {
			return err
		}
	} else {
		for _, run := range page.Runs {
			if err := writeListedRun(out, run, o.format, o.quiet); err != nil {
				return err
			}
		}
	}
	return writePageInfo(out, diagnostics, page.NextCursor, page.Truncated, o.format, o.quiet)
}

func writeListedRun(out io.Writer, run controllerwire.RunDetail, format string, quiet bool) error {
	if quiet || format == "plain" {
		if format == "json" {
			return json.NewEncoder(out).Encode(struct {
				ID string `json:"id"`
			}{run.ID})
		}
		_, err := fmt.Fprintln(out, run.ID)
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(struct {
			ID         string     `json:"id"`
			Number     int64      `json:"number"`
			Pipeline   string     `json:"pipeline"`
			Status     string     `json:"status"`
			Ref        string     `json:"ref,omitempty"`
			SHA        string     `json:"sha,omitempty"`
			StartedAt  *time.Time `json:"started_at,omitempty"`
			FinishedAt *time.Time `json:"finished_at,omitempty"`
		}{run.ID, run.Number, run.Inputs.Pipeline, run.Status, run.Inputs.Git.Ref, run.Inputs.Git.SHA, run.StartedAt, run.FinishedAt})
	}
	_, err := fmt.Fprintf(out, "%s (#%d)  %s  %s  %s  %s\n", run.ID, run.Number, run.Inputs.Pipeline, run.Status, run.Inputs.Git.Ref, shortSHAOrDash(run.Inputs.Git.SHA))
	return err
}

type listedFailure struct {
	RunID    string `json:"run_id"`
	Pipeline string `json:"pipeline"`
	JobID    string `json:"job_id,omitempty"`
	StepID   string `json:"step_id,omitempty"`
	Message  string `json:"message,omitempty"`
}

type failureCluster struct {
	Key         string `json:"key"`
	Count       int    `json:"count"`
	SampleError string `json:"sample_error,omitempty"`
}

func runFailures(ctx context.Context, reader controllerwire.RunReader, run controllerwire.RunDetail, group string) ([]listedFailure, error) {
	base := listedFailure{RunID: run.ID, Pipeline: run.Inputs.Pipeline}
	var rows []listedFailure
	if group == "step" {
		steps, err := reader.ReadSteps(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		for _, step := range steps.Steps {
			if step.Outcome != "failed" {
				continue
			}
			row := base
			row.JobID = step.JobID
			row.StepID = step.ID
			row.Message = step.Reason
			rows = append(rows, row)
		}
	} else {
		for _, job := range run.Jobs {
			if job.Outcome != "failed" {
				continue
			}
			row := base
			row.JobID = job.ID
			row.Message = jobFailureText(job)
			rows = append(rows, row)
			if group == "run" {
				break
			}
		}
	}
	if len(rows) == 0 {
		rows = append(rows, base)
	}
	return rows, nil
}

func renderRunFailures(ctx context.Context, reader controllerwire.RunReader, out io.Writer, runs []controllerwire.RunDetail, o runListOptions) error {
	clusters := map[string]*failureCluster{}
	for _, run := range runs {
		rows, err := runFailures(ctx, reader, run, o.group)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, row := range rows {
			if o.group == "run" {
				if o.format == "json" && !o.quiet {
					err = json.NewEncoder(out).Encode(row)
				} else if o.format == "plain" || o.quiet {
					err = writeListedRun(out, run, o.format, true)
				} else {
					_, err = fmt.Fprintf(out, "%s  %s  %s  %s\n", row.RunID, row.Pipeline, row.JobID, truncateOneLine(row.Message, 160))
				}
				if err != nil {
					return err
				}
				continue
			}
			key := row.JobID
			if o.group == "step" && row.StepID != "" {
				key += "/" + row.StepID
			}
			if key == "" {
				key = "(unknown)"
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			if clusters[key] == nil {
				clusters[key] = &failureCluster{Key: key, SampleError: row.Message}
			}
			clusters[key].Count++
		}
	}
	keys := make([]string, 0, len(clusters))
	for key := range clusters {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if clusters[keys[i]].Count != clusters[keys[j]].Count {
			return clusters[keys[i]].Count > clusters[keys[j]].Count
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		row := clusters[key]
		var err error
		switch {
		case o.format == "json" && !o.quiet:
			err = json.NewEncoder(out).Encode(row)
		case o.format == "json":
			err = json.NewEncoder(out).Encode(struct {
				Key string `json:"key"`
			}{key})
		case o.format == "plain" || o.quiet:
			_, err = fmt.Fprintln(out, key)
		default:
			_, err = fmt.Fprintf(out, "%s  %d  %s\n", key, row.Count, truncateOneLine(row.SampleError, 160))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type listedPipeline struct {
	Pipeline       string   `json:"pipeline"`
	Total          int      `json:"total"`
	Failures       int      `json:"failures"`
	LastRunID      string   `json:"last_run_id"`
	LastStatus     string   `json:"last_status"`
	RecentStatuses []string `json:"recent_statuses"`
	lastNumber     int64
}

func renderPipelineList(ctx context.Context, reader controllerwire.RunReader, out, diagnostics io.Writer, page controllerwire.RunPage, o runListOptions) error {
	groups := map[string]*listedPipeline{}
	add := func(page controllerwire.RunPage) error {
		for _, run := range page.Runs {
			name := run.Inputs.Pipeline
			row := groups[name]
			if row == nil {
				row = &listedPipeline{Pipeline: name, LastRunID: run.ID, LastStatus: run.Status, lastNumber: run.Number}
				groups[name] = row
			}
			row.Total++
			if run.Status == "failed" {
				row.Failures++
			}
			if len(row.RecentStatuses) < o.sparkline {
				row.RecentStatuses = append(row.RecentStatuses, run.Status)
			}
		}
		return nil
	}
	_ = add(page)
	if page.Truncated {
		query := o.query
		query.Cursor = page.NextCursor
		boundary := int64(0)
		if len(page.Runs) > 0 {
			boundary = page.Runs[len(page.Runs)-1].Number
		}
		err := visitRunPages(ctx, reader, query, func(next controllerwire.RunPage) error {
			if boundary > 0 && len(next.Runs) > 0 && next.Runs[0].Number >= boundary {
				return invalidObservation("pipeline listing repeated a run")
			}
			if len(next.Runs) > 0 {
				boundary = next.Runs[len(next.Runs)-1].Number
			}
			return add(next)
		})
		if err != nil {
			return err
		}
	}
	rows := make([]*listedPipeline, 0, len(groups))
	for _, row := range groups {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].lastNumber > rows[j].lastNumber })
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if o.format == "pretty" && !o.quiet {
		if _, err := fmt.Fprintln(tw, "PIPELINE\tRECENT\tRUNS\tFAIL\tLAST\tSTATUS"); err != nil {
			return err
		}
	}
	for _, row := range rows {
		var err error
		switch {
		case o.format == "json" && !o.quiet:
			err = json.NewEncoder(out).Encode(row)
		case o.format == "json":
			err = json.NewEncoder(out).Encode(struct {
				Pipeline string `json:"pipeline"`
			}{row.Pipeline})
		case o.format == "plain" || o.quiet:
			_, err = fmt.Fprintln(out, row.Pipeline)
		default:
			_, err = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n", row.Pipeline, pipelineSparkline(row.RecentStatuses, o.style), row.Total, row.Failures, row.LastRunID, row.LastStatus)
		}
		if err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return writePageInfo(out, diagnostics, "", false, o.format, o.quiet)
}

func pipelineSparkline(statuses []string, style string) string {
	glyphs := []string{"+", "x", "-", "~", "."}
	if style == "block" {
		glyphs = []string{"█", "▓", "▒", "░", " "}
	}
	if style == "dot" {
		glyphs = []string{"●", "○", "◌", "◐", "·"}
	}
	var out strings.Builder
	for _, status := range statuses {
		i := 4
		switch status {
		case "success":
			i = 0
		case "failed":
			i = 1
		case "cancelled":
			i = 2
		case "running":
			i = 3
		}
		out.WriteString(glyphs[i])
	}
	return out.String()
}
