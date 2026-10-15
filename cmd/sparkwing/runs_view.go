package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func renderObservedRun(ctx context.Context, reader controllerwire.RunReader, out io.Writer, detail controllerwire.RunDetail, opts runStatusOptions) error {
	if opts.view == "receipt" {
		receipt, err := reader.ReadReceipt(ctx, detail.ID)
		if err != nil {
			return exitError(3, err)
		}
		if receipt.RunID != detail.ID {
			return exitError(3, invalidObservation("receipt names a different run"))
		}
		return writeRunReceipt(out, receipt, opts.format)
	}
	if opts.view == "tree" {
		tree, err := readRunTree(ctx, reader, detail, opts.steps, map[string]bool{})
		if err != nil {
			return exitError(3, err)
		}
		if opts.format == "json" {
			return json.NewEncoder(out).Encode(tree)
		}
		var text strings.Builder
		writeRunTree(&text, tree, "", opts.format == "plain")
		_, err = io.WriteString(out, text.String())
		return err
	}
	if opts.view == "errors" {
		return writeRunErrors(out, detail, opts.format)
	}
	if opts.format == "json" {
		return json.NewEncoder(out).Encode(detail)
	}
	return writeRunDetail(out, detail, opts)
}

func invalidObservation(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}

type runTree struct {
	Run      controllerwire.RunDetail `json:"run"`
	Children []*runTree               `json:"children"`
}

func readRunTree(ctx context.Context, reader controllerwire.RunReader, detail controllerwire.RunDetail, includeSteps bool, seen map[string]bool) (*runTree, error) {
	if detail.ID == "" || seen[detail.ID] {
		return nil, invalidObservation("run tree contains a missing or repeated identity")
	}
	seen[detail.ID] = true
	tree := &runTree{Run: detail, Children: []*runTree{}}
	query := controllerwire.RunQuery{ParentRunID: detail.ID, Limit: 100}
	cursors := map[string]bool{}
	for {
		page, err := reader.ListRuns(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, child := range page.Runs {
			if includeSteps {
				steps, err := reader.ReadSteps(ctx, child.ID)
				if err != nil {
					return nil, err
				}
				child.Steps = steps.Steps
			}
			branch, err := readRunTree(ctx, reader, child, includeSteps, seen)
			if err != nil {
				return nil, err
			}
			tree.Children = append(tree.Children, branch)
		}
		if !page.Truncated {
			return tree, nil
		}
		if page.NextCursor == "" || cursors[page.NextCursor] {
			return nil, invalidObservation("run tree pagination did not advance")
		}
		cursors[page.NextCursor] = true
		query.Cursor = page.NextCursor
	}
}

func writeRunTree(text *strings.Builder, tree *runTree, indent string, plain bool) {
	if plain {
		fmt.Fprintln(text, tree.Run.ID)
	} else {
		fmt.Fprintf(text, "%s%s\t%s\t%s\n", indent, tree.Run.ID, tree.Run.Inputs.Pipeline, tree.Run.Status)
	}
	for _, step := range tree.Run.Steps {
		if plain {
			fmt.Fprintf(text, "%s/%s\n", step.JobID, step.ID)
		} else {
			fmt.Fprintf(text, "%s  %s/%s\t%s\t%s\n", indent, step.JobID, step.ID, step.Status, step.Outcome)
		}
	}
	for _, child := range tree.Children {
		writeRunTree(text, child, indent+"  ", plain)
	}
}

func writeRunReceipt(out io.Writer, receipt controllerwire.Receipt, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(receipt)
	}
	if format == "plain" {
		_, err := fmt.Fprintln(out, receipt.ReceiptSHA)
		return err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s\t%s\t%s\n", receipt.RunID, receipt.Pipeline, receipt.Status)
	fmt.Fprintf(&text, "receipt\t%s\nduration_ms\t%d\ncost\t%s\t%d\nsettled\t%t\n", receipt.ReceiptSHA, receipt.DurationMS, receipt.Cost.Currency, receipt.Cost.ComputeCents, receipt.Cost.Settled)
	for _, step := range receipt.Steps {
		fmt.Fprintf(&text, "%s\t%s\t%s\t%dms\n", step.JobID, step.ID, step.Outcome, step.DurationMS)
	}
	_, err := io.WriteString(out, text.String())
	return err
}

type runFailure struct {
	RunID   string `json:"run_id"`
	JobID   string `json:"job_id"`
	Attempt int64  `json:"attempt"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

func writeRunErrors(out io.Writer, detail controllerwire.RunDetail, format string) error {
	for _, job := range detail.Jobs {
		if job.Outcome != "failed" && job.Status != "failed" {
			continue
		}
		failure := runFailure{RunID: detail.ID, JobID: job.ID, Attempt: job.Attempt, Outcome: job.Outcome, Reason: job.StatusDetail}
		var err error
		if format == "json" {
			err = json.NewEncoder(out).Encode(failure)
		} else if format == "plain" {
			_, err = fmt.Fprintln(out, failure.JobID)
		} else {
			_, err = fmt.Fprintf(out, "%s\t%s\tattempt=%d\t%s\n", failure.RunID, failure.JobID, failure.Attempt, failure.Reason)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeRunDetail(out io.Writer, detail controllerwire.RunDetail, opts runStatusOptions) error {
	var text strings.Builder
	if opts.format == "plain" {
		fmt.Fprintln(&text, detail.Status)
		for _, step := range detail.Steps {
			fmt.Fprintf(&text, "%s/%s\n", step.JobID, step.ID)
		}
		_, err := io.WriteString(out, text.String())
		return err
	}
	fmt.Fprintf(&text, "%s\t%s\t%s\n", detail.ID, detail.Inputs.Pipeline, detail.Status)
	if detail.Engine != nil {
		fmt.Fprintf(&text, "engine\t%s\n", detail.Engine.Version)
	}
	for _, notice := range detail.Notices {
		fmt.Fprintf(&text, "warning: %s\n", notice.Reason)
		if notice.Hint != "" {
			fmt.Fprintf(&text, "hint: %s\n", notice.Hint)
		}
	}
	if opts.view == "timeline" {
		writeRunTimeline(&text, detail, opts.width)
	} else {
		if opts.format == "pretty" {
			text.WriteString("JOB\tSTATUS\tOUTCOME\tATTEMPT\tGENERATION\n")
		}
		for _, job := range detail.Jobs {
			fmt.Fprintf(&text, "%s\t%s\t%s\t%d\t%d\n", job.ID, job.Status, job.Outcome, job.Attempt, job.Generation)
		}
	}
	for _, step := range detail.Steps {
		fmt.Fprintf(&text, "%s/%s\t%s\t%s\tattempt=%d\n", step.JobID, step.ID, step.Status, step.Outcome, step.Attempt)
	}
	_, err := io.WriteString(out, text.String())
	return err
}

func writeRunTimeline(text *strings.Builder, detail controllerwire.RunDetail, width int) {
	attempts := append([]controllerwire.AttemptDetail(nil), detail.Attempts...)
	sort.SliceStable(attempts, func(i, j int) bool {
		if attempts[i].StartedAt == nil {
			return false
		}
		if attempts[j].StartedAt == nil {
			return true
		}
		return attempts[i].StartedAt.Before(*attempts[j].StartedAt)
	})
	var start, end time.Time
	for _, a := range attempts {
		if a.StartedAt != nil && (start.IsZero() || a.StartedAt.Before(start)) {
			start = *a.StartedAt
		}
		if a.FinishedAt != nil && a.FinishedAt.After(end) {
			end = *a.FinishedAt
		}
	}
	for _, a := range attempts {
		bar := ""
		if a.StartedAt != nil && a.FinishedAt != nil && end.After(start) && !a.FinishedAt.Before(*a.StartedAt) {
			offset := int(float64(a.StartedAt.Sub(start)) / float64(end.Sub(start)) * float64(width))
			length := max(1, int(float64(a.FinishedAt.Sub(*a.StartedAt))/float64(end.Sub(start))*float64(width)))
			bar = strings.Repeat(" ", offset) + strings.Repeat("=", length)
		}
		fmt.Fprintf(text, "%s\tattempt=%d\t%s\t%s\t%s\t%s\n", a.JobID, a.Attempt, a.Outcome, displayTime(a.StartedAt), displayTime(a.FinishedAt), bar)
	}
}

func displayTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Format(time.RFC3339Nano)
}
