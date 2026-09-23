package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type githubCheckPhase int

const (
	githubCheckQueued githubCheckPhase = iota + 1
	githubCheckInProgress
	githubCheckCompleted
)

func githubCheckPhaseFor(runStatus string) githubCheckPhase {
	switch runStatus {
	case "pending":
		return githubCheckQueued
	case "running":
		return githubCheckInProgress
	default:
		return githubCheckCompleted
	}
}

// githubCheckConclusion maps a finished run's status onto GitHub's check
// run conclusions.
func githubCheckConclusion(runStatus string) string {
	switch runStatus {
	case "success":
		return "success"
	case "cancelled":
		return "cancelled"
	case "timed_out":
		return "timed_out"
	default:
		return "failure"
	}
}

// githubCheckUpdate is the state one run of an App trigger reports.
type githubCheckUpdate struct {
	installation int64
	owner        string
	repo         string
	sha          string
	pipeline     string
	runID        string
	runStatus    string
	team         store.Team
}

func (u githubCheckUpdate) phase() githubCheckPhase { return githubCheckPhaseFor(u.runStatus) }

type githubCheckJob struct {
	want     githubCheckUpdate
	logger   *slog.Logger
	inFlight bool
	queued   bool
}

const (
	githubCheckCapacity = 256
	// safety: the attempts and their waits bound how long one run's update
	// holds the queue while GitHub fails, at about four request timeouts.
	githubCheckAttempts  = 4
	githubCheckBackoff   = 250 * time.Millisecond
	githubStatusRunLimit = 4096
)

// githubCheckReporter reports the runs the App started as GitHub check runs.
// Updates for one run coalesce to the latest state and never move a check
// run backwards, and a write GitHub keeps refusing is logged and dropped, so
// no run waits on GitHub.
type githubCheckReporter struct {
	client       *githubapp.Client
	store        *store.Store
	dashboardURL string
	ctx          context.Context
	cancel       context.CancelFunc
	wake         chan struct{}
	done         chan struct{}

	mu        sync.Mutex
	accepting bool
	jobs      map[string]*githubCheckJob
	order     []string
	changed   chan struct{}
	// statusRuns are the runs that began on commit statuses because their
	// installation lacked the checks permission; they finish there too, so a
	// commit never keeps a pending status beside a check run.
	statusRuns map[string]bool
	// warned holds the installations whose missing permission was logged.
	warned map[int64]bool
}

func newGitHubCheckReporter(client *githubapp.Client, st *store.Store, dashboardURL string) *githubCheckReporter {
	ctx, cancel := context.WithCancel(context.Background())
	r := &githubCheckReporter{
		client: client, store: st, dashboardURL: dashboardURL,
		ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		accepting: true, jobs: map[string]*githubCheckJob{}, changed: make(chan struct{}),
		statusRuns: map[string]bool{}, warned: map[int64]bool{},
	}
	go r.run()
	return r
}

func (r *githubCheckReporter) enqueue(logger *slog.Logger, u githubCheckUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting {
		return
	}
	job := r.jobs[u.runID]
	if job == nil {
		if len(r.jobs) >= githubCheckCapacity {
			logger.Warn("github check run update dropped", "run_id", u.runID, "pipeline", u.pipeline,
				"run_status", u.runStatus, "reason", "capacity")
			return
		}
		job = &githubCheckJob{}
		r.jobs[u.runID] = job
	} else if u.phase() <= job.want.phase() {
		return
	}
	job.want, job.logger = u, logger
	if !job.inFlight && !job.queued {
		job.queued = true
		r.order = append(r.order, u.runID)
		r.signalLocked()
	}
}

func (r *githubCheckReporter) signalLocked() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *githubCheckReporter) run() {
	defer close(r.done)
	for {
		job, exit := r.take()
		if job != nil {
			phase := r.deliver(job.logger, job.want)
			r.finish(job.want.runID, phase)
			continue
		}
		if exit {
			return
		}
		select {
		case <-r.ctx.Done():
			return
		case <-r.wake:
		}
	}
}

func (r *githubCheckReporter) take() (*githubCheckJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return nil, true
	}
	if len(r.order) == 0 {
		return nil, !r.accepting
	}
	id := r.order[0]
	r.order = r.order[1:]
	job := r.jobs[id]
	job.queued, job.inFlight = false, true
	cp := *job
	return &cp, false
}

func (r *githubCheckReporter) finish(runID string, sent githubCheckPhase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[runID]
	job.inFlight = false
	switch {
	case job.want.phase() > sent:
		job.queued = true
		r.order = append(r.order, runID)
	default:
		delete(r.jobs, runID)
		if sent == githubCheckCompleted {
			delete(r.statusRuns, runID)
		}
	}
	r.signalLocked()
}

// idle waits until no update is queued or being sent.
func (r *githubCheckReporter) idle(ctx context.Context) error {
	for {
		r.mu.Lock()
		empty, changed := len(r.jobs) == 0, r.changed
		r.mu.Unlock()
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (r *githubCheckReporter) shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.accepting = false
	r.signalLocked()
	r.mu.Unlock()
	defer r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *githubCheckReporter) stop() {
	r.mu.Lock()
	r.accepting = false
	r.mu.Unlock()
	r.cancel()
}

// deliver writes u to GitHub and returns the phase to count as sent, which
// is u's own even when GitHub never took it, so a failing write is not
// retried past its attempts.
func (r *githubCheckReporter) deliver(logger *slog.Logger, u githubCheckUpdate) githubCheckPhase {
	r.mu.Lock()
	onStatuses := r.statusRuns[u.runID]
	r.mu.Unlock()
	// safety: a commit status has no running state; the pending one the run
	// already posted stands for it.
	if onStatuses && u.phase() == githubCheckInProgress {
		return u.phase()
	}
	if !onStatuses {
		err := r.retry(logger, u, "check run", func(ctx context.Context) error { return r.writeCheckRun(ctx, u) })
		if err == nil || !errors.Is(err, githubapp.ErrPermissionMissing) {
			if err != nil {
				logger.Warn("github check run update failed", "run_id", u.runID, "pipeline", u.pipeline,
					"run_status", u.runStatus, "err", err)
			}
			return u.phase()
		}
		r.fallBackToStatuses(logger, u)
	}
	if err := r.retry(logger, u, "commit status", func(ctx context.Context) error { return r.writeCommitStatus(ctx, u) }); err != nil {
		logger.Warn("github commit status update failed", "run_id", u.runID, "pipeline", u.pipeline,
			"run_status", u.runStatus, "err", err)
	}
	return u.phase()
}

func (r *githubCheckReporter) fallBackToStatuses(logger *slog.Logger, u githubCheckUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u.phase() != githubCheckCompleted && len(r.statusRuns) < githubStatusRunLimit {
		r.statusRuns[u.runID] = true
	}
	if !r.warned[u.installation] {
		r.warned[u.installation] = true
		logger.Warn("github app installation has not accepted the checks permission; its runs report commit statuses until it does",
			"installation_id", u.installation, "repo", u.owner+"/"+u.repo)
	}
}

func (r *githubCheckReporter) retry(logger *slog.Logger, u githubCheckUpdate, what string, write func(context.Context) error) error {
	wait := githubCheckBackoff
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(r.ctx, githubStatusTimeout)
		err := write(ctx)
		cancel()
		if err == nil || attempt == githubCheckAttempts || !githubRetryable(err) {
			return err
		}
		logger.Warn("github "+what+" write will be retried", "run_id", u.runID, "pipeline", u.pipeline,
			"attempt", attempt, "err", err)
		select {
		case <-r.ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// githubRetryable reports whether a write GitHub failed may land if sent
// again: GitHub answered 429 or a 5xx, or the request never got an answer.
func githubRetryable(err error) bool {
	var apiErr *githubapp.APIError
	switch {
	case errors.As(err, &apiErr):
		return apiErr.Temporary()
	case errors.Is(err, githubapp.ErrPermissionMissing), errors.Is(err, githubapp.ErrNotInstalled),
		errors.Is(err, githubapp.ErrRejected), errors.Is(err, errGitHubCheckRunUnrecorded),
		errors.Is(err, store.ErrNotFound), errors.Is(err, context.Canceled):
		return false
	}
	return true
}

func (r *githubCheckReporter) writeCheckRun(ctx context.Context, u githubCheckUpdate) error {
	tok, err := r.client.InstallationToken(ctx, u.installation, []string{u.repo}, map[string]string{"checks": "write"})
	if err != nil {
		return err
	}
	tenant, err := r.store.ForTeam(ctx, u.team)
	if err != nil {
		return err
	}
	id, err := tenant.GitHubCheckRun(ctx, u.runID)
	if err != nil {
		return err
	}
	check := r.checkRun(ctx, u)
	if id != 0 {
		return r.client.UpdateCheckRun(ctx, tok.Token, u.owner, u.repo, id, check)
	}
	id, err = r.client.CreateCheckRun(ctx, tok.Token, u.owner, u.repo, check)
	if err != nil {
		return err
	}
	if _, err := tenant.RecordGitHubCheckRun(context.WithoutCancel(ctx), u.runID, id); err != nil {
		return fmt.Errorf("%w: check run %d: %w", errGitHubCheckRunUnrecorded, id, err)
	}
	return nil
}

// safety: a check run GitHub created but the store did not record is not
// written again, since a second create would put a second check run beside it.
var errGitHubCheckRunUnrecorded = errors.New("the check run was created but not recorded")

func (r *githubCheckReporter) writeCommitStatus(ctx context.Context, u githubCheckUpdate) error {
	tok, err := r.client.InstallationToken(ctx, u.installation, []string{u.repo}, map[string]string{"statuses": "write"})
	if err != nil {
		return err
	}
	status := "pending"
	if u.phase() == githubCheckCompleted {
		status = u.runStatus
	}
	state, description := githubCommitState(status)
	return r.client.CreateCommitStatus(ctx, tok.Token, u.owner, u.repo, u.sha, githubCommitStatusRequest{
		State: state, TargetURL: githubRunTargetURL(r.dashboardURL, u.runID),
		Description: description, Context: "sparkwing/" + u.pipeline,
	})
}

func (r *githubCheckReporter) checkRun(ctx context.Context, u githubCheckUpdate) githubapp.CheckRun {
	link := githubRunTargetURL(r.dashboardURL, u.runID)
	check := githubapp.CheckRun{
		Name: "sparkwing/" + u.pipeline, HeadSHA: u.sha, DetailsURL: link, ExternalID: u.runID,
	}
	run, err := r.store.GetRun(ctx, u.runID)
	if err != nil {
		run = nil
	}
	switch u.phase() {
	case githubCheckQueued:
		check.Status = "queued"
		check.Output = &githubapp.CheckRunOutput{Title: "Queued", Summary: githubCheckLinkLine("Waiting for a runner.", link)}
	case githubCheckInProgress:
		check.Status = "in_progress"
		if run != nil && !run.StartedAt.IsZero() {
			started := run.StartedAt
			check.StartedAt = &started
		}
		check.Output = &githubapp.CheckRunOutput{Title: "Running", Summary: githubCheckLinkLine("Running.", link)}
	default:
		check.Status, check.Conclusion = "completed", githubCheckConclusion(u.runStatus)
		completed := time.Now().UTC()
		if run != nil && run.FinishedAt != nil {
			completed = *run.FinishedAt
		}
		check.CompletedAt = &completed
		var nodes []*store.Node
		if run != nil {
			if nodes, err = r.store.ListNodes(ctx, u.runID); err != nil {
				nodes = nil
			}
		}
		check.Output = &githubapp.CheckRunOutput{
			Title:   githubCheckTitle(check.Conclusion),
			Summary: githubCheckSummary(run, nodes, check.Conclusion, link),
		}
	}
	return check
}

func githubCheckTitle(conclusion string) string {
	switch conclusion {
	case "success":
		return "Passed"
	case "cancelled":
		return "Cancelled"
	case "timed_out":
		return "Timed out"
	default:
		return "Failed"
	}
}

func githubCheckLinkLine(text, link string) string {
	if link == "" {
		return text
	}
	return text + " [Open the run in Sparkwing](" + link + ")."
}

// githubCheckSummary reports aggregate run state on GitHub; details stay on
// the authenticated Sparkwing run page.
func githubCheckSummary(run *store.Run, nodes []*store.Node, conclusion, link string) string {
	var summary strings.Builder
	summary.WriteString("**" + githubCheckTitle(conclusion) + "**")
	if run != nil && run.FinishedAt != nil && !run.StartedAt.IsZero() {
		summary.WriteString(" in " + run.FinishedAt.Sub(run.StartedAt).Round(time.Second).String())
	}
	summary.WriteString(".")
	if link != "" {
		summary.WriteString(" [Open the run in Sparkwing](" + link + ").")
	}
	counts := map[string]int{}
	for _, n := range nodes {
		outcome := n.Outcome
		if outcome == "" {
			outcome = n.Status
		}
		switch outcome {
		case "success", "failed", "satisfied", "cached", "skipped", "cancelled", "skipped-concurrent", "superseded", "pending", "running":
		default:
			outcome = "other"
		}
		counts[outcome]++
	}
	if len(counts) > 0 {
		outcomes := make([]string, 0, len(counts))
		for outcome := range counts {
			outcomes = append(outcomes, outcome)
		}
		slices.Sort(outcomes)
		summary.WriteString("\n\nNodes: ")
		for i, outcome := range outcomes {
			if i > 0 {
				summary.WriteString(", ")
			}
			fmt.Fprintf(&summary, "%d %s", counts[outcome], outcome)
		}
		summary.WriteString(".")
	}
	return summary.String()
}
