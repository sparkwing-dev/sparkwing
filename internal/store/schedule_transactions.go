package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// ScheduleSourcePolicy resolves stored source intent before the committing authority check.
type ScheduleSourcePolicy interface {
	ResolveScheduleSource(context.Context, CronSchedule, runspec.Request) (runspec.Request, error)
}

type scheduleConsent struct {
	Version   int                     `json:"version"`
	Authority capability.AuthorityRef `json:"authority"`
	Request   runspec.Request         `json:"request"`
}

type appScheduleScope struct {
	installationID int64
	repositoryID   int64
	publication    GitHubAppPublication
}

func ownsSchedule(app *appScheduleScope, repo string, row CronSchedule) bool {
	if app != nil {
		return row.GitHubInstallationID == app.installationID && row.GitHubRepositoryID == app.repositoryID
	}
	return row.GitHubRepositoryID == 0 && row.RepoPath == repo
}

func (t *Tenant) checkGitHubAppScheduleTx(ctx context.Context, tx *storeTx, installationID, repositoryID int64) error {
	if _, err := githubDeliveryFenceTx(ctx, tx); err != nil {
		return err
	}
	var enabled int64
	err := tx.QueryRowContext(ctx, `SELECT enabled_at FROM github_app_automation WHERE team=? AND installation_id=? AND repository_id=?`, t.team, installationID, repositoryID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGitHubAppAutomationRevoked
	}
	if err != nil {
		return err
	}
	return t.checkGitHubAppAutomationTx(ctx, tx, GitHubAppAutomation{InstallationID: installationID, RepositoryID: repositoryID, EnabledAt: time.Unix(0, enabled)})
}

func (t *Tenant) scheduleAuthorityTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) (capability.Principal, error) {
	p, err := tx.ResolveAuthority(ctx, ref)
	if err != nil {
		return p, err
	}
	if p.Capability.Subject.Kind == capability.SubjectLocal && p.Capability.Parent == nil && p.Capability.Binding.Kind == capability.BindingNone && t.team == DefaultTeam {
		p.Class = capability.CredentialPeer
	}
	route, ok := capability.LookupRoute("PUT", "/api/v1/schedules/repos")
	if !ok {
		return p, authorityError(apierr.PolicyMissing, "schedule policy is unavailable")
	}
	_, err = capability.Authorize(ctx, tx, route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, capability.Target{Team: string(t.team)})
	return p, err
}

func scheduleNext(row CronSchedule, now time.Time, minimum int64) (*time.Time, error) {
	d := row.Effective()
	trigger := pipelines.ScheduleTrigger{Cron: d.Cron, TZ: d.TZ, Overlap: d.Overlap, CatchUp: d.CatchUp.String(), Where: d.Where, Args: d.Args}
	if row.Pipeline == "" || d.CatchUp < 0 || d.Overlap != CronOverlapSkip && d.Overlap != CronOverlapQueue {
		return nil, ErrInvalidInput
	}
	parsed, err := cronspec.Parse(d.Cron)
	if err != nil {
		return nil, err
	}
	loc, err := trigger.Location()
	if err != nil {
		return nil, err
	}
	if minimum > 0 {
		if gap, ok := parsed.ShortestInterval(now, loc, 10); ok && int64(gap/time.Second) < minimum {
			return nil, &ComputeLimitError{Limit: ComputeLimitCronSeconds, Cap: minimum, Observed: int64(gap / time.Second)}
		}
	}
	next := parsed.Next(now, loc)
	if next.IsZero() {
		return nil, nil
	}
	return &next, nil
}

func (t *Tenant) scheduleRowsTx(ctx context.Context, tx *storeTx) ([]CronSchedule, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+cronScheduleColumns+` FROM cron_schedules WHERE team=? ORDER BY repo_path,pipeline,schedule_name`+tx.forUpdate(), t.team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronSchedule
	for rows.Next() {
		row, err := scanCronSchedule(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (t *Tenant) resolveScheduleTx(ctx context.Context, tx *storeTx, selector string) (CronSchedule, error) {
	rows, err := t.scheduleRowsTx(ctx, tx)
	if err != nil {
		return CronSchedule{}, err
	}
	var found []CronSchedule
	for _, row := range rows {
		repo := strings.TrimSuffix(filepath.Base(row.RepoPath), ".git")
		if !filepath.IsAbs(row.RepoPath) {
			if identity, err := sourceurl.Identity(row.RepoPath); err == nil {
				_, repo, _ = strings.Cut(identity, "/")
			}
		}
		base := repo + "/" + row.Pipeline
		name := row.Pipeline
		if row.Name != CronScheduleDefaultName {
			base += "/" + row.Name
			name += "/" + row.Name
		}
		if selector == row.ID {
			return row, nil
		}
		if selector == base || selector == name {
			found = append(found, row)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return CronSchedule{}, authorityError(apierr.Conflict, "schedule selector is ambiguous")
	}
	return CronSchedule{}, ErrNotFound
}

// ReplaceAuthorizedSchedules atomically publishes a tenant's complete repository declaration set.
func (t *Tenant) ReplaceAuthorizedSchedules(ctx context.Context, a capability.Authorized, body ScheduleRepoRequest) ([]string, []string, error) {
	if a.Target.Team != string(t.team) || body.Mode != ScheduleReplace && body.Mode != ScheduleUpsert {
		return nil, nil, ErrInvalidInput
	}
	remote, err := sourceurl.ValidateCloneURL(body.RepoURL)
	if err != nil {
		return nil, nil, err
	}
	var rows []CronSchedule
	var requests []runspec.Request
	policy, ok := t.s.intakePolicy.(ScheduleSourcePolicy)
	if !ok {
		return nil, nil, authorityError(apierr.Unavailable, "schedule source policy is unavailable")
	}
	for _, entry := range body.Schedules {
		name := entry.Name
		if name == "" {
			name = CronScheduleDefaultName
		}
		trigger := pipelines.ScheduleTrigger{Cron: entry.Cron, TZ: entry.TZ, Overlap: entry.Overlap, CatchUp: entry.CatchUp, Where: CronWhereController, Args: entry.Args}
		if err := trigger.Validate(entry.Pipeline); err != nil {
			return nil, nil, err
		}
		catchUp, err := trigger.CatchUpDuration()
		if err != nil {
			return nil, nil, err
		}
		ref := body.SHA
		if body.Follow {
			ref = ""
		}
		row := CronSchedule{Team: t.team, RepoPath: remote, Pipeline: entry.Pipeline, Name: name, Cron: entry.Cron, TZ: entry.TZ, Overlap: trigger.OverlapPolicy(), CatchUp: catchUp, Where: CronWhereController, GitBranch: body.Branch, Args: entry.Args, LockedRef: ref}
		request, err := policy.ResolveScheduleSource(ctx, row, runspec.Request{})
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, row)
		requests = append(requests, request)
	}
	return t.replaceConsentedSchedules(ctx, &a, nil, remote, rows, requests, body.Mode == ScheduleReplace)
}

// ReplaceAuthorizedLocalSchedules persists prepared checkout declarations and captured inputs together.
func (t *Tenant) ReplaceAuthorizedLocalSchedules(ctx context.Context, a capability.Authorized, root string, rows []CronSchedule, requests []runspec.Request, replace bool) ([]string, []string, error) {
	if len(rows) != len(requests) || !filepath.IsAbs(root) || a.Principal.Capability.Subject.Kind != capability.SubjectLocal || t.team != DefaultTeam {
		return nil, nil, bindingRefusal()
	}
	for i, row := range rows {
		if row.Where != CronWhereLocal || requests[i].Source.Location.LocalCheckout != root {
			return nil, nil, bindingRefusal()
		}
	}
	return t.replaceConsentedSchedules(ctx, &a, nil, root, rows, requests, replace)
}

// ReplaceGitHubAppSchedules publishes one installation's declarations under its live automation consent.
func (t *Tenant) replaceGitHubAppSchedules(ctx context.Context, publication GitHubAppPublication, repo string, proposed []CronSchedule, requests []runspec.Request) ([]string, []string, error) {
	installationID, repositoryID := publication.installationID, publication.repositoryID
	remote, err := sourceurl.ValidateCloneURL(repo)
	if err != nil || installationID <= 0 || repositoryID <= 0 || !strings.HasPrefix(remote, "https://github.com/") {
		return nil, nil, ErrInvalidInput
	}
	if len(proposed) != len(requests) {
		return nil, nil, ErrInvalidInput
	}
	key := runspec.RepoKey("gh:" + strconv.FormatInt(repositoryID, 10))
	for i, row := range proposed {
		if row.GitHubInstallationID != installationID || row.GitHubRepositoryID != repositoryID || row.LockedRef != "" || row.RepoPath != remote || row.Where != CronWhereController || requests[i].Git.Repository.Key != key {
			return nil, nil, ErrInvalidInput
		}
	}
	return t.replaceConsentedSchedules(ctx, nil, &appScheduleScope{installationID: installationID, repositoryID: repositoryID, publication: publication}, repo, proposed, requests, true)
}

func (t *Tenant) replaceConsentedSchedules(ctx context.Context, a *capability.Authorized, app *appScheduleScope, repo string, proposed []CronSchedule, requests []runspec.Request, replace bool) ([]string, []string, error) {
	if (a == nil) == (app == nil) || a != nil && a.Target.Team != string(t.team) || len(proposed) != len(requests) {
		return nil, nil, bindingRefusal()
	}
	var ids, withdrawn []string
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		ids, withdrawn = ids[:0], withdrawn[:0]
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := lockSet{}
			if a != nil {
				set = discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			}
			set.add(lockTeam, true, string(t.team))
			if app != nil {
				set = tx.operator.discoverInstallationLocks(ctx, tx, set, app.installationID)
			}
			return set
		}); err != nil {
			return err
		}
		apply := func() (githubPublicationResult, error) {
			if app != nil {
				if err := t.checkPublicationTx(ctx, tx, app.publication); err != nil {
					return githubPublicationResult{}, err
				}
			}
			err := func() error {
				var p capability.Principal
				var err error
				if a != nil {
					p, err = t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority)
				} else {
					err = t.checkGitHubAppScheduleTx(ctx, tx, app.installationID, app.repositoryID)
					p.Capability.Subject.ID = "github-app:" + strconv.FormatInt(app.installationID, 10)
				}
				if err != nil {
					return err
				}
				if filepath.IsAbs(repo) && (p.Capability.Subject.Kind != capability.SubjectLocal || t.team != DefaultTeam) {
					return bindingRefusal()
				}
				existing, err := t.scheduleRowsTx(ctx, tx)
				if err != nil {
					return err
				}
				repos := map[string]bool{}
				keep := map[string]bool{}
				seen := map[string]bool{}
				for _, row := range existing {
					if row.Declared {
						key := string(row.RepositoryKey)
						if key == "" {
							key = row.RepoPath
						}
						repos[key] = true
					}
				}
				repoKey := repo
				if len(requests) > 0 {
					repoKey = string(requests[0].Git.Repository.Key)
				}
				if p.Capability.Subject.Kind != capability.SubjectLocal && len(proposed) > 0 && !repos[repoKey] && len(repos) >= limits.MaxScheduledReposPerTeam {
					return authorityError(apierr.Conflict, "team schedule repository limit reached")
				}
				guards, err := computeLimitsTx(ctx, tx)
				if err != nil {
					return err
				}
				minimum := guards.CronSeconds
				if p.Capability.Subject.Kind == capability.SubjectLocal {
					minimum = 0
				}
				for i, row := range proposed {
					if row.RepoPath != repo || row.Pipeline == "" {
						return ErrInvalidInput
					}
					identity := row.Pipeline + "\x00" + row.Name
					if seen[identity] {
						return ErrInvalidInput
					}
					seen[identity] = true
					if app != nil && row.ID != "" {
						for _, old := range existing {
							if old.ID == row.ID && (old.GitHubInstallationID != app.installationID || old.GitHubRepositoryID != app.repositoryID) {
								return ErrCronScheduleTaken
							}
						}
					}
					for _, old := range existing {
						if ownsSchedule(app, repo, old) && old.Pipeline == row.Pipeline && old.Name == row.Name {
							row.ID = old.ID
							row.Override = old.Override
						}
					}
					if app == nil {
						for _, old := range existing {
							if old.GitHubRepositoryID > 0 && (old.RepoPath == repo || old.RepositoryKey == requests[i].Git.Repository.Key) && old.Pipeline == row.Pipeline && old.Name == row.Name {
								return ErrCronScheduleTaken
							}
						}
					} else {
						for _, old := range existing {
							if old.GitHubRepositoryID == 0 && (old.RepoPath == repo || old.RepositoryKey == requests[i].Git.Repository.Key) && old.Pipeline == row.Pipeline && old.Name == row.Name {
								return ErrCronScheduleTaken
							}
						}
					}
					if row.ID == "" {
						row.ID, err = newOpaqueIdentity("crn_")
						if err != nil {
							return err
						}
					}
					row.ArmedBy = p.Capability.Subject.ID
					row.NextDueAt, err = scheduleNext(row, tx.now(ctx), minimum)
					if err != nil {
						return err
					}
					request := requests[i]
					request.Version = 1
					request.Pipeline = row.Pipeline
					request.Team = string(t.team)
					request.Mode = runspec.IntakeExecute
					request.Origin = runspec.OriginSchedule
					request.Dispatch = ""
					request.RequestedArgs = row.Args
					request.Git.App = runspec.Optional[runspec.AppFacts]{}
					request.Lineage = runspec.Lineage{}
					request.PullRequest = runspec.Optional[runspec.PullRequest]{}
					repository := request.Git.Repository
					if repository.Key == "" {
						return ErrInvalidInput
					}
					args, err := encodeCronArgs(row.Args)
					if err != nil {
						return err
					}
					stored, _, err := t.armCronScheduleTx(ctx, tx, row, repository, args, tx.now(ctx))
					if err != nil {
						return err
					}
					if app == nil {
						consent, err := json.Marshal(scheduleConsent{Version: 1, Authority: p.Authority, Request: request})
						if err != nil {
							return err
						}
						if _, err = tx.ExecContext(ctx, `UPDATE cron_schedules SET consent_json=?,armed_by=? WHERE team=? AND id=?`, string(consent), p.Capability.Subject.ID, t.team, stored.ID); err != nil {
							return err
						}
					}
					ids = append(ids, stored.ID)
					keep[stored.ID] = true
				}
				count := len(keep)
				for _, row := range existing {
					owned := ownsSchedule(app, repo, row)
					if owned && !keep[row.ID] && row.Declared {
						if replace {
							if _, err = tx.ExecContext(ctx, `UPDATE cron_schedules SET declared=0,updated_at=? WHERE team=? AND id=?`, tx.now(ctx).UnixNano(), t.team, row.ID); err != nil {
								return err
							}
							withdrawn = append(withdrawn, row.Pipeline+"/"+row.Name)
						} else {
							count++
						}
					} else if !owned && !keep[row.ID] && row.Declared && string(row.RepositoryKey) == repoKey {
						count++
					}
				}
				if p.Capability.Subject.Kind != capability.SubjectLocal && count > limits.MaxSchedulesPerRepo {
					return authorityError(apierr.BadRequest, "repository schedule limit reached")
				}
				if a != nil {
					_, err = t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority)
					return err
				}
				return t.checkGitHubAppScheduleTx(ctx, tx, app.installationID, app.repositoryID)
			}()
			return githubPublicationResult{IDs: ids, Withdrawn: withdrawn}, err
		}
		var result githubPublicationResult
		var err error
		if app != nil {
			result, err = githubDeliveryResultTx(ctx, tx, githubPublicationEffect(app.repositoryID), apply)
		} else {
			result, err = apply()
		}
		ids, withdrawn = result.IDs, result.Withdrawn
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return ids, withdrawn, nil
}

func (t *Tenant) editSchedule(ctx context.Context, a capability.Authorized, selector string, edit func(*storeTx, CronSchedule) error) (string, error) {
	var id string
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if a.Target.Team != string(t.team) {
			return bindingRefusal()
		}
		if _, err := t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority); err != nil {
			return err
		}
		row, err := t.resolveScheduleTx(ctx, tx, selector)
		if err != nil {
			return err
		}
		if row.GitHubRepositoryID > 0 {
			if err := bumpGitHubPublicationTx(ctx, tx, t.team, row.GitHubRepositoryID); err != nil {
				return err
			}
		}
		id = row.ID
		if err = edit(tx, row); err != nil {
			return err
		}
		_, err = t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority)
		return err
	})
	return id, err
}

// PauseAuthorizedSchedule changes pause state under current tenant authority.
func (t *Tenant) PauseAuthorizedSchedule(ctx context.Context, a capability.Authorized, id string, paused bool) (string, error) {
	return t.editSchedule(ctx, a, id, func(tx *storeTx, row CronSchedule) error {
		if row.Paused && !paused {
			next, err := scheduleNext(row, tx.now(ctx), 0)
			if err != nil {
				return err
			}
			now := tx.now(ctx)
			if err := t.resolveCronDueTx(ctx, tx, row.ID, now, next, nil, now); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE cron_schedules SET paused=?,updated_at=? WHERE team=? AND id=?`, boolToInt(paused), tx.now(ctx).UnixNano(), t.team, row.ID)
		return err
	})
}

// OverrideAuthorizedSchedule validates and persists an effective cadence atomically.
func (t *Tenant) OverrideAuthorizedSchedule(ctx context.Context, a capability.Authorized, id string, body *cronspec.Override) (string, error) {
	return t.editSchedule(ctx, a, id, func(tx *storeTx, row CronSchedule) error {
		if body == nil {
			row.Override = nil
		} else {
			if err := body.Validate(); err != nil {
				return err
			}
			o := &CronOverride{Base: row.Declaration(), SetAt: tx.now(ctx)}
			if body.Cron != nil {
				o.Cron = *body.Cron
			}
			if body.TZ != nil {
				o.TZ = *body.TZ
				o.TZSet = true
			}
			if body.Overlap != nil {
				o.Overlap = *body.Overlap
			}
			if body.Args != nil {
				o.Args = *body.Args
			}
			if body.CatchUp != nil {
				d, err := time.ParseDuration(*body.CatchUp)
				if err != nil {
					return err
				}
				o.CatchUp = &d
			}
			row.Override = o
		}
		guards, err := computeLimitsTx(ctx, tx)
		if err != nil {
			return err
		}
		minimum := guards.CronSeconds
		if a.Principal.Capability.Subject.Kind == capability.SubjectLocal {
			minimum = 0
		}
		next, err := scheduleNext(row, tx.now(ctx), minimum)
		if err != nil {
			return err
		}
		if row.Override == nil {
			_, err = tx.ExecContext(ctx, `UPDATE cron_schedules SET override_cron=NULL,override_tz=NULL,override_overlap=NULL,override_catch_up_ns=NULL,override_args=NULL,override_base=NULL,override_set_at=NULL,next_due_at=?,updated_at=? WHERE team=? AND id=?`, nullNanos(next), tx.now(ctx).UnixNano(), t.team, row.ID)
			return err
		}
		if err = t.setCronOverrideTx(ctx, tx, row.ID, *row.Override, tx.now(ctx)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE cron_schedules SET next_due_at=? WHERE team=? AND id=?`, nullNanos(next), t.team, row.ID)
		return err
	})
}

// DeleteAuthorizedSchedule removes a resolved schedule and history under current tenant authority.
func (t *Tenant) DeleteAuthorizedSchedule(ctx context.Context, a capability.Authorized, id string) (string, bool, error) {
	resolved, err := t.editSchedule(ctx, a, id, func(tx *storeTx, row CronSchedule) error { return deleteScheduleTx(ctx, tx, t.team, row.ID) })
	return resolved, err == nil, err
}

func deleteScheduleTx(ctx context.Context, tx *storeTx, team Team, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM cron_fires WHERE team=? AND schedule_id=?`, team, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM cron_schedules WHERE team=? AND id=?`, team, id)
	return err
}

// DeleteAuthorizedSchedules atomically removes a repository's schedules and history.
func (t *Tenant) DeleteAuthorizedSchedules(ctx context.Context, a capability.Authorized, repo string) (int, error) {
	count := 0
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		count = 0
		if a.Target.Team != string(t.team) {
			return bindingRefusal()
		}
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			set.add(lockTeam, true, string(t.team))
			return set
		}); err != nil {
			return err
		}
		if _, err := t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority); err != nil {
			return err
		}
		rows, err := t.scheduleRowsTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.RepoPath == repo {
				if row.GitHubRepositoryID > 0 {
					if err := bumpGitHubPublicationTx(ctx, tx, t.team, row.GitHubRepositoryID); err != nil {
						return err
					}
				}
				if err = deleteScheduleTx(ctx, tx, t.team, row.ID); err != nil {
					return err
				}
				count++
			}
		}
		_, err = t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority)
		return err
	})
	return count, err
}

// RunAuthorizedSchedule admits a run now with transactional request replay.
func (t *Tenant) RunAuthorizedSchedule(ctx context.Context, a capability.Authorized, id, key string) (string, string, bool, error) {
	if strings.TrimSpace(key) == "" {
		return "", "", false, ErrInvalidInput
	}
	var row CronSchedule
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		row, err = t.resolveScheduleTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return "", "", false, err
	}
	result, err := t.admitSchedule(ctx, row, "manual:"+key, time.Time{}, &a)
	return row.ID, result.RunID, result.Replayed, err
}
