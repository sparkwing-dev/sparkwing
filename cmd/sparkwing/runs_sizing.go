package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func observeSizing(ctx context.Context, inv clisurface.Invocation, manager controllerwire.SizingManager, out io.Writer) error {
	o, err := parseRunStatistics(inv, time.Now())
	if err != nil {
		return err
	}
	if o.reset {
		summary, err := manager.ResetSizing(ctx, controllerwire.SizingReset{Pipeline: o.query.Pipeline, AllPipelines: o.all})
		if err != nil {
			return err
		}
		return writeSizingReset(out, summary, o.format)
	}
	rows, err := readSizing(ctx, manager, controllerwire.SizingQuery{Pipeline: o.query.Pipeline, Limit: 100})
	if err != nil {
		return err
	}
	if o.format == "json" {
		for _, row := range rows {
			if err := json.NewEncoder(out).Encode(row); err != nil {
				return err
			}
		}
		return nil
	}
	if o.format == "plain" {
		for _, row := range rows {
			if _, err := fmt.Fprintln(out, sizingName(row)); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PIPELINE/JOB\tSOURCE\tP50/P99\tCPU P50/P95/PEAK\tCPU CHARGE\tMEM P50/P95/PEAK\tWAIT P50/P99\tSAMPLES\tCONTENDED\tMEMO EXCLUDED\tFLOOR")
	for _, row := range rows {
		writeSizingRow(tw, sizingName(row), row.Source, row.Rollup, row.MemoExcluded)
		for _, job := range row.Jobs {
			writeSizingRow(tw, "  "+job.JobID, "", job.SizingMeasurements, 0)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, row := range rows {
		if row.Drift != "" {
			if _, err := fmt.Fprintf(out, "%s: %s\n", sizingName(row), row.Drift); err != nil {
				return err
			}
		}
	}
	return nil
}

func readSizing(ctx context.Context, manager controllerwire.SizingManager, q controllerwire.SizingQuery) ([]controllerwire.SizingDetail, error) {
	var rows []controllerwire.SizingDetail
	cursors := map[string]bool{q.Cursor: true}
	seen := map[string]map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := manager.ListSizing(ctx, q)
		if err != nil {
			return nil, err
		}
		if len(page.Sizing) > q.Limit || page.Truncated && (page.NextCursor == "" || cursors[page.NextCursor]) || !page.Truncated && page.NextCursor != "" {
			return nil, invalidObservation("sizing pagination is inconsistent")
		}
		for _, row := range page.Sizing {
			if seen[row.Repo] == nil {
				seen[row.Repo] = map[string]bool{}
			}
			if row.Pipeline == "" || row.Source == "" || seen[row.Repo][row.Pipeline] {
				return nil, invalidObservation("sizing has missing or repeated identities")
			}
			seen[row.Repo][row.Pipeline] = true
			jobs := map[string]bool{}
			for _, job := range row.Jobs {
				if job.JobID == "" || jobs[job.JobID] {
					return nil, invalidObservation("sizing has missing or repeated jobs")
				}
				jobs[job.JobID] = true
			}
			rows = append(rows, row)
		}
		if !page.Truncated {
			break
		}
		cursors[page.NextCursor] = true
		q.Cursor = page.NextCursor
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Repo == rows[j].Repo {
			return rows[i].Pipeline < rows[j].Pipeline
		}
		return rows[i].Repo < rows[j].Repo
	})
	return rows, nil
}

func sizingName(row controllerwire.SizingDetail) string {
	if row.Repo == "" {
		return row.Pipeline
	}
	return row.Repo + "/" + row.Pipeline
}

func writeSizingRow(out io.Writer, name, source string, p controllerwire.SizingMeasurements, memo int) {
	wait := "-"
	if p.WaitSampleCount > 0 {
		wait = fmtDur(p.WaitP50) + "/" + fmtDur(p.WaitP99)
	}
	contended := "-"
	if p.ContendedCount > 0 {
		contended = fmt.Sprintf("%d/%d", p.ContendedCount, p.SampleCount)
	}
	floor := "-"
	if p.FloorCores > 0 || p.FloorMemoryBytes > 0 {
		floor = fmt.Sprintf("%.1f/%s", p.FloorCores, humanBytes(p.FloorMemoryBytes))
	}
	charge := p.SustainedCores
	if charge == 0 {
		charge = p.PeakCores
	}
	fmt.Fprintf(out, "%s\t%s\t%s/%s\t%.1f/%.1f/%.1f\t%.1f\t%s/%s/%s\t%s\t%d\t%s\t%d\t%s\n", name, source, fmtDur(p.P50Duration), fmtDur(p.P99Duration), p.CPUP50, p.CPUP95, p.PeakCores, charge, humanBytes(p.MemoryP50Bytes), humanBytes(p.MemoryP95Bytes), humanBytes(p.PeakMemoryBytes), wait, p.SampleCount, contended, memo, floor)
}

func writeSizingReset(out io.Writer, s controllerwire.SizingResetSummary, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(s)
	}
	if format == "plain" {
		for _, pipeline := range s.Pipelines {
			if _, err := fmt.Fprintln(out, pipeline); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := fmt.Fprintf(out, "reset %d pipeline(s): dropped %d row(s), cleared %d pinned row(s), %d learned sample(s), %d demand floor(s); pins retained\n", len(s.Pipelines), s.RowsDeleted, s.RowsCleared, s.SamplesDropped, s.FloorsDropped)
	return err
}
