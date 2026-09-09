package crons

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ArmPush is one repository's controller schedules as an operator pushed them.
// RepoURL is the validated clone URL every fire clones, Branch the branch the
// entries were read from, and SHA the commit each fire is pinned to; an empty
// SHA follows the branch tip. Entries carries one [Declared] per schedule, with
// RepoPath left to the arm.
type ArmPush struct {
	RepoURL string
	Branch  string
	SHA     string
	Entries []Declared
}

// ArmPushed records a repository's controller schedules against this store and
// computes each one's next due instant, exactly as [Service.Arm] does for a
// checkout: an existing row keeps its pause state, cursor, history and
// override, and a row this push no longer carries is marked undeclared rather
// than deleted. The rows carry the clone URL as their repository path, so
// nothing on this machine is read to evaluate them.
func (s *Service) ArmPushed(ctx context.Context, push ArmPush) (ArmReport, error) {
	if push.RepoURL == "" {
		return ArmReport{}, errors.New("crons: a repository URL is required to arm pushed schedules")
	}
	if !PushedRepo(push.RepoURL) {
		return ArmReport{}, fmt.Errorf("crons: %q is not a clone URL, so it cannot carry pushed schedules", push.RepoURL)
	}
	now := s.now()
	var report ArmReport
	keep := make(map[string]bool, len(push.Entries))
	for _, entry := range push.Entries {
		entry.RepoPath = push.RepoURL
		if entry.Name == "" {
			entry.Name = store.CronScheduleDefaultName
		}
		entry.Trigger.Where = store.CronWhereController
		row, rerr := scheduleRow(entry, store.CronLock{Ref: push.SHA}, s.ArmedBy, now)
		if rerr != nil {
			return report, fmt.Errorf("%s: %w", entry.DisplayName(), rerr)
		}
		row.GitBranch = push.Branch
		stored, created, aerr := s.Store.ArmCronSchedule(ctx, row, now)
		if aerr != nil {
			return report, aerr
		}
		if created {
			report.Armed++
		} else {
			report.Refreshed++
		}
		if rebased, berr := s.rebaseOverride(ctx, stored, now); berr != nil {
			return report, berr
		} else if rebased != nil {
			stored = *rebased
		}
		keep[stored.ID] = true
		report.Schedules = append(report.Schedules, stored)
	}

	existing, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return report, err
	}
	for _, sched := range existing {
		if sched.RepoPath != push.RepoURL || keep[sched.ID] || !sched.Declared {
			continue
		}
		if err := s.Store.SetCronScheduleDeclared(ctx, sched.ID, false, now); err != nil {
			return report, err
		}
		report.Withdrawn++
		report.Withdrawals = append(report.Withdrawals, DisplayName(sched))
	}
	sort.Strings(report.Withdrawals)
	return report, nil
}

// DisarmRepoURL deletes every schedule pushed for one repository URL, and their
// histories, returning how many rows went.
func (s *Service) DisarmRepoURL(ctx context.Context, repoURL string) (int, error) {
	if repoURL == "" {
		return 0, errors.New("crons: a repository URL is required to disarm pushed schedules")
	}
	return s.Store.DeleteCronSchedulesForRepo(ctx, repoURL)
}

// ControllerHealth is [Service.Health] for a process that evaluates schedules
// from a loop of its own rather than an OS timer: the counts and the last tick
// come from the store, and the timer state names the loop.
func (s *Service) ControllerHealth(ctx context.Context) (Health, error) {
	rows, err := s.List(ctx)
	if err != nil {
		return Health{}, err
	}
	health := Health{Schedules: len(rows)}
	for _, r := range rows {
		health.count(r)
	}
	tick, err := s.Store.GetCronTick(ctx)
	if err != nil {
		return health, err
	}
	health.LastTick = tick
	health.Timer.Installed = true
	health.Timer.Enabled = true
	health.Timer.Detail = ControllerTimerDetail
	now := s.now()
	health.TickStale = !tick.At.IsZero() && now.Sub(tick.At) > TickStaleAfter
	health.Detail = health.describeController(now)
	health.Remedy = health.remedy()
	return health, nil
}

// ControllerTimerDetail is what a controller's health reports in place of an OS
// timer, because the tick comes from a loop inside the server.
const ControllerTimerDetail = "controller loop"

func (h Health) describeController(now time.Time) string {
	switch {
	case h.Armed == 0 && h.Schedules == 0:
		return "no schedule is pushed here; `sparkwing crons install --profile <name>` pushes a repository's"
	case h.Armed == 0:
		return fmt.Sprintf("no schedule is armed here (%d paused, %d undeclared)", h.Paused, h.Undeclared)
	case h.LastTick.At.IsZero():
		return fmt.Sprintf("%d schedule(s) are armed and the controller loop has not ticked yet", h.Armed)
	case h.TickStale:
		return fmt.Sprintf("the last tick was %s ago, so the controller loop is not landing; the controller log says why",
			roundDuration(now.Sub(h.LastTick.At)))
	case h.LastTick.Error != "":
		return "the last tick reported: " + h.LastTick.Error
	}
	return fmt.Sprintf("%d schedule(s) armed and the controller loop ticks every minute", h.Armed)
}
