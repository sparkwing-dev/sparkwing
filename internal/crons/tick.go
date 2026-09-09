package crons

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ErrTickRunning is returned when another tick already holds the lock. It is
// the ordinary answer on a busy host, not a failure: the tick that holds the
// lock is resolving the same minute.
var ErrTickRunning = errors.New("crons: another tick is already running")

// TickReport is what one tick did. Decisions carry one entry per schedule that
// resolved an instant, and Errors one sentence per schedule or repository that
// could not be evaluated.
type TickReport struct {
	At        time.Time  `json:"at"`
	Evaluated int        `json:"evaluated"`
	Fired     int        `json:"fired"`
	Skipped   int        `json:"skipped"`
	Missed    int        `json:"missed"`
	Failed    int        `json:"failed"`
	Decisions []Decision `json:"decisions,omitempty"`
	Errors    []string   `json:"errors,omitempty"`
}

// Decision is one schedule's resolved instant.
type Decision struct {
	Schedule store.CronSchedule `json:"schedule"`
	Due      time.Time          `json:"due"`
	Outcome  string             `json:"outcome"`
	RunID    string             `json:"run_id,omitempty"`
	Detail   string             `json:"detail,omitempty"`
	// Args are the arguments the launch was given, after this host's
	// override.
	Args map[string]string `json:"args,omitempty"`
}

// Tick is the per-minute entry point the OS timer calls.
//
// It takes an exclusive lock on [Service.LockPath] -- unless that is empty,
// which says the caller holds exclusion of its own -- republishes what the
// armed repositories declare, evaluates every declared unpaused schedule
// against its cursor, and resolves each due instant: a launch, a skip when the schedule's
// previous run is still active and its policy is skip, or a miss when the
// instant fell outside the catch-up window. Every outcome moves the cursor, so
// no instant is ever considered twice. Paused and undeclared rows fire
// nothing; their cursor and next due instant still move, so neither is
// replayed when the schedule comes back.
//
// One schedule's failure is recorded against that schedule and never aborts
// the tick. dryRun evaluates and reports without launching anything or writing
// anything.
func (s *Service) Tick(ctx context.Context, dryRun bool) (TickReport, error) {
	// safety: an empty LockPath is a caller that already holds exclusion of its
	// own, such as the controller's store lease, not a caller that forgot one.
	if s.LockPath != "" {
		unlock, err := lockTick(s.LockPath)
		if err != nil {
			return TickReport{}, err
		}
		defer unlock()
	}

	now := s.now()
	report := TickReport{At: now}

	if !dryRun {
		refresh, rerr := s.Refresh(ctx)
		if rerr != nil {
			report.Errors = append(report.Errors, rerr.Error())
		}
		report.Errors = append(report.Errors, refresh.Errors...)
	}

	stored, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return report, err
	}
	for _, sched := range stored {
		s.tickOne(ctx, sched, now, dryRun, &report)
	}

	if dryRun {
		return report, nil
	}
	tick := store.CronTick{At: now, Host: s.Host, Version: s.Version, Error: summarize(report.Errors)}
	if err := s.Store.RecordCronTick(ctx, tick); err != nil {
		return report, err
	}
	return report, nil
}

func summarize(errs []string) string {
	switch len(errs) {
	case 0:
		return ""
	case 1:
		return errs[0]
	}
	return fmt.Sprintf("%s (and %d more)", errs[0], len(errs)-1)
}

func (s *Service) tickOne(ctx context.Context, sched store.CronSchedule, now time.Time, dryRun bool, report *TickReport) {
	if !s.evaluates(sched) {
		return
	}
	eval, err := prepare(sched)
	if err != nil {
		s.recordUnevaluable(ctx, sched, now, dryRun, report, err)
		return
	}
	if !sched.Declared || sched.Paused {
		if !dryRun {
			s.advanceIdleCursor(ctx, sched, eval, now, report)
		}
		return
	}

	report.Evaluated++
	decision := cronspec.Decide(eval.schedule, eval.loc, sched.CursorAt, now, eval.catchUp)
	if decision.Due.IsZero() {
		if !dryRun {
			if err := s.Store.SetCronScheduleNextDue(ctx, sched.ID, eval.nextAfter(now), now); err != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
			}
		}
		return
	}

	if decision.Missed > 0 {
		s.recordMissed(ctx, sched, eval, decision, now, dryRun, report)
		if !decision.Fire {
			return
		}
	}
	s.resolveDue(ctx, sched, eval, decision.Due, now, dryRun, report)
}

// safety: a host's override can break a cadence the config validated, and such
// a schedule has no due instant to resolve, so the failure is recorded once per
// episode rather than on every minute tick.
func (s *Service) recordUnevaluable(
	ctx context.Context, sched store.CronSchedule, now time.Time, dryRun bool,
	report *TickReport, cause error,
) {
	detail := cause.Error()
	if sched.Override != nil {
		detail += "; `sparkwing crons reset " + DisplayName(sched) + "` drops this host's override"
	}
	report.Errors = append(report.Errors, fmt.Sprintf("%s: %s", DisplayName(sched), detail))
	if !sched.Declared || sched.Paused {
		return
	}
	report.Failed++
	report.Decisions = append(report.Decisions, Decision{
		Schedule: sched, Due: now, Outcome: store.CronOutcomeFailed, Detail: detail,
	})
	if dryRun || sched.LastOutcome == store.CronOutcomeFailed {
		return
	}
	fire := &store.CronFire{DueAt: now, DecidedAt: now, Outcome: store.CronOutcomeFailed, Detail: detail}
	if err := s.Store.ResolveCronDue(ctx, sched.ID, sched.CursorAt, sched.NextDueAt, fire, now); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
	}
}

// safety: a cursor left where the pause found it reads as a backlog when the
// schedule comes back, and a week of missed instants launches from it.
func (s *Service) advanceIdleCursor(ctx context.Context, sched store.CronSchedule, eval evaluable,
	now time.Time, report *TickReport,
) {
	next := eval.nextAfter(now)
	decision := cronspec.Decide(eval.schedule, eval.loc, sched.CursorAt, now, eval.catchUp)
	var err error
	if decision.Due.IsZero() {
		err = s.Store.SetCronScheduleNextDue(ctx, sched.ID, next, now)
	} else {
		err = s.Store.ResolveCronDue(ctx, sched.ID, decision.Due, next, nil, now)
	}
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
	}
}

// safety: a backlog collapses to one row; a week of a slept-through minutely schedule is not ten thousand fires.
func (s *Service) recordMissed(ctx context.Context, sched store.CronSchedule, eval evaluable,
	decision cronspec.Decision, now time.Time, dryRun bool, report *TickReport,
) {
	last := decision.Due
	if decision.Fire {
		last = lastInstantBefore(eval, sched.CursorAt, decision.Due)
		if last.IsZero() {
			return
		}
	}
	detail := fmt.Sprintf("%d due instants missed", decision.Missed)
	if decision.Missed >= cronspec.MaxMissed {
		detail = fmt.Sprintf("%d or more due instants missed", cronspec.MaxMissed)
	}
	report.Missed++
	report.Decisions = append(report.Decisions, Decision{
		Schedule: sched, Due: last, Outcome: store.CronOutcomeMissed, Detail: detail,
	})
	if dryRun {
		return
	}
	fire := &store.CronFire{DueAt: last, DecidedAt: now, Outcome: store.CronOutcomeMissed, Detail: detail}
	next := eval.nextAfter(last)
	if err := s.Store.ResolveCronDue(ctx, sched.ID, last, next, fire, now); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
	}
}

func lastInstantBefore(eval evaluable, cursor, due time.Time) time.Time {
	var last time.Time
	at := cursor
	for i := 0; i < cronspec.MaxMissed; i++ {
		next := eval.schedule.Next(at, eval.loc)
		if next.IsZero() || !next.Before(due) {
			return last
		}
		last, at = next, next
	}
	return last
}

func (s *Service) resolveDue(ctx context.Context, sched store.CronSchedule, eval evaluable,
	due, now time.Time, dryRun bool, report *TickReport,
) {
	outcome, runID, detail := store.CronOutcomeFired, "", ""

	if err := s.pinnedBinaryReady(sched); err != nil {
		outcome, detail = store.CronOutcomeFailed, err.Error()
	}

	if outcome == store.CronOutcomeFired && sched.Effective().Overlap == store.CronOverlapSkip && sched.LastRunID != "" {
		active, err := s.Launcher.Active(ctx, sched.LastRunID, eval.catchUp)
		switch {
		case err != nil:
			report.Errors = append(report.Errors,
				fmt.Sprintf("%s: check run %s: %v", DisplayName(sched), sched.LastRunID, err))
		case active:
			outcome = store.CronOutcomeSkippedOverlap
			detail = fmt.Sprintf("run %s is still going and overlap is %q", sched.LastRunID, store.CronOverlapSkip)
		}
	}

	if outcome == store.CronOutcomeFired && !dryRun {
		id, err := s.Launcher.Launch(ctx, sched, due)
		if err != nil {
			outcome, detail = store.CronOutcomeFailed, err.Error()
		} else {
			runID = id
		}
	}

	switch outcome {
	case store.CronOutcomeFired:
		report.Fired++
	case store.CronOutcomeSkippedOverlap:
		report.Skipped++
	case store.CronOutcomeFailed:
		report.Failed++
	}
	report.Decisions = append(report.Decisions, Decision{
		Schedule: sched, Due: due, Outcome: outcome, RunID: runID, Detail: detail, Args: eval.args,
	})
	if dryRun {
		return
	}
	fire := &store.CronFire{
		DueAt: due, DecidedAt: now, Outcome: outcome, RunID: runID, Detail: detail, Args: eval.args,
	}
	if err := s.Store.ResolveCronDue(ctx, sched.ID, due, eval.nextAfter(due), fire, now); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
	}
}

// Summary is the one line a quiet tick prints.
func (r TickReport) Summary() string {
	parts := []string{
		fmt.Sprintf("%d evaluated", r.Evaluated),
		fmt.Sprintf("%d fired", r.Fired),
		fmt.Sprintf("%d skipped", r.Skipped),
	}
	if r.Missed > 0 {
		parts = append(parts, fmt.Sprintf("%d missed", r.Missed))
	}
	if r.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", r.Failed))
	}
	return strings.Join(parts, ", ")
}
