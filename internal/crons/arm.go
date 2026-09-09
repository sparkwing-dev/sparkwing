package crons

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ArmReport says what arming one repository changed. Schedules holds every row
// the repository still declares, created or refreshed; Withdrawals names the
// display names of the rows it stopped declaring.
type ArmReport struct {
	Armed       int                  `json:"armed"`
	Refreshed   int                  `json:"refreshed"`
	Withdrawn   int                  `json:"withdrawn"`
	Schedules   []store.CronSchedule `json:"schedules,omitempty"`
	Withdrawals []string             `json:"withdrawals,omitempty"`
}

// Arm records every schedule repoRoot declares against this home and computes
// each one's next due instant. A row that already exists keeps its pause
// state, its cursor, and its history; only the declaration is republished. A
// row this repository no longer declares is marked undeclared rather than
// deleted, so its history stays readable.
//
// prove, when non-nil, runs once per declared pipeline before anything is
// written. The first failure aborts the whole repository with that reason, so
// a repository is never half armed.
func (s *Service) Arm(ctx context.Context, repoRoot string, prove func(repoRoot, pipeline string) error) (ArmReport, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return ArmReport{}, fmt.Errorf("resolve %s: %w", repoRoot, err)
	}
	declared, err := DeclaredSchedules(root)
	if err != nil {
		return ArmReport{}, err
	}
	if prove != nil {
		for _, d := range declared {
			if perr := prove(root, d.Pipeline); perr != nil {
				return ArmReport{}, fmt.Errorf("%s does not compile, so nothing in %s was armed: %w",
					d.Pipeline, root, perr)
			}
		}
	}

	now := s.now()
	var report ArmReport
	keep := make(map[string]bool, len(declared))
	for _, d := range declared {
		row, err := scheduleRow(d, s.ArmedBy, now)
		if err != nil {
			return report, fmt.Errorf("%s/%s: %w", filepath.Base(root), d.Pipeline, err)
		}
		keep[row.ID] = true
		stored, created, err := s.Store.ArmCronSchedule(ctx, row, now)
		if err != nil {
			return report, err
		}
		if created {
			report.Armed++
		} else {
			report.Refreshed++
		}
		report.Schedules = append(report.Schedules, stored)
	}

	existing, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return report, err
	}
	for _, sched := range existing {
		if sched.RepoPath != root || keep[sched.ID] || !sched.Declared {
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

// safety: the next due instant is computed here so a freshly armed schedule reports one before the first tick.
func scheduleRow(d Declared, armedBy string, now time.Time) (store.CronSchedule, error) {
	parsed, err := cronspec.Parse(d.Trigger.Cron)
	if err != nil {
		return store.CronSchedule{}, err
	}
	loc, err := d.Trigger.Location()
	if err != nil {
		return store.CronSchedule{}, err
	}
	catchUp, err := d.Trigger.CatchUpDuration()
	if err != nil {
		return store.CronSchedule{}, err
	}
	row := store.CronSchedule{
		ID:       ScheduleID(d.RepoPath, d.Pipeline),
		RepoPath: d.RepoPath,
		Pipeline: d.Pipeline,
		Cron:     d.Trigger.Cron,
		TZ:       d.Trigger.TZ,
		Overlap:  d.Trigger.OverlapPolicy(),
		CatchUp:  catchUp,
		ArmedAt:  now,
		ArmedBy:  armedBy,
	}
	if next := parsed.Next(now, loc); !next.IsZero() {
		row.NextDueAt = &next
	}
	return row, nil
}

// Disarm deletes every schedule of one repository checkout, and their
// histories, returning how many rows went.
func (s *Service) Disarm(ctx context.Context, repoRoot string) (int, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return 0, fmt.Errorf("resolve %s: %w", repoRoot, err)
	}
	return s.Store.DeleteCronSchedulesForRepo(ctx, root)
}

// RefreshReport says what one pass over the armed repositories changed.
// Updated counts the rows whose declaration actually moved, not the rows
// looked at. Errors carries one sentence per repository that could not be
// read; a repository that fails never blocks the others.
type RefreshReport struct {
	Repos     int      `json:"repos"`
	Updated   int      `json:"updated"`
	Withdrawn int      `json:"withdrawn"`
	Errors    []string `json:"errors,omitempty"`
}

// safety: the refresh runs every minute over every armed row, so an unchanged
// declaration must not write: an updated_at that moves each minute tells an
// operator nothing and costs a transaction per schedule.
func republished(stored, declared store.CronSchedule) bool {
	return stored.Declared &&
		stored.Cron == declared.Cron &&
		stored.TZ == declared.TZ &&
		stored.Overlap == declared.Overlap &&
		stored.CatchUp == declared.CatchUp
}

// Refresh re-reads every armed repository's sparkwing.yaml and republishes
// what it finds, without proof: a changed cadence, zone, overlap policy or
// catch-up window is stored, and a row the repository stopped declaring is
// marked undeclared. A checkout that cannot be read at all is reported in
// Errors and its rows are left alone, because a detached volume or a moved
// directory is not a decision to stop scheduling.
//
// Refresh republishes rows that already exist. A pipeline that starts
// declaring a schedule is armed by `sparkwing crons install`, because which
// host evaluates a schedule is a decision an operator makes on that host.
func (s *Service) Refresh(ctx context.Context) (RefreshReport, error) {
	stored, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return RefreshReport{}, err
	}
	byRepo := map[string][]store.CronSchedule{}
	var roots []string
	for _, sched := range stored {
		if _, seen := byRepo[sched.RepoPath]; !seen {
			roots = append(roots, sched.RepoPath)
		}
		byRepo[sched.RepoPath] = append(byRepo[sched.RepoPath], sched)
	}
	sort.Strings(roots)

	now := s.now()
	report := RefreshReport{Repos: len(roots)}
	for _, root := range roots {
		declared, derr := DeclaredSchedules(root)
		if derr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", root, derr))
			continue
		}
		byID := make(map[string]Declared, len(declared))
		for _, d := range declared {
			byID[ScheduleID(d.RepoPath, d.Pipeline)] = d
		}
		for _, sched := range byRepo[root] {
			d, still := byID[sched.ID]
			if !still {
				if !sched.Declared {
					continue
				}
				if err := s.Store.SetCronScheduleDeclared(ctx, sched.ID, false, now); err != nil {
					report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
					continue
				}
				report.Withdrawn++
				continue
			}
			row, rerr := scheduleRow(d, sched.ArmedBy, now)
			if rerr != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), rerr))
				continue
			}
			if republished(sched, row) {
				continue
			}
			if _, _, err := s.Store.ArmCronSchedule(ctx, row, now); err != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", DisplayName(sched), err))
				continue
			}
			report.Updated++
		}
	}
	return report, nil
}
