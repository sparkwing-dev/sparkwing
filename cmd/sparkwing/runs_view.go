package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func renderObservedRun(ctx context.Context, reader controllerwire.RunReader, out io.Writer, detail controllerwire.RunDetail, opts runStatusOptions) error {
	if opts.view == "receipt" {
		receipt, err := reader.ReadReceipt(ctx, detail.ID)
		if err != nil {
			return err
		}
		if receipt.RunID != detail.ID {
			return invalidObservation("receipt names a different run")
		}
		return writeRunReceipt(out, receipt, opts.format)
	}
	if opts.view == "tree" {
		tree, err := readRunTree(ctx, reader, detail, opts.steps, map[string]bool{})
		if err != nil {
			return err
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
		for _, notice := range detail.Notices {
			if notice.Kind != engineid.NoticeSecretPreflightRefused {
				continue
			}
			var err error
			if opts.format == "json" {
				err = json.NewEncoder(out).Encode(notice)
			} else {
				err = writeEngineNotices(out, []engineid.Notice{notice})
			}
			if err != nil {
				return err
			}
		}
		return writeRunErrors(out, detail, opts.format)
	}
	if opts.format == "json" {
		return json.NewEncoder(out).Encode(hintedRunDetail{RunDetail: detail, Hint: planFailureHint(detail)})
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
		fmt.Fprintln(text, opsview.DisplayText(tree.Run.ID))
	} else {
		fmt.Fprintf(text, "%s%s\t%s\t%s\n", indent, opsview.DisplayText(tree.Run.ID), opsview.DisplayText(tree.Run.Inputs.Pipeline), opsview.DisplayText(tree.Run.Status))
	}
	for _, step := range tree.Run.Steps {
		if plain {
			fmt.Fprintf(text, "%s/%s\n", opsview.DisplayText(step.JobID), opsview.DisplayText(step.ID))
		} else {
			fmt.Fprintf(text, "%s  %s/%s\t%s\t%s\n", indent, opsview.DisplayText(step.JobID), opsview.DisplayText(step.ID), opsview.DisplayText(step.Status), opsview.DisplayText(step.Outcome))
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
		_, err := fmt.Fprintln(out, opsview.DisplayText(receipt.ReceiptSHA))
		return err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s\t%s\t%s\n", opsview.DisplayText(receipt.RunID), opsview.DisplayText(receipt.Pipeline), opsview.DisplayText(receipt.Status))
	fmt.Fprintf(&text, "receipt\t%s\nduration_ms\t%d\ncost\t%s\t%d\nsettled\t%t\n", opsview.DisplayText(receipt.ReceiptSHA), receipt.DurationMS, opsview.DisplayText(receipt.Cost.Currency), receipt.Cost.ComputeCents, receipt.Cost.Settled)
	for _, step := range receipt.Steps {
		fmt.Fprintf(&text, "%s\t%s\t%s\t%dms\n", opsview.DisplayText(step.JobID), opsview.DisplayText(step.ID), opsview.DisplayText(step.Outcome), step.DurationMS)
	}
	_, err := io.WriteString(out, text.String())
	return err
}

type hintedRunDetail struct {
	controllerwire.RunDetail
	Hint string `json:"hint,omitempty"`
}

type runFailure struct {
	RunID   string `json:"run_id"`
	JobID   string `json:"job_id"`
	Attempt int64  `json:"attempt"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	Hint    string `json:"hint,omitempty"`
}

func writeRunErrors(out io.Writer, detail controllerwire.RunDetail, format string) error {
	if detail.Failure != "" {
		failure := runFailure{RunID: detail.ID, Outcome: detail.Status, Reason: detail.Failure, Hint: planFailureHint(detail)}
		var err error
		if format == "json" {
			err = json.NewEncoder(out).Encode(failure)
		} else if format == "plain" {
			_, err = fmt.Fprintln(out, opsview.DisplayText(failure.RunID))
		} else {
			_, err = fmt.Fprintf(out, "%s\trun\t%s\n", opsview.DisplayText(failure.RunID), opsview.DisplayText(failure.Reason))
		}
		if err != nil {
			return err
		}
	}
	for _, job := range detail.Jobs {
		if job.Outcome != "failed" && job.Status != "failed" && job.Expansion != "failed" {
			continue
		}
		failure := runFailure{RunID: detail.ID, JobID: job.ID, Attempt: job.Attempt, Outcome: job.Outcome, Reason: jobFailureText(job)}
		var err error
		if format == "json" {
			err = json.NewEncoder(out).Encode(failure)
		} else if format == "plain" {
			_, err = fmt.Fprintln(out, opsview.DisplayText(failure.JobID))
		} else {
			_, err = fmt.Fprintf(out, "%s\t%s\tattempt=%d\t%s\n", opsview.DisplayText(failure.RunID), opsview.DisplayText(failure.JobID), failure.Attempt, opsview.DisplayText(failure.Reason))
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
		fmt.Fprintln(&text, opsview.DisplayText(detail.Status))
	} else {
		fmt.Fprintf(&text, "%s\t%s\t%s\n", opsview.DisplayText(detail.ID), opsview.DisplayText(detail.Inputs.Pipeline), opsview.DisplayText(detail.Status))
	}
	fmt.Fprintf(&text, "started_at\t%s\nfinished_at\t%s\nduration\t%s\n", displayTime(detail.StartedAt), displayTime(detail.FinishedAt), displayDuration(detail.StartedAt, detail.FinishedAt))
	if detail.Engine != nil {
		fmt.Fprintf(&text, "engine\t%s\n", opsview.DisplayText(detail.Engine.Version))
	}
	if detail.Inputs.Agent != "" {
		fmt.Fprintf(&text, "agent\t%s\n", opsview.DisplayText(detail.Inputs.Agent))
	}
	if lineage := detail.Inputs.Lineage; lineage.RetryOf != "" {
		fmt.Fprintf(&text, "retry_of\t%s\n", opsview.DisplayText(lineage.RetryOf))
		if lineage.RetryReason != "" {
			fmt.Fprintf(&text, "retry_reason\t%s\n", opsview.DisplayText(lineage.RetryReason))
		}
	}
	if err := writeEngineNotices(&text, detail.Notices); err != nil {
		return err
	}
	if opts.view == "timeline" {
		writeRunTimeline(&text, detail, opts.width)
	} else {
		if opts.format == "pretty" {
			text.WriteString("JOB\tSTATUS\tOUTCOME\tATTEMPT\tGENERATION\tDETAIL\tSTARTED_AT\tFINISHED_AT\tDURATION\n")
		}
		for _, job := range detail.Jobs {
			fmt.Fprintf(&text, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n", opsview.DisplayText(job.ID), opsview.DisplayText(job.Status), opsview.DisplayText(job.Outcome), job.Attempt, job.Generation, opsview.DisplayText(job.StatusDetail), displayTime(job.StartedAt), displayTime(job.FinishedAt), displayDuration(job.StartedAt, job.FinishedAt))
		}
	}
	for _, step := range detail.Steps {
		fmt.Fprintf(&text, "%s/%s\t%s\t%s\tattempt=%d\t%s\n", opsview.DisplayText(step.JobID), opsview.DisplayText(step.ID), opsview.DisplayText(step.Status), opsview.DisplayText(step.Outcome), step.Attempt, opsview.DisplayText(step.Reason))
	}
	for _, job := range detail.Jobs {
		if job.Error != "" {
			fmt.Fprintf(&text, "%s error:\n%s\n", opsview.DisplayText(job.ID), opsview.DisplayText(strings.TrimRight(job.Error, "\n")))
		}
	}
	writeRunError(&text, detail)
	_, err := io.WriteString(out, text.String())
	return err
}

func writeRunError(text *strings.Builder, detail controllerwire.RunDetail) {
	if detail.Failure == "" {
		return
	}
	fmt.Fprintf(text, "run error:\n%s\n", opsview.DisplayText(strings.TrimRight(detail.Failure, "\n")))
	if hint := planFailureHint(detail); hint != "" {
		fmt.Fprintf(text, "hint: %s\n", opsview.DisplayText(hint))
	}
}

func planFailureHint(detail controllerwire.RunDetail) string {
	if detail.Failure == "" || detail.Status != "failed" || len(detail.Jobs) != 0 || detail.Reason == capability.CloudCapacityUnconfigured || detail.Reason == capability.CloudCapacityUnavailable {
		return ""
	}
	return fmt.Sprintf("the pipeline failed to plan, so no job ran; fix the error above, check it from the repository with `sparkwing pipeline plan --pipeline %s`, and trigger the run again", detail.Inputs.Pipeline)
}

// jobFailureText prefers the job's recorded error, the only account of a failure reported before any attempt started.
func jobFailureText(job controllerwire.JobDetail) string {
	if job.Error != "" {
		return job.Error
	}
	return job.StatusDetail
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
		fmt.Fprintf(text, "%s\tattempt=%d\t%s\t%s\t%s\t%s\n", opsview.DisplayText(a.JobID), a.Attempt, opsview.DisplayText(a.Outcome), displayTime(a.StartedAt), displayTime(a.FinishedAt), bar)
	}
}

func displayTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Format(time.RFC3339Nano)
}

var shownEngineNotices sync.Map

func writeEngineNoticesOnce(out io.Writer, notices []engineid.Notice) error {
	// safety: only process streams are shared by the stages of one command; a buffer is its own report.
	if _, stream := out.(*os.File); !stream {
		return writeEngineNotices(out, notices)
	}
	var fresh []engineid.Notice
	for _, notice := range notices {
		key := string(notice.Kind) + "\x00" + notice.Version + "\x00" + notice.Reason
		if _, shown := shownEngineNotices.LoadOrStore(key, struct{}{}); !shown {
			fresh = append(fresh, notice)
		}
	}
	return writeEngineNotices(out, fresh)
}

func writeEngineNotices(out io.Writer, notices []engineid.Notice) error {
	for _, notice := range notices {
		if _, err := fmt.Fprintf(out, "warning: %s\n", opsview.DisplayText(notice.Reason)); err != nil {
			return err
		}
		if notice.Hint != "" {
			if _, err := fmt.Fprintf(out, "hint: %s\n", opsview.DisplayText(notice.Hint)); err != nil {
				return err
			}
		}
	}
	return nil
}

func displayDuration(start, finish *time.Time) string {
	if start == nil || finish == nil || finish.Before(*start) {
		return "-"
	}
	return finish.Sub(*start).String()
}
