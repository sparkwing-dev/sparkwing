package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

type scheduleAdmission struct {
	schedule CronSchedule
	consent  string
	caller   *capability.Authorized
}

func (in scheduleAdmission) authority() capability.AuthorityRef {
	return capability.AuthorityRef{Kind: "schedule", ID: in.schedule.ID}
}

// AdmitScheduledRun admits a persisted schedule through the shared sealed intake.
func (t *Tenant) AdmitScheduledRun(ctx context.Context, schedule CronSchedule, due time.Time) (AdmissionResult, error) {
	if due.IsZero() {
		return AdmissionResult{}, ErrInvalidInput
	}
	return t.admitSchedule(ctx, schedule, schedule.ID+"@"+due.UTC().Format(time.RFC3339Nano), due, nil)
}

func scheduleDisplay(schedule CronSchedule) (string, error) {
	if schedule.GitHubRepositoryID == 0 {
		return schedule.RepoPath, nil
	}
	remote, err := sourceurl.ValidateCloneURL(schedule.RepoPath)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(remote)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return "", fmt.Errorf("%w: App schedule repository is not a GitHub URL", ErrInvalidInput)
	}
	repo, ok := ParseGitHubRepo(strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git"))
	if !ok {
		return "", fmt.Errorf("%w: App schedule repository must name owner/name", ErrInvalidInput)
	}
	return repo.Slug(), nil
}

func (t *Tenant) admitSchedule(ctx context.Context, schedule CronSchedule, key string, due time.Time, caller *capability.Authorized) (AdmissionResult, error) {
	if schedule.Team != t.team || schedule.Where != CronWhereController && schedule.Where != CronWhereLocal {
		return AdmissionResult{}, ErrInvalidInput
	}
	in := scheduleAdmission{schedule: schedule, caller: caller}
	var saved scheduleConsent
	if err := t.s.queryRow(ctx, `SELECT consent_json FROM cron_schedules WHERE team=? AND id=?`, t.team, schedule.ID).Scan(&in.consent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdmissionResult{}, notFound("schedule", schedule.ID)
		}
		return AdmissionResult{}, err
	}
	display, err := scheduleDisplay(schedule)
	if err != nil {
		return AdmissionResult{}, err
	}
	input := runspec.Request{
		Version: 1, Team: string(t.team), Pipeline: schedule.Pipeline, Mode: runspec.IntakeExecute, Origin: runspec.OriginSchedule,
		RequestedArgs: schedule.Effective().Args, Git: runspec.Git{Repository: runspec.Repository{Key: schedule.RepositoryKey, Display: display}, Ref: schedule.GitBranch, SHA: schedule.LockedRef},
	}
	if in.consent != "" {
		if json.Unmarshal([]byte(in.consent), &saved) != nil || saved.Version != 1 {
			return AdmissionResult{}, bindingRefusal()
		}
		input = saved.Request
		input.Dispatch = ""
	} else {
		remote, err := sourceurl.ValidateCloneURL(schedule.RepoPath)
		if err != nil {
			return AdmissionResult{}, err
		}
		input.Source.Location = runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"}
		if schedule.LockedRef != "" {
			git, code, err := source.ResolveRevisions(ctx, input.Git, input.Source, "", nil)
			if err != nil {
				return AdmissionResult{}, err
			}
			input.Git, input.Source = git, code
		}
	}
	input.Team, input.Pipeline, input.Origin = string(t.team), schedule.Pipeline, runspec.OriginSchedule
	input.RequestedArgs = schedule.Effective().Args
	if in.consent != "" {
		var replay AdmissionResult
		err := t.s.inTx(ctx, func(tx *storeTx) error {
			tx.scheduleAdmission = &in
			if !schedule.Declared || caller == nil && schedule.Paused {
				return bindingRefusal()
			}
			p, err := in.resolve(ctx, tx)
			if err != nil {
				return err
			}
			scope, err := intakeScope(p, schedule.Pipeline)
			if err != nil {
				return err
			}
			var id string
			err = tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE team=? AND idempotency_scope=? AND idempotency_key=?`, t.team, scope, key).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			stored, err := admissionResultTx(ctx, tx, t.team, id)
			if err != nil {
				return err
			}
			if schedule.LockedRef == "" {
				input.Git.SHA = stored.Request.Request.Git.SHA
			}
			input.Source.Ref = stored.Request.Request.Source.Ref
			digest, err := tx.store.planServices().CanonicalHash(struct {
				Request  runspec.Request
				Delivery string
			}{Request: input})
			if err != nil {
				return err
			}
			replay, _, err = replayAdmissionTx(ctx, tx, t.team, scope, key, string(digest))
			return err
		})
		if err != nil || replay.Replayed {
			return replay, err
		}
	}
	// safety: only the controller's fire-time lookup through the App names the default
	// branch; an armed request cannot carry one in.
	input.Git.DefaultBranch = runspec.Optional[string]{}
	if policy, ok := t.s.intakePolicy.(ScheduleSourcePolicy); ok && (in.consent != "" || schedule.LockedRef == "") {
		var err error
		input, err = policy.ResolveScheduleSource(ctx, schedule, input)
		if err != nil {
			return AdmissionResult{}, err
		}
	}
	input.Team, input.Pipeline, input.Origin = string(t.team), schedule.Pipeline, runspec.OriginSchedule
	input.RequestedArgs = schedule.Effective().Args
	request := IntakeRequest{Authority: in.authority(), IdempotencyKey: key, Request: input}
	var result AdmissionResult
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		tx.scheduleAdmission = &in
		if !schedule.Declared || caller == nil && schedule.Paused {
			return bindingRefusal()
		}
		var err error
		result, err = admitRun(ctx, tx, t.team, request, tx.time)
		if err != nil {
			return err
		}
		if _, err = in.resolve(ctx, tx); err != nil {
			return err
		}
		if result.Replayed {
			return nil
		}
		cursor, next := schedule.CursorAt, schedule.NextDueAt
		fire := &CronFire{DueAt: due, DecidedAt: tx.time, Outcome: CronOutcomeFired, RunID: result.RunID, Args: input.RequestedArgs}
		if due.IsZero() {
			fire.DueAt, fire.Detail = tx.time, "run now"
		} else {
			if in.consent != "" {
				expected, err := scheduleNext(schedule, due.Add(-time.Nanosecond), 0)
				if err != nil {
					return err
				}
				if due.After(tx.time) || !due.After(schedule.CursorAt) || expected == nil || !expected.Equal(due) {
					return authorityError(apierr.Conflict, "schedule instant is not due")
				}
			}
			cursor = due
			next, err = scheduleNext(schedule, tx.time, 0)
			if err != nil {
				return err
			}
		}
		return t.resolveCronDueTx(ctx, tx, schedule.ID, cursor, next, fire, tx.time)
	})
	if err != nil {
		return AdmissionResult{}, err
	}
	return result, nil
}

func (in scheduleAdmission) resolve(ctx context.Context, tx *storeTx) (capability.Principal, error) {
	s := in.schedule
	if in.consent == "" && s.GitHubInstallationID <= 0 {
		return capability.Principal{}, bindingRefusal()
	}
	tenant := &Tenant{s: tx.store, team: s.Team}
	if in.caller != nil {
		if in.caller.Target.Team != string(s.Team) {
			return capability.Principal{}, bindingRefusal()
		}
		if _, err := tenant.scheduleAuthorityTx(ctx, tx, in.caller.Principal.Authority); err != nil {
			return capability.Principal{}, err
		}
	}
	if in.consent != "" {
		var saved scheduleConsent
		if json.Unmarshal([]byte(in.consent), &saved) != nil || saved.Version != 1 {
			return capability.Principal{}, bindingRefusal()
		}
		if _, err := tenant.scheduleAuthorityTx(ctx, tx, saved.Authority); err != nil {
			return capability.Principal{}, err
		}
	}
	if s.GitHubInstallationID > 0 {
		grant := GitHubAppAutomation{InstallationID: s.GitHubInstallationID, RepositoryID: s.GitHubRepositoryID}
		var enabled int64
		if err := tx.QueryRowContext(ctx, `SELECT enabled_at FROM github_app_automation WHERE team=? AND installation_id=? AND repository_id=?`, s.Team, grant.InstallationID, grant.RepositoryID).Scan(&enabled); err != nil {
			return capability.Principal{}, err
		}
		grant.EnabledAt = time.Unix(0, enabled)
		if err := (&Tenant{s: tx.store, team: s.Team}).checkGitHubAppAutomationTx(ctx, tx, grant); err != nil {
			return capability.Principal{}, err
		}
	}
	current, err := scanCronSchedule(tx.QueryRowContext(ctx, `SELECT `+cronScheduleColumns+` FROM cron_schedules WHERE team=? AND id=? AND NOT EXISTS (SELECT 1 FROM team_deletions WHERE slug=? AND state=?)`+tx.forUpdate(), s.Team, s.ID, s.Team, TeamDeletionPending).Scan)
	if err != nil {
		return capability.Principal{}, err
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT consent_json FROM cron_schedules WHERE team=? AND id=?`, s.Team, s.ID).Scan(&raw); err != nil {
		return capability.Principal{}, err
	}
	if raw != in.consent {
		return capability.Principal{}, bindingRefusal()
	}
	if current.RepoPath != s.RepoPath || current.RepositoryKey != s.RepositoryKey || current.Pipeline != s.Pipeline || current.GitBranch != s.GitBranch || current.LockedRef != s.LockedRef || current.Where != s.Where || current.GitHubInstallationID != s.GitHubInstallationID || current.GitHubRepositoryID != s.GitHubRepositoryID || !current.ArmedAt.Equal(s.ArmedAt) || current.ArmedBy != s.ArmedBy || current.Paused != s.Paused || current.Declared != s.Declared || !reflect.DeepEqual(current.Effective(), s.Effective()) {
		return capability.Principal{}, authorityError(apierr.Forbidden, "schedule declaration changed before admission")
	}
	return capability.Principal{Authority: in.authority(), Capability: capability.Capability{Team: string(s.Team), Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectService, ID: s.ArmedBy}, Grants: []capability.Grant{capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}}}, nil
}

// VerifiedScheduleIntake returns source facts only from the locked schedule admission transaction.
func VerifiedScheduleIntake(ctx context.Context, resolver capability.AuthorityResolver, request IntakeRequest) (runspec.SourceLocation, error) {
	tx, ok := resolver.(*storeTx)
	if !ok || tx.scheduleAdmission == nil || tx.scheduleAdmission.authority() != request.Authority {
		return runspec.SourceLocation{}, authorityError(apierr.Forbidden, "verified schedule origin is unavailable")
	}
	if _, err := tx.scheduleAdmission.resolve(ctx, tx); err != nil {
		return runspec.SourceLocation{}, err
	}
	return request.Request.Source.Location, nil
}
