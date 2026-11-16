package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type timingPhase int

// safety: overlay gives a run-level instant the highest phase of any job, so this order is that precedence.
const (
	phaseIdle timingPhase = iota
	phaseGap
	phaseApproval
	phaseQueue
	phaseAdmit
	phaseSetup
	phaseRunning
	phaseCount
)

type phaseDurations [phaseCount]time.Duration

func (p phaseDurations) sum() time.Duration {
	var total time.Duration
	for _, d := range p {
		total += d
	}
	return total
}

type change struct {
	at    time.Time
	phase timingPhase
}

type timingSpan struct {
	from, to time.Time
	phase    timingPhase
}

type runTiming struct {
	ID        string
	Number    int64
	Status    string
	SHA       string
	Rerun     bool
	Wall      time.Duration
	Phases    phaseDurations
	Jobs      map[string]jobRunTiming
	Failures  []timingFailureKey
	StepShift time.Duration
}

type jobRunTiming struct {
	Phases   phaseDurations
	Total    time.Duration
	Attempts int64
	Outcome  string
}

type timingFailureKey struct {
	Job, Step, Outcome, Reason string
}

type percentiles struct {
	P50 time.Duration `json:"p50_ns"`
	P90 time.Duration `json:"p90_ns"`
	Max time.Duration `json:"max_ns"`
}

type timingRunRecord struct {
	Kind      string        `json:"kind"`
	Number    int64         `json:"number"`
	ID        string        `json:"id"`
	Status    string        `json:"status"`
	Rerun     bool          `json:"rerun"`
	WallNS    time.Duration `json:"wall_ns"`
	QueueNS   time.Duration `json:"queue_ns"`
	AdmitNS   time.Duration `json:"admit_ns"`
	Approval  time.Duration `json:"approval_ns"`
	SetupNS   time.Duration `json:"setup_ns"`
	RunningNS time.Duration `json:"running_ns"`
	GapNS     time.Duration `json:"gap_ns"`
	IdleNS    time.Duration `json:"idle_ns"`
	ShiftNS   time.Duration `json:"step_clock_shift_ns"`
}

type timingSummaryRecord struct {
	Kind            string      `json:"kind"`
	Pipeline        string      `json:"pipeline"`
	Runs            int         `json:"runs"`
	Success         int         `json:"success"`
	Failed          int         `json:"failed"`
	Cancelled       int         `json:"cancelled"`
	Reruns          int         `json:"reruns"`
	RepeatedCommits int         `json:"repeated_commits"`
	Wall            percentiles `json:"wall"`
	Queue           percentiles `json:"queue"`
	Admit           percentiles `json:"admit"`
	Approval        percentiles `json:"approval"`
	Setup           percentiles `json:"setup"`
	Running         percentiles `json:"running"`
	Gap             percentiles `json:"gap"`
	Idle            percentiles `json:"idle"`
}

type timingJobRecord struct {
	Kind     string      `json:"kind"`
	Job      string      `json:"job"`
	Runs     int         `json:"runs"`
	Failed   int         `json:"failed"`
	Retries  int64       `json:"retries"`
	Wait     percentiles `json:"wait"`
	Queue    percentiles `json:"queue"`
	Admit    percentiles `json:"admit"`
	Approval percentiles `json:"approval"`
	Setup    percentiles `json:"setup"`
	Running  percentiles `json:"running"`
	Gap      percentiles `json:"gap"`
	Total    percentiles `json:"total"`
}

type timingPage struct {
	Kind                  string `json:"kind"`
	Jobs                  int    `json:"jobs"`
	JobsReturned          int    `json:"jobs_returned"`
	FailureGroups         int    `json:"failure_groups"`
	FailureGroupsReturned int    `json:"failure_groups_returned"`
	Truncated             bool   `json:"truncated"`
}

type timingFailureRecord struct {
	Kind    string  `json:"kind"`
	Job     string  `json:"job"`
	Step    string  `json:"step,omitempty"`
	Outcome string  `json:"outcome"`
	Reason  string  `json:"reason,omitempty"`
	Count   int     `json:"count"`
	Runs    []int64 `json:"runs"`
}

func runRunsTiming(ctx context.Context, inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	if _, err := parseRunsTiming(inv); err != nil {
		return err
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return observationError(err)
	}
	defer closeTransport()
	runs, err := client.NewRunReader(options)
	if err != nil {
		return observationError(err)
	}
	logs, err := client.NewLogReader(options)
	if err != nil {
		return observationError(err)
	}
	return observeRunsTiming(ctx, inv, runs, logs, os.Stdout)
}

func parseRunsTiming(inv clisurface.Invocation) (controllerwire.RunQuery, error) {
	q := controllerwire.RunQuery{Pipeline: inv.Value("pipeline"), RootOnly: true, Limit: 20}
	if inv.Has("last") {
		last, err := strconv.Atoi(inv.Value("last"))
		if err != nil {
			return q, &clisurface.UsageError{Path: inv.Command.Path, Message: "--last must be a positive number"}
		}
		q.Limit = last
	}
	if q.Pipeline == "" || q.Limit < 1 {
		return q, &clisurface.UsageError{Path: inv.Command.Path, Message: "--pipeline is required and --last must be positive"}
	}
	return q, nil
}

func observeRunsTiming(ctx context.Context, inv clisurface.Invocation, runs controllerwire.RunReader, logs controllerwire.LogReader, out io.Writer) error {
	query, err := parseRunsTiming(inv)
	if err != nil {
		return err
	}
	want := query.Limit
	var selected []controllerwire.RunDetail
	errEnough := errors.New("enough runs")
	err = visitRunPages(ctx, runs, query, func(page controllerwire.RunPage) error {
		for _, run := range page.Runs {
			if run.StartedAt == nil || run.FinishedAt == nil || run.Status == "dry_run" {
				continue
			}
			selected = append(selected, run)
			if len(selected) == want {
				return errEnough
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		return observationError(err)
	}
	timings := make([]runTiming, 0, len(selected))
	for _, run := range selected {
		steps, err := runs.ReadSteps(ctx, run.ID)
		if err != nil {
			return observationError(err)
		}
		events, err := readRunEvents(ctx, logs, run.ID)
		if err != nil {
			return observationError(err)
		}
		timings = append(timings, measureRun(run, steps.Steps, events))
	}
	rows := 20
	if inv.Has("top") {
		if rows, err = strconv.Atoi(inv.Value("top")); err != nil {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "--top must be a positive number"}
		}
	}
	return writeRunsTiming(out, query.Pipeline, inv.Flags["job"], rows, timings, inv.Output(color.IsInteractiveStdout()))
}

func readRunEvents(ctx context.Context, logs controllerwire.LogReader, run string) ([]controllerwire.LogRecord, error) {
	var events []controllerwire.LogRecord
	query := controllerwire.LogQuery{EventsOnly: true}
	for {
		page, err := logs.ReadLogs(ctx, run, query)
		if err != nil {
			return nil, err
		}
		for _, record := range page.Records {
			if record.Event != "" && record.Time != nil && record.RunID == run {
				events = append(events, record)
			}
		}
		if !page.Truncated {
			return events, nil
		}
		if page.NextCursor == "" || page.NextCursor == query.Cursor {
			return nil, invalidObservation("event pagination did not advance")
		}
		query.Cursor = page.NextCursor
	}
}

func measureRun(run controllerwire.RunDetail, steps []controllerwire.StepDetail, events []controllerwire.LogRecord) runTiming {
	timing := runTiming{ID: run.ID, Number: run.Number, Status: run.Status, SHA: run.Inputs.Git.SHA, Rerun: run.Retry != nil, Jobs: map[string]jobRunTiming{}}
	start, end := *run.StartedAt, *run.FinishedAt
	timing.Wall = end.Sub(start)
	byJob := map[string][]controllerwire.LogRecord{}
	for _, event := range events {
		if event.JobID != "" {
			byJob[event.JobID] = append(byJob[event.JobID], event)
		}
	}
	stepsByJob := map[string][]controllerwire.StepDetail{}
	for _, step := range steps {
		stepsByJob[step.JobID] = append(stepsByJob[step.JobID], step)
	}
	attempts := map[string]int64{}
	for _, attempt := range run.Attempts {
		attempts[attempt.JobID] = max(attempts[attempt.JobID], attempt.Attempt)
	}
	var spans []timingSpan
	for _, job := range run.Jobs {
		jobSpans, shift := jobTimeline(byJob[job.ID], stepsByJob[job.ID], end)
		spans = append(spans, jobSpans...)
		timing.StepShift = max(timing.StepShift, shift)
		requested := run.CancelRequestedAt != nil || job.CancelRequestedAt != nil || cancelRequested(byJob[job.ID])
		if job.Outcome == "failed" || job.Outcome == "cancelled" && !requested && job.Reason != "upstream_failed" {
			for _, step := range failingSteps(stepsByJob[job.ID], job.Attempt) {
				timing.Failures = append(timing.Failures, timingFailureKey{Job: job.ID, Step: step, Outcome: job.Outcome, Reason: job.Reason})
			}
		}
		if len(jobSpans) == 0 {
			continue
		}
		var jobTiming jobRunTiming
		for _, span := range jobSpans {
			jobTiming.Phases[span.phase] += span.to.Sub(span.from)
		}
		jobTiming.Total = jobSpans[len(jobSpans)-1].to.Sub(jobSpans[0].from)
		jobTiming.Attempts, jobTiming.Outcome = attempts[job.ID], job.Outcome
		timing.Jobs[job.ID] = jobTiming
	}
	for _, span := range overlay(spans, start, end) {
		timing.Phases[span.phase] += span.to.Sub(span.from)
	}
	return timing
}

// safety: steps carry the runner's clock and events the store's, so a job's steps move by one offset into its window.
func alignStepClock(steps []controllerwire.StepDetail, started, end time.Time) ([]controllerwire.StepDetail, time.Duration) {
	var earliest, latest time.Time
	for _, step := range steps {
		if step.StartedAt == nil {
			continue
		}
		if earliest.IsZero() || step.StartedAt.Before(earliest) {
			earliest = *step.StartedAt
		}
		finish := *step.StartedAt
		if step.FinishedAt != nil {
			finish = *step.FinishedAt
		}
		if finish.After(latest) {
			latest = finish
		}
	}
	var shift time.Duration
	switch {
	case earliest.IsZero() || started.IsZero():
	case earliest.Before(started):
		shift = started.Sub(earliest)
	case latest.After(end):
		shift = max(end.Sub(latest), started.Sub(earliest))
	}
	if shift == 0 {
		return steps, 0
	}
	moved := make([]controllerwire.StepDetail, len(steps))
	for i, step := range steps {
		moved[i] = step
		if step.StartedAt != nil {
			from := step.StartedAt.Add(shift)
			moved[i].StartedAt = &from
		}
		if step.FinishedAt != nil {
			to := step.FinishedAt.Add(shift)
			moved[i].FinishedAt = &to
		}
	}
	return moved, shift
}

// safety: each attempt may run on a different runner, so each attempt's steps align to their own start event.
func alignAttempts(steps []controllerwire.StepDetail, changes []change, end time.Time) ([]controllerwire.StepDetail, time.Duration) {
	var starts []time.Time
	for _, c := range changes {
		if c.phase == phaseSetup {
			starts = append(starts, c.at)
		}
	}
	byAttempt := map[int64][]controllerwire.StepDetail{}
	var order []int64
	for _, step := range steps {
		if _, seen := byAttempt[step.Attempt]; !seen {
			order = append(order, step.Attempt)
		}
		byAttempt[step.Attempt] = append(byAttempt[step.Attempt], step)
	}
	var aligned []controllerwire.StepDetail
	var largest time.Duration
	for _, attempt := range order {
		var from time.Time
		to := end
		if n := len(starts); n > 0 {
			i := min(max(int(attempt), 1), n) - 1
			from = starts[i]
			if i+1 < n {
				to = starts[i+1]
			}
		}
		moved, shift := alignStepClock(byAttempt[attempt], from, to)
		aligned = append(aligned, moved...)
		if shift < 0 {
			shift = -shift
		}
		largest = max(largest, shift)
	}
	return aligned, largest
}

func cancelRequested(events []controllerwire.LogRecord) bool {
	for _, event := range events {
		if event.Event == "job_cancel_requested" {
			return true
		}
	}
	return false
}

func failingSteps(steps []controllerwire.StepDetail, attempt int64) []string {
	var failed []string
	for _, step := range steps {
		if step.Status == "failed" && step.Attempt == attempt && !contains(failed, step.ID) {
			failed = append(failed, step.ID)
		}
	}
	if len(failed) == 0 {
		return []string{""}
	}
	return failed
}

func jobTimeline(events []controllerwire.LogRecord, steps []controllerwire.StepDetail, runEnd time.Time) ([]timingSpan, time.Duration) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].LineNo < events[j].LineNo })
	var changes []change
	var end time.Time
	for _, event := range events {
		at := *event.Time
		switch event.Event {
		case "job_ready", "job_requeued", "job_revoked", "job_approval_ready", "job_expansion_ready":
			changes = append(changes, change{at, phaseQueue})
		case "job_approval_pending":
			changes = append(changes, change{at, phaseApproval})
		case "job_claimed":
			changes = append(changes, change{at, phaseAdmit})
		case "job_started", "job_retrying":
			changes = append(changes, change{at, phaseSetup})
		case "job_succeeded", "job_failed", "job_cancelled", "job_skipped", "job_superseded", "job_memoized":
			if len(changes) > 0 {
				end = at
			}
		}
		if !end.IsZero() {
			break
		}
	}
	if len(changes) == 0 {
		return nil, 0
	}
	if end.IsZero() || end.After(runEnd) {
		end = runEnd
	}
	steps, shift := alignAttempts(steps, changes, end)
	type interval struct{ from, to time.Time }
	var ran []interval
	cuts := []time.Time{end}
	for _, c := range changes {
		cuts = append(cuts, c.at)
	}
	for _, step := range steps {
		if step.StartedAt == nil {
			continue
		}
		to := end
		if step.FinishedAt != nil && step.FinishedAt.Before(end) {
			to = *step.FinishedAt
		}
		ran = append(ran, interval{*step.StartedAt, to})
		cuts = append(cuts, *step.StartedAt, to)
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].Before(cuts[j]) })
	var spans []timingSpan
	for i := 0; i+1 < len(cuts); i++ {
		from, to := cuts[i], cuts[i+1]
		if !from.Before(to) || from.Before(changes[0].at) || !to.After(changes[0].at) || to.After(end) {
			continue
		}
		phase, since := phaseIdle, time.Time{}
		for _, c := range changes {
			if c.at.After(from) {
				break
			}
			phase, since = c.phase, c.at
		}
		if phase == phaseSetup {
			for _, r := range ran {
				if !r.from.After(from) && r.to.After(from) {
					phase = phaseRunning
					break
				}
				if !r.from.Before(since) && !r.from.After(from) {
					phase = phaseGap
				}
			}
		}
		spans = appendSpan(spans, timingSpan{from, to, phase})
	}
	return spans, shift
}

func overlay(spans []timingSpan, start, end time.Time) []timingSpan {
	cuts := []time.Time{start, end}
	for _, span := range spans {
		for _, at := range []time.Time{span.from, span.to} {
			if at.After(start) && at.Before(end) {
				cuts = append(cuts, at)
			}
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].Before(cuts[j]) })
	var out []timingSpan
	for i := 0; i+1 < len(cuts); i++ {
		from, to := cuts[i], cuts[i+1]
		if !from.Before(to) {
			continue
		}
		phase := phaseIdle
		for _, span := range spans {
			if !span.from.After(from) && span.to.After(from) && span.phase > phase {
				phase = span.phase
			}
		}
		out = appendSpan(out, timingSpan{from, to, phase})
	}
	return out
}

func appendSpan(spans []timingSpan, span timingSpan) []timingSpan {
	if n := len(spans); n > 0 && spans[n-1].phase == span.phase && spans[n-1].to.Equal(span.from) {
		spans[n-1].to = span.to
		return spans
	}
	return append(spans, span)
}

func spread(values []time.Duration) percentiles {
	if len(values) == 0 {
		return percentiles{}
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := func(q float64) time.Duration { return sorted[max(0, int(math.Ceil(float64(len(sorted))*q))-1)] }
	return percentiles{P50: rank(0.5), P90: rank(0.9), Max: sorted[len(sorted)-1]}
}

func selected(filter []string, job string) bool {
	return len(filter) == 0 || contains(filter, job)
}

func summarizeTiming(pipeline string, jobFilter []string, timings []runTiming) (timingSummaryRecord, []timingJobRecord, []timingFailureRecord) {
	summary := timingSummaryRecord{Kind: "summary", Pipeline: pipeline, Runs: len(timings)}
	var walls []time.Duration
	var phases [phaseCount][]time.Duration
	commits := map[string]int{}
	type jobSamples struct {
		runs, failed int
		retries      int64
		wait, total  []time.Duration
		phases       [phaseCount][]time.Duration
	}
	jobs := map[string]*jobSamples{}
	failures := map[timingFailureKey]*timingFailureRecord{}
	for _, run := range timings {
		switch run.Status {
		case "success":
			summary.Success++
		case "failed":
			summary.Failed++
		case "cancelled":
			summary.Cancelled++
		}
		if run.Rerun {
			summary.Reruns++
		}
		if run.SHA != "" {
			commits[run.SHA]++
		}
		walls = append(walls, run.Wall)
		for phase, d := range run.Phases {
			phases[phase] = append(phases[phase], d)
		}
		for id, job := range run.Jobs {
			if !selected(jobFilter, id) {
				continue
			}
			samples := jobs[id]
			if samples == nil {
				samples = &jobSamples{}
				jobs[id] = samples
			}
			samples.runs++
			if job.Outcome == "failed" {
				samples.failed++
			}
			samples.retries += max(0, job.Attempts-1)
			samples.wait = append(samples.wait, job.Phases[phaseQueue]+job.Phases[phaseAdmit]+job.Phases[phaseApproval])
			samples.total = append(samples.total, job.Total)
			for phase, d := range job.Phases {
				samples.phases[phase] = append(samples.phases[phase], d)
			}
		}
		for _, key := range run.Failures {
			if !selected(jobFilter, key.Job) {
				continue
			}
			record := failures[key]
			if record == nil {
				record = &timingFailureRecord{Kind: "failure", Job: key.Job, Step: key.Step, Outcome: key.Outcome, Reason: key.Reason}
				failures[key] = record
			}
			record.Count++
			record.Runs = append(record.Runs, run.Number)
		}
	}
	for _, n := range commits {
		summary.RepeatedCommits += n - 1
	}
	summary.Wall = spread(walls)
	summary.Queue, summary.Admit, summary.Approval = spread(phases[phaseQueue]), spread(phases[phaseAdmit]), spread(phases[phaseApproval])
	summary.Setup, summary.Running = spread(phases[phaseSetup]), spread(phases[phaseRunning])
	summary.Gap, summary.Idle = spread(phases[phaseGap]), spread(phases[phaseIdle])
	jobRecords := make([]timingJobRecord, 0, len(jobs))
	for id, s := range jobs {
		jobRecords = append(jobRecords, timingJobRecord{
			Kind: "job", Job: id, Runs: s.runs, Failed: s.failed, Retries: s.retries,
			Wait: spread(s.wait), Queue: spread(s.phases[phaseQueue]), Admit: spread(s.phases[phaseAdmit]), Approval: spread(s.phases[phaseApproval]),
			Setup: spread(s.phases[phaseSetup]), Running: spread(s.phases[phaseRunning]), Gap: spread(s.phases[phaseGap]), Total: spread(s.total),
		})
	}
	sort.Slice(jobRecords, func(i, j int) bool {
		if jobRecords[i].Total.P50 != jobRecords[j].Total.P50 {
			return jobRecords[i].Total.P50 > jobRecords[j].Total.P50
		}
		return jobRecords[i].Job < jobRecords[j].Job
	})
	failureRecords := make([]timingFailureRecord, 0, len(failures))
	for _, record := range failures {
		failureRecords = append(failureRecords, *record)
	}
	sort.Slice(failureRecords, func(i, j int) bool {
		a, b := failureRecords[i], failureRecords[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Job+a.Step+a.Reason < b.Job+b.Step+b.Reason
	})
	return summary, jobRecords, failureRecords
}

func writeRunsTiming(out io.Writer, pipeline string, jobFilter []string, rows int, timings []runTiming, format string) error {
	summary, jobs, failures := summarizeTiming(pipeline, jobFilter, timings)
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		for _, run := range timings {
			p := run.Phases
			if err := enc.Encode(timingRunRecord{
				Kind: "run", Number: run.Number, ID: run.ID, Status: run.Status, Rerun: run.Rerun, WallNS: run.Wall,
				QueueNS: p[phaseQueue], AdmitNS: p[phaseAdmit], Approval: p[phaseApproval], SetupNS: p[phaseSetup], RunningNS: p[phaseRunning], GapNS: p[phaseGap], IdleNS: p[phaseIdle], ShiftNS: run.StepShift,
			}); err != nil {
				return err
			}
		}
		for _, job := range jobs[:min(len(jobs), rows)] {
			if err := enc.Encode(job); err != nil {
				return err
			}
		}
		for _, failure := range failures[:min(len(failures), rows)] {
			if err := enc.Encode(failure); err != nil {
				return err
			}
		}
		if err := enc.Encode(summary); err != nil {
			return err
		}
		return enc.Encode(timingPage{
			Kind: "page", Jobs: len(jobs), JobsReturned: min(len(jobs), rows),
			FailureGroups: len(failures), FailureGroupsReturned: min(len(failures), rows),
			Truncated: len(jobs) > rows || len(failures) > rows,
		})
	case "plain":
		for _, job := range jobs {
			if _, err := fmt.Fprintln(out, opsview.DisplayText(job.Job)); err != nil {
				return err
			}
		}
		return nil
	}
	fmt.Fprintf(out, "%s: %d finished runs (%d success, %d failed, %d cancelled; %d reruns, %d repeated commits)\n\n",
		opsview.DisplayText(pipeline), summary.Runs, summary.Success, summary.Failed, summary.Cancelled, summary.Reruns, summary.RepeatedCommits)
	if summary.Runs == 0 {
		return nil
	}
	pair := func(p percentiles) string { return fmtDur(p.P50) + " / " + fmtDur(p.P90) }
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN WALL\tP50 / P90\tMAX")
	for _, row := range []struct {
		name string
		p    percentiles
	}{{"wall", summary.Wall}, {"queue", summary.Queue}, {"admit", summary.Admit}, {"approval", summary.Approval}, {"setup", summary.Setup}, {"running", summary.Running}, {"gap", summary.Gap}, {"idle", summary.Idle}} {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", row.name, pair(row.p), fmtDur(row.p.Max))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	var shifted time.Duration
	for _, run := range timings {
		shifted = max(shifted, run.StepShift)
	}
	if shifted > 0 {
		fmt.Fprintf(out, "\nStep times use the runner's clock; some jobs' steps were moved up to %s to fit their job window.\n", fmtDur(shifted))
	}
	fmt.Fprintln(out, "\nEach run's wall splits into the most advanced phase any job was in; gap is a job between or after its steps, idle is no job active.")
	fmt.Fprintln(out)
	fmt.Fprintln(tw, "JOB\tRUNS\tFAIL\tRETRY\tWAIT\tSETUP\tRUNNING\tGAP\tTOTAL")
	for _, job := range jobs[:min(len(jobs), rows)] {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n", opsview.DisplayText(job.Job), job.Runs, job.Failed, job.Retries, pair(job.Wait), pair(job.Setup), pair(job.Running), pair(job.Gap), pair(job.Total))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(jobs) > rows {
		fmt.Fprintf(out, "%d more jobs: raise --top or select them with --job.\n", len(jobs)-rows)
	}
	fmt.Fprintln(out, "\nJob columns are P50 / P90. Wait is queue (ready to claimed), admit (claimed to started: admission and source checkout) and approval.")
	type longWait struct {
		number                 int64
		job                    string
		queue, admit, approval time.Duration
	}
	var waits []longWait
	for _, run := range timings {
		for id, job := range run.Jobs {
			if selected(jobFilter, id) {
				waits = append(waits, longWait{run.Number, id, job.Phases[phaseQueue], job.Phases[phaseAdmit], job.Phases[phaseApproval]})
			}
		}
	}
	total := func(w longWait) time.Duration { return w.queue + w.admit + w.approval }
	sort.Slice(waits, func(i, j int) bool { return total(waits[i]) > total(waits[j]) })
	if len(waits) > 5 {
		waits = waits[:5]
	}
	fmt.Fprintln(out, "\nLongest waits:")
	fmt.Fprintln(tw, "RUN\tJOB\tWAIT\tQUEUE\tADMIT\tAPPROVAL")
	for _, w := range waits {
		fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\t%s\t%s\n", w.number, opsview.DisplayText(w.job), fmtDur(total(w)), fmtDur(w.queue), fmtDur(w.admit), fmtDur(w.approval))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(failures) == 0 {
		return nil
	}
	fmt.Fprintln(out, "\nFailed jobs, grouped by job, failing step and reason:")
	fmt.Fprintln(tw, "COUNT\tJOB\tSTEP\tOUTCOME\tREASON\tRUNS")
	for _, f := range failures[:min(len(failures), rows)] {
		numbers := make([]string, 0, min(len(f.Runs), 5))
		for _, n := range f.Runs[:min(len(f.Runs), 5)] {
			numbers = append(numbers, "#"+strconv.FormatInt(n, 10))
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", f.Count, opsview.DisplayText(f.Job), opsview.DisplayText(dashIfEmpty(f.Step)), opsview.DisplayText(f.Outcome), opsview.DisplayText(dashIfEmpty(f.Reason)), strings.Join(numbers, " "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(failures) > rows {
		fmt.Fprintf(out, "%d more failure groups: raise --top or select their jobs with --job.\n", len(failures)-rows)
	}
	return nil
}
