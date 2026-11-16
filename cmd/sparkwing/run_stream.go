package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// safety: a shell step's error repeats its whole script on its first line.
const failureLineBytes = 160

// hack: these restate what the job's output and the end-of-run block already show.
var prettyHiddenEvents = []string{
	"run_started", "run_success", "run_dry_run",
	"job_ready", "job_claimed", "job_started", "job_succeeded", "job_failed",
}

var prettyHiddenFrames = []string{"preparation", "work_fail_fast", "lease_keeper"}

type logStream struct {
	ctx    context.Context
	run    string
	runs   runDetailReader
	detail *controllerwire.RunDetail
	// safety: a grant shows only after a printed wait, so that wait line gets its answer.
	admitted []string
	// safety: a finished read hides queue waits, whose grants it cannot pair with them.
	historical bool
}

type runDetailReader interface {
	ReadRun(context.Context, string) (controllerwire.RunDetail, error)
}

func (s *logStream) write(out io.Writer, record controllerwire.LogRecord) error {
	subject, text, ok := s.render(record)
	if !ok {
		return nil
	}
	label := opsview.DisplayText(subject)
	_, err := fmt.Fprintf(out, "%s | %s\n", label, opsview.DisplayBlock(text, label+" |   "))
	return err
}

func (s *logStream) render(record controllerwire.LogRecord) (subject, text string, ok bool) {
	if record.Event == runstate.EventAdmissionGranted {
		index := slices.Index(s.admitted, record.JobID)
		if index < 0 {
			return "", "", false
		}
		s.admitted = slices.Delete(s.admitted, index, index+1)
		var grant struct {
			WaitMS int64 `json:"wait_ms"`
		}
		if json.Unmarshal([]byte(record.Line), &grant) == nil && grant.WaitMS > 0 {
			return record.JobID, "admission granted after " + shortDuration(time.Duration(grant.WaitMS)*time.Millisecond), true
		}
		return record.JobID, "admission granted", true
	}
	if record.Event == runstate.EventAdmissionQueued && s.historical {
		return "", "", false
	}
	if record.Event != "" {
		if slices.Contains(prettyHiddenEvents, record.Event) {
			return "", "", false
		}
		subject = "run"
		if record.JobID != "" {
			subject = record.JobID
			if record.StepID != "" {
				subject += "/" + record.StepID
			}
		}
		return subject, s.eventText(record), true
	}
	subject = "run"
	if record.Subject != nil {
		if attempt := record.Subject.Attempt; attempt != nil {
			subject = attempt.NodeID
			if attempt.Attempt > 1 {
				subject += "#" + strconv.FormatInt(attempt.Attempt, 10)
			}
		} else if unit := record.Subject.Unit; unit != nil {
			subject = string(unit.Kind)
		}
	}
	frame, ok := decodeFrame(record.Line)
	if !ok {
		return subject, record.Line, true
	}
	if slices.Contains(prettyHiddenFrames, frame.Event) {
		return "", "", false
	}
	step := cmp.Or(frame.Step, frame.Message)
	switch frame.Event {
	case "step_start":
		return subject, step + " started", true
	case "step_end":
		var attrs map[string]json.RawMessage
		if json.Unmarshal(frame.Attrs, &attrs) == nil {
			return subject, stepEndText(step, attrs), true
		}
	}
	return subject, frameLine(frame), true
}

func stepEndText(step string, attrs map[string]json.RawMessage) string {
	outcome := eventValue(attrs["outcome"])
	var duration string
	if ms, err := strconv.ParseInt(eventValue(attrs["duration_ms"]), 10, 64); err == nil {
		duration = shortDuration(time.Duration(ms) * time.Millisecond)
	}
	switch {
	case outcome == "success" && duration != "":
		return step + " succeeded in " + duration
	case outcome == "success":
		return step + " succeeded"
	case outcome == "failed" && duration != "":
		return step + " failed after " + duration
	case duration != "":
		return step + " " + outcome + " after " + duration
	}
	return strings.TrimSpace(step + " " + outcome)
}

func (s *logStream) eventText(record controllerwire.LogRecord) string {
	words := strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(record.Event, "job_"), "run_"), "_", " ")
	detail := eventDetail(record.Line)
	var payload struct{ Reason string }
	if record.Event == runstate.EventSecretPreflightRefused {
		var refusal struct{ Message string }
		if json.Unmarshal([]byte(record.Line), &refusal) == nil && refusal.Message != "" {
			return refusal.Message
		}
	}
	if record.Event == runstate.EventAdmissionQueued {
		var queued struct {
			BlockingReason string `json:"blocking_reason"`
		}
		if json.Unmarshal([]byte(record.Line), &queued) == nil && queued.BlockingReason != "" {
			return "waiting for admission: " + queued.BlockingReason
		}
		return "waiting for admission"
	}
	if record.Event == "job_cancelled" && json.Unmarshal([]byte(record.Line), &payload) == nil && payload.Reason == "upstream_failed" {
		if blockers := s.blockers(record.JobID); len(blockers) > 0 {
			return "cancelled: " + strings.Join(blockers, " and ") + " did not succeed"
		}
		return "cancelled: a job it needs did not succeed"
	}
	if detail == "" || strings.HasPrefix(detail, words) {
		return cmp.Or(detail, words)
	}
	return words + ": " + detail
}

func (s *logStream) blockers(job string) []string {
	if s.runs == nil || s.ctx == nil {
		return nil
	}
	if s.detail == nil || upstreamBlockers(*s.detail, job) == nil {
		detail, err := s.runs.ReadRun(s.ctx, s.run)
		if err != nil || detail.ID == "" {
			return nil
		}
		s.detail = &detail
	}
	return upstreamBlockers(*s.detail, job)
}

func upstreamBlockers(detail controllerwire.RunDetail, job string) []string {
	outcomes := map[string]string{}
	var needs []string
	for _, j := range detail.Jobs {
		outcomes[j.ID] = j.Outcome
		if j.ID == job {
			needs = j.Needs
		}
	}
	var blocked []string
	for _, need := range needs {
		switch outcomes[need] {
		case "success", "memoized", "skipped", "":
		default:
			blocked = append(blocked, need)
		}
	}
	return blocked
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	case d < time.Minute:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	}
	return d.Round(time.Second).String()
}

func writeFailureBlock(out io.Writer, detail controllerwire.RunDetail, job, profile string) error {
	var text strings.Builder
	var failed []string
	for _, j := range detail.Jobs {
		if job != "" && j.ID != job {
			continue
		}
		label := opsview.DisplayText(j.ID)
		switch {
		case j.Outcome == "failed" || j.Status == "failed" || j.Expansion == "failed":
			failed = append(failed, j.ID)
			header := label + " failed"
			if step := failedStep(detail, j); step != "" {
				header += " in step " + opsview.DisplayText(step)
			}
			lines := opsview.DisplayLines(jobFailureText(j), failureLineBytes)
			if len(lines) == 1 && lines[0] == "" {
				fmt.Fprintln(&text, header)
				continue
			}
			fmt.Fprintln(&text, header+":")
			for _, line := range lines {
				fmt.Fprintln(&text, "  "+line)
			}
		case j.Outcome == "cancelled":
			if blockers := upstreamBlockers(detail, j.ID); j.Reason == "upstream_failed" && len(blockers) > 0 {
				fmt.Fprintf(&text, "%s was cancelled because %s did not succeed\n", label, opsview.DisplayText(strings.Join(blockers, " and ")))
			} else {
				fmt.Fprintln(&text, label+" was cancelled")
			}
		}
	}
	if len(failed) == 0 && detail.Failure != "" && job == "" {
		fmt.Fprintln(&text, "run failed:")
		for _, line := range opsview.DisplayLines(detail.Failure, failureLineBytes) {
			fmt.Fprintln(&text, "  "+line)
		}
		if hint := planFailureHint(detail); hint != "" {
			fmt.Fprintln(&text, "hint: "+opsview.DisplayText(hint))
		}
	}
	if text.Len() == 0 {
		return nil
	}
	if len(failed) > 0 {
		run := detail.ID
		if detail.Number > 0 {
			run = strconv.FormatInt(detail.Number, 10)
		}
		suffix := ""
		if profile != "" {
			suffix = " --profile " + shellWord(profile)
		}
		text.WriteString("\n")
		if job == "" {
			fmt.Fprintf(&text, "Show the failed job's log:  sparkwing runs logs --run %s --job %s%s\n", opsview.DisplayText(run), shellWord(failed[0]), suffix)
		}
		fmt.Fprintf(&text, "Retry the failed jobs:      sparkwing runs retry --failed --run %s%s\n", opsview.DisplayText(run), suffix)
	}
	_, err := io.WriteString(out, "\n"+text.String())
	return err
}

// safety: run detail leaves steps to a separate read; without them the block names only the job.
func withSteps(ctx context.Context, reader any, detail controllerwire.RunDetail) controllerwire.RunDetail {
	steps, ok := reader.(interface {
		ReadSteps(context.Context, string) (controllerwire.RunSteps, error)
	})
	if !ok || len(detail.Steps) > 0 {
		return detail
	}
	if read, err := steps.ReadSteps(ctx, detail.ID); err == nil {
		detail.Steps = read.Steps
	}
	return detail
}

func failedStep(detail controllerwire.RunDetail, job controllerwire.JobDetail) string {
	for _, step := range detail.Steps {
		if step.JobID == job.ID && step.Attempt == job.Attempt && step.Outcome == "failed" {
			return step.ID
		}
	}
	return ""
}

func shellWord(value string) string {
	if value != "" && !strings.ContainsFunc(value, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		return value
	}
	return opsview.DisplayText("'" + strings.ReplaceAll(value, "'", `'\''`) + "'")
}
