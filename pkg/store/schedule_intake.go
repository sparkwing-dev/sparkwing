package store

import (
	"context"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

type scheduleAdmission struct{ schedule CronSchedule }

func (in scheduleAdmission) authority() capability.AuthorityRef {
	return capability.AuthorityRef{Kind: "schedule", ID: in.schedule.ID}
}

// AdmitScheduledRun admits a persisted controller schedule through the shared sealed intake.
func (t *Tenant) AdmitScheduledRun(ctx context.Context, schedule CronSchedule, due time.Time) (AdmissionResult, error) {
	if schedule.Team != t.team || schedule.Where != CronWhereController || due.IsZero() {
		return AdmissionResult{}, ErrInvalidInput
	}
	remote, err := sourceurl.ValidateCloneURL(schedule.RepoPath)
	if err != nil {
		return AdmissionResult{}, err
	}
	in := scheduleAdmission{schedule: schedule}
	request := IntakeRequest{Authority: in.authority(), IdempotencyKey: schedule.ID + "@" + due.UTC().Format(time.RFC3339Nano), Request: runspec.Request{
		Version: 1, Team: string(t.team), Pipeline: schedule.Pipeline, Mode: runspec.IntakeExecute, Origin: runspec.OriginSchedule, Dispatch: runspec.DispatchHost,
		RequestedArgs: schedule.Effective().Args,
		Git:           runspec.Git{Repository: runspec.Repository{Key: schedule.RepositoryKey, Display: schedule.RepoPath}, Ref: schedule.GitBranch, SHA: schedule.LockedRef},
		Source:        runspec.Source{Location: runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"}},
	}}
	var result AdmissionResult
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		tx.scheduleAdmission = &in
		var err error
		result, err = admitRun(ctx, tx, t.team, request, tx.now())
		if err == nil {
			_, err = in.resolve(ctx, tx)
		}
		return err
	})
	if err != nil {
		return AdmissionResult{}, err
	}
	return result, nil
}

func (in scheduleAdmission) resolve(ctx context.Context, tx *storeTx) (capability.Principal, error) {
	s := in.schedule
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
	current, err := scanCronSchedule(tx.QueryRowContext(ctx, `SELECT `+cronScheduleColumns+` FROM cron_schedules WHERE team=? AND id=? AND NOT EXISTS (SELECT 1 FROM team_deletions WHERE slug=? AND state=?)`+tx.forShare(), s.Team, s.ID, s.Team, TeamDeletionPending).Scan)
	if err != nil {
		return capability.Principal{}, err
	}
	if current.RepoPath != s.RepoPath || current.RepositoryKey != s.RepositoryKey || current.Pipeline != s.Pipeline || current.GitHubInstallationID != s.GitHubInstallationID || current.GitHubRepositoryID != s.GitHubRepositoryID || !current.ArmedAt.Equal(s.ArmedAt) || current.ArmedBy != s.ArmedBy || current.Paused != s.Paused || current.Declared != s.Declared || !reflect.DeepEqual(current.Effective(), s.Effective()) {
		return capability.Principal{}, authorityError(apierr.Forbidden, "schedule declaration changed before admission")
	}
	return capability.Principal{Authority: in.authority(), Capability: capability.Capability{Team: string(s.Team), Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectService, ID: s.ID}, Grants: []capability.Grant{capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}}}, nil
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
