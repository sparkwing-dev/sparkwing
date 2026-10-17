package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
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
		base := strings.TrimSuffix(filepath.Base(row.RepoPath), ".git") + "/" + row.Pipeline
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
func (t *Tenant) ReplaceAuthorizedSchedules(ctx context.Context, a capability.Authorized, body controllerwire.ScheduleRepoRequest) ([]string, []string, error) {
	if a.Target.Team != string(t.team) || body.Mode != controllerwire.ScheduleReplace && body.Mode != controllerwire.ScheduleUpsert {
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
	return t.replaceConsentedSchedules(ctx, a, remote, rows, requests, body.Mode == controllerwire.ScheduleReplace)
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
	return t.replaceConsentedSchedules(ctx, a, root, rows, requests, replace)
}

func (t *Tenant) replaceConsentedSchedules(ctx context.Context, a capability.Authorized, repo string, proposed []CronSchedule, requests []runspec.Request, replace bool) ([]string, []string, error) {
	if a.Target.Team != string(t.team) || len(proposed) != len(requests) {
		return nil, nil, bindingRefusal()
	}
	var ids, withdrawn []string
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		p, err := t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority)
		if err != nil {
			return err
		}
		if filepath.IsAbs(repo) && (p.Capability.Subject.Kind != capability.SubjectLocal || t.team != DefaultTeam) {
			return bindingRefusal()
		}
		if err := t.lockTeamTx(ctx, tx); err != nil {
			return err
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
				repos[row.RepoPath] = true
			}
		}
		if p.Capability.Subject.Kind != capability.SubjectLocal && len(proposed) > 0 && !repos[repo] && len(repos) >= 10 {
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
			for _, old := range existing {
				if old.RepoPath == repo && old.Pipeline == row.Pipeline && old.Name == row.Name {
					row.ID = old.ID
					row.Override = old.Override
				}
			}
			if row.ID == "" {
				row.ID, err = newOpaqueIdentity("crn_")
				if err != nil {
					return err
				}
			}
			row.ArmedBy = p.Capability.Subject.ID
			row.NextDueAt, err = scheduleNext(row, tx.now(), minimum)
			if err != nil {
				return err
			}
			request := requests[i]
			request.Version = 1
			request.Pipeline = row.Pipeline
			request.Team = string(t.team)
			request.Mode = runspec.IntakeExecute
			request.Origin = runspec.OriginSchedule
			request.Dispatch = runspec.DispatchController
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
			stored, _, err := t.armCronScheduleTx(ctx, tx, row, repository, args, tx.now())
			if err != nil {
				return err
			}
			consent, err := json.Marshal(scheduleConsent{Version: 1, Authority: p.Authority, Request: request})
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE cron_schedules SET consent_json=?,armed_by=? WHERE team=? AND id=?`, string(consent), p.Capability.Subject.ID, t.team, stored.ID); err != nil {
				return err
			}
			ids = append(ids, stored.ID)
			keep[stored.ID] = true
		}
		count := len(keep)
		for _, row := range existing {
			if row.RepoPath == repo && !keep[row.ID] && row.Declared {
				if replace {
					if _, err = tx.ExecContext(ctx, `UPDATE cron_schedules SET declared=0,updated_at=? WHERE team=? AND id=?`, tx.now().UnixNano(), t.team, row.ID); err != nil {
						return err
					}
					withdrawn = append(withdrawn, row.Pipeline+"/"+row.Name)
				} else {
					count++
				}
			}
		}
		if p.Capability.Subject.Kind != capability.SubjectLocal && count > 20 {
			return authorityError(apierr.BadRequest, "repository schedule limit reached")
		}
		_, err = t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority)
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
		_, err := tx.ExecContext(ctx, `UPDATE cron_schedules SET paused=?,updated_at=? WHERE team=? AND id=?`, boolToInt(paused), tx.now().UnixNano(), t.team, row.ID)
		return err
	})
}

// OverrideAuthorizedSchedule validates and persists an effective cadence atomically.
func (t *Tenant) OverrideAuthorizedSchedule(ctx context.Context, a capability.Authorized, id string, body *controllerwire.ScheduleOverride) (string, error) {
	return t.editSchedule(ctx, a, id, func(tx *storeTx, row CronSchedule) error {
		if body == nil {
			row.Override = nil
		} else {
			if err := body.Validate(); err != nil {
				return err
			}
			o := &CronOverride{Base: row.Declaration(), SetAt: tx.now()}
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
		next, err := scheduleNext(row, tx.now(), minimum)
		if err != nil {
			return err
		}
		if row.Override == nil {
			_, err = tx.ExecContext(ctx, `UPDATE cron_schedules SET override_cron=NULL,override_tz=NULL,override_overlap=NULL,override_catch_up_ns=NULL,override_args=NULL,override_base=NULL,override_set_at=NULL,next_due_at=?,updated_at=? WHERE team=? AND id=?`, nullNanos(next), tx.now().UnixNano(), t.team, row.ID)
			return err
		}
		if err = t.setCronOverrideTx(ctx, tx, row.ID, *row.Override, tx.now()); err != nil {
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
		if a.Target.Team != string(t.team) {
			return bindingRefusal()
		}
		if _, err := t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority); err != nil {
			return err
		}
		if err := t.lockTeamTx(ctx, tx); err != nil {
			return err
		}
		rows, err := t.scheduleRowsTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.RepoPath == repo {
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

func (t *Tenant) scheduleConsentTx(ctx context.Context, tx *storeTx, id string) (scheduleConsent, error) {
	var raw string
	var c scheduleConsent
	err := tx.QueryRowContext(ctx, `SELECT consent_json FROM cron_schedules WHERE team=? AND id=?`+tx.forUpdate(), t.team, id).Scan(&raw)
	if err != nil {
		return c, err
	}
	if json.Unmarshal([]byte(raw), &c) != nil || c.Version != 1 || c.Authority.ID == "" {
		return c, authorityError(apierr.Forbidden, "schedule has no arming consent")
	}
	_, err = t.scheduleAuthorityTx(ctx, tx, c.Authority)
	return c, err
}

// AdmitScheduledRun seals at most one run for an authorized schedule's due instant.
func (t *Tenant) AdmitScheduledRun(ctx context.Context, id string, due time.Time) (AdmissionResult, error) {
	if due.IsZero() {
		return AdmissionResult{}, ErrInvalidInput
	}
	return t.admitSchedule(ctx, capability.Authorized{}, id, "due:"+due.UTC().Format(time.RFC3339Nano), due)
}

// RunAuthorizedSchedule admits a run now with transactional request replay.
func (t *Tenant) RunAuthorizedSchedule(ctx context.Context, a capability.Authorized, id, key string) (string, string, bool, error) {
	if strings.TrimSpace(key) == "" {
		return "", "", false, ErrInvalidInput
	}
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if a.Target.Team != string(t.team) {
			return bindingRefusal()
		}
		if _, err := t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority); err != nil {
			return err
		}
		row, err := t.resolveScheduleTx(ctx, tx, id)
		if err == nil {
			id = row.ID
		}
		return err
	})
	if err != nil {
		return "", "", false, err
	}
	result, err := t.admitSchedule(ctx, a, id, "manual:"+key, time.Time{})
	return id, result.RunID, result.Replayed, err
}

func (t *Tenant) scheduleAdmissionTx(ctx context.Context, tx *storeTx, a capability.Authorized, selector, key string) (CronSchedule, scheduleConsent, AdmissionResult, error) {
	var row CronSchedule
	var consent scheduleConsent
	var result AdmissionResult
	if a.Principal.Authority.ID != "" {
		if a.Target.Team != string(t.team) {
			return row, consent, result, bindingRefusal()
		}
		if _, err := t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority); err != nil {
			return row, consent, result, err
		}
	}
	row, err := t.resolveScheduleTx(ctx, tx, selector)
	if err != nil {
		return row, consent, result, err
	}
	consent, err = t.scheduleConsentTx(ctx, tx, row.ID)
	if err != nil {
		return row, consent, result, err
	}
	if !row.Declared || row.Paused {
		return row, consent, result, authorityError(apierr.Conflict, "schedule is paused or withdrawn")
	}
	scope, err := scheduleIntakeScope(row.ID)
	if err != nil {
		return row, consent, result, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE team=? AND idempotency_scope=? AND idempotency_key=?`, t.team, scope, key).Scan(&previous)
	if err == nil {
		result, err = admissionResultTx(ctx, tx, t.team, previous)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return row, consent, result, err
}

func scheduleAdmissionHash(row CronSchedule, consent scheduleConsent) (string, error) {
	return stateHash(struct {
		Row     CronSchedule
		Consent scheduleConsent
	}{row, consent})
}

func (t *Tenant) admitSchedule(ctx context.Context, a capability.Authorized, selector, key string, due time.Time) (AdmissionResult, error) {
	var row CronSchedule
	var consent scheduleConsent
	var result AdmissionResult
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		row, consent, result, err = t.scheduleAdmissionTx(ctx, tx, a, selector, key)
		return err
	})
	if err != nil || result.Replayed {
		return result, err
	}
	fence, err := scheduleAdmissionHash(row, consent)
	if err != nil {
		return result, err
	}
	policy, ok := t.s.intakePolicy.(ScheduleSourcePolicy)
	if !ok {
		return result, authorityError(apierr.Unavailable, "schedule source policy is unavailable")
	}
	request, err := policy.ResolveScheduleSource(ctx, row, consent.Request)
	if err != nil {
		return result, err
	}
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		current, authority, replay, err := t.scheduleAdmissionTx(ctx, tx, a, row.ID, key)
		if err != nil {
			return err
		}
		if replay.Replayed {
			result = replay
			return nil
		}
		hash, err := scheduleAdmissionHash(current, authority)
		if err != nil {
			return err
		}
		if hash != fence {
			return authorityError(apierr.Conflict, "schedule changed during source resolution")
		}
		now := tx.now()
		if !due.IsZero() {
			next, err := scheduleNext(row, due.Add(-time.Nanosecond), 0)
			if err != nil {
				return err
			}
			if due.After(now) || !due.After(row.CursorAt) || next == nil || !next.Equal(due) {
				return authorityError(apierr.Conflict, "schedule instant is not due")
			}
		}
		request.RequestedArgs = row.Effective().Args
		result, err = admitRun(ctx, tx, t.team, IntakeRequest{Authority: consent.Authority, Request: request, IdempotencyKey: key, scheduleID: row.ID}, now)
		if err != nil {
			return err
		}
		if _, err = t.scheduleAuthorityTx(ctx, tx, consent.Authority); err != nil {
			return err
		}
		if a.Principal.Authority.ID != "" {
			if _, err = t.scheduleAuthorityTx(ctx, tx, a.Principal.Authority); err != nil {
				return err
			}
		}
		decided := due
		if decided.IsZero() {
			decided = now
		}
		next, err := scheduleNext(row, now, 0)
		if err != nil {
			return err
		}
		return t.resolveCronDueTx(ctx, tx, row.ID, decided, next, &CronFire{DueAt: decided, DecidedAt: now, Outcome: CronOutcomeFired, RunID: result.RunID, Args: request.RequestedArgs}, now)
	})
	if err != nil {
		return AdmissionResult{}, err
	}
	return result, nil
}

func scheduleIntakeScope(id string) (string, error) { return stateHash(struct{ Schedule string }{id}) }

// ScheduledSourceVerified checks that the private admission path uses the persisted consent's repository.
func ScheduledSourceVerified(ctx context.Context, resolver capability.AuthorityResolver, request IntakeRequest) bool {
	tx, ok := resolver.(*storeTx)
	if !ok || request.scheduleID == "" {
		return false
	}
	var raw string
	if tx.QueryRowContext(ctx, `SELECT consent_json FROM cron_schedules WHERE team=? AND id=?`, request.Request.Team, request.scheduleID).Scan(&raw) != nil {
		return false
	}
	var consent scheduleConsent
	return json.Unmarshal([]byte(raw), &consent) == nil && consent.Version == 1 && consent.Authority == request.Authority && consent.Request.Git.Repository == request.Request.Git.Repository && consent.Request.Source.Location == request.Request.Source.Location
}
