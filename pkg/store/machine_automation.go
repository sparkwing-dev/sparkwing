package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Machine automation states report a personal machine's standing for its owner's automated runs.
const (
	AutomationNone      = "none"
	AutomationRequested = "requested"
	AutomationApproved  = "approved"
)

// RequestMachineAutomation lets a personal machine's owner, signed in directly, request or withdraw automated runs.
func (t *Tenant) RequestMachineAutomation(ctx context.Context, authority capability.AuthorityRef, machine string, requested bool) (string, error) {
	var state string
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		p, err := tx.ResolveAuthority(ctx, authority)
		if err != nil {
			return err
		}
		if !p.DirectAccountSession() || p.Capability.Team != string(t.team) || p.Capability.Audience != capability.Controller {
			return authorityError(apierr.Forbidden, "requesting automated runs needs the owner's own sign-in session")
		}
		prefixes, owner, err := personalMachineTx(ctx, tx, t.team, machine)
		if err != nil {
			return err
		}
		if owner != p.Capability.Subject.ID {
			return authorityError(apierr.Forbidden, "only a personal machine's owner requests its automated runs")
		}
		kind := "runner.automation_withdrawn"
		if requested {
			kind = "runner.automation_requested"
		}
		for _, prefix := range prefixes {
			if requested {
				_, err = tx.ExecContext(ctx, `INSERT INTO machine_automation(team,prefix,owner,requested_at) VALUES (?,?,?,?) ON CONFLICT(team,prefix) DO NOTHING`, t.team, prefix, owner, tx.now().UnixNano())
			} else {
				_, err = tx.ExecContext(ctx, `DELETE FROM machine_automation WHERE team=? AND prefix=?`, t.team, prefix)
			}
			if err != nil {
				return err
			}
		}
		if state, err = machineAutomationTx(ctx, tx, t.team, prefixes[0]); err != nil {
			return err
		}
		return automationAuditTx(tx, t.team, kind, owner, machine)
	})
	return state, err
}

// ApproveMachineAutomation lets a team administrator approve or revoke a personal machine's automated runs.
// Both cover every live credential generation of the machine at once, so a rotation's predecessor keeps no approval.
func (t *Tenant) ApproveMachineAutomation(ctx context.Context, a capability.Authorized, machine string, approved bool) (string, error) {
	var state string
	err := t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		prefixes, _, err := personalMachineTx(ctx, tx, t.team, machine)
		if err != nil {
			return err
		}
		kind := "runner.automation_revoked"
		if approved {
			kind = "runner.automation_approved"
		}
		var changed int64
		for _, prefix := range prefixes {
			var result sql.Result
			if approved {
				result, err = tx.ExecContext(ctx, `UPDATE machine_automation SET approved_by=?,approved_at=? WHERE team=? AND prefix=?`, p.Capability.Subject.ID, tx.now().UnixNano(), t.team, prefix)
			} else {
				result, err = tx.ExecContext(ctx, `DELETE FROM machine_automation WHERE team=? AND prefix=?`, t.team, prefix)
			}
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			changed += count
		}
		if approved && changed == 0 {
			return authorityError(apierr.BadRequest, "the machine's owner has not requested automated runs")
		}
		if state, err = machineAutomationTx(ctx, tx, t.team, prefixes[0]); err != nil {
			return err
		}
		return automationAuditTx(tx, t.team, kind, p.Capability.Subject.ID, machine)
	})
	return state, err
}

// safety: approval changes cover every live generation, so a rotation predecessor in grace keeps no approval.
func personalMachineTx(ctx context.Context, tx *storeTx, team Team, machine string) ([]string, string, error) {
	if !ValidRunnerName(machine) {
		return nil, "", authorityError(apierr.BadRequest, "invalid runner name")
	}
	now := tx.now().UnixNano()
	rows, err := tx.QueryContext(ctx, `SELECT prefix,created_by FROM capabilities WHERE team=? AND subject_kind='machine' AND subject_id=? AND bind_kind='none' AND parent_id IS NULL AND (revoked_at IS NULL OR revoked_at>?) AND (expires_at IS NULL OR expires_at>?) ORDER BY created_at DESC,prefix`, team, machine, now, now)
	if err != nil {
		return nil, "", err
	}
	type root struct{ prefix, owner string }
	var roots []root
	for rows.Next() {
		var r root
		if err := rows.Scan(&r.prefix, &r.owner); err != nil {
			return nil, "", errors.Join(err, rows.Close())
		}
		roots = append(roots, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, "", err
	}
	var prefixes []string
	owner := ""
	for _, r := range roots {
		record, err := loadCapabilityRecordTx(ctx, tx, r.prefix)
		if err != nil {
			return nil, "", err
		}
		if record.Origin == nil || !record.Origin.Personal || owner != "" && r.owner != owner {
			continue
		}
		prefixes, owner = append(prefixes, r.prefix), r.owner
	}
	if len(prefixes) == 0 {
		return nil, "", notFound("personal machine", machine)
	}
	return prefixes, owner, nil
}

func machineAutomationTx(ctx context.Context, tx *storeTx, team Team, prefix string) (string, error) {
	var approved int64
	err := tx.QueryRowContext(ctx, `SELECT approved_at FROM machine_automation WHERE team=? AND prefix=?`, team, prefix).Scan(&approved)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return AutomationNone, nil
	case err != nil:
		return "", err
	case approved > 0:
		return AutomationApproved, nil
	}
	return AutomationRequested, nil
}

func automationAuditTx(tx *storeTx, team Team, kind, actor, machine string) error {
	id, err := newIdentityID()
	if err != nil {
		return err
	}
	return RecordBusinessEvent(tx, BusinessEvent{At: tx.now(), Team: team, Kind: kind, SubjectID: id, Actor: actor, Attrs: map[string]any{"runner": machine}})
}

// PersonalRepoAccessNeeds names the owner's GitHub ID and the sealed repositories whose access the controller must
// check before an approved personal machine claims: those of every waiting unit of the owner's automated runs.
// Fresh is true for a repository checked within half of limits.PersonalRepoAccess.
func (t *Tenant) PersonalRepoAccessNeeds(ctx context.Context, prefix string) (int64, map[runspec.RepoKey]bool, error) {
	var github int64
	needs := map[runspec.RepoKey]bool{}
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var owner, subject string
		err := tx.QueryRowContext(ctx, `SELECT owner FROM machine_automation WHERE team=? AND prefix=? AND approved_at>0`, t.team, prefix).Scan(&owner)
		if err == nil {
			err = tx.QueryRowContext(ctx, `SELECT subject FROM identities WHERE provider=? AND account_id=? ORDER BY subject LIMIT 1`, ProviderGitHub, owner).Scan(&subject)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if github, err = strconv.ParseInt(subject, 10, 64); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT r.request_json FROM runs r WHERE r.team=? AND r.dispatch='controller' AND r.status IN ('pending','running') AND (
 EXISTS (SELECT 1 FROM triggers g WHERE g.team=r.team AND g.id=r.id AND g.unit_kind='plan' AND g.status='pending' AND g.claimed_by IS NULL)
 OR EXISTS (SELECT 1 FROM nodes n WHERE n.team=r.team AND n.run_id=r.id AND n.status='pending' AND n.ready_at IS NOT NULL AND n.claimed_by IS NULL)
 OR EXISTS (SELECT 1 FROM eval_units e WHERE e.team=r.team AND e.run_id=r.id AND e.status='pending' AND e.claimed_by IS NULL))`, t.team)
		if err != nil {
			return err
		}
		var identities []runspec.Automation
		for rows.Next() {
			var body []byte
			var seal runspec.SealedRequest
			if err := rows.Scan(&body); err != nil {
				return errors.Join(err, rows.Close())
			}
			if err := json.Unmarshal(body, &seal); err != nil {
				return errors.Join(err, rows.Close())
			}
			if seal.Request.Automation.Set {
				identities = append(identities, seal.Request.Automation.Value)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, identity := range identities {
			account, err := automationOwnerTx(ctx, tx, t.team, identity)
			if err != nil {
				return err
			}
			if account != owner {
				continue
			}
			for _, repo := range identity.Repos {
				var checked int64
				err := tx.QueryRowContext(ctx, `SELECT checked_at FROM machine_repo_access WHERE team=? AND prefix=? AND repo=?`, t.team, prefix, repo).Scan(&checked)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				needs[repo] = needs[repo] || checked > tx.now().Add(-limits.PersonalRepoAccess/2).UnixNano()
			}
		}
		return nil
	})
	return github, needs, err
}

// RecordPersonalRepoAccess stores the controller's server-side access checks, by repository ID, for a personal machine.
func (t *Tenant) RecordPersonalRepoAccess(ctx context.Context, prefix string, access map[runspec.RepoKey]bool) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		for repo, allowed := range access {
			if _, err := tx.ExecContext(ctx, `INSERT INTO machine_repo_access(team,prefix,repo,allowed,checked_at) VALUES (?,?,?,?,?) ON CONFLICT(team,prefix,repo) DO UPDATE SET allowed=excluded.allowed,checked_at=excluded.checked_at`, t.team, prefix, repo, boolInt(allowed), tx.now().UnixNano()); err != nil {
				return err
			}
		}
		return nil
	})
}

// AwardedAutomation returns the owner's GitHub ID and the sealed repositories when a personal machine was awarded a
// unit of an automated run, so the controller can recheck access live; github is zero otherwise.
func (t *Tenant) AwardedAutomation(ctx context.Context, claim UnitClaim, prefix string) (int64, []runspec.RepoKey, error) {
	var github int64
	var repos []runspec.RepoKey
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, false)
		if err != nil {
			return err
		}
		if personal, err := personalRootTx(ctx, tx, chain); err != nil || !personal {
			return err
		}
		seal, err := sealedRequestTx(ctx, tx, t.team, claim.RunID)
		if err != nil || !seal.Request.Automation.Set {
			return err
		}
		owner, err := automationOwnerTx(ctx, tx, t.team, seal.Request.Automation.Value)
		if err != nil || owner == "" {
			return err
		}
		var subject string
		err = tx.QueryRowContext(ctx, `SELECT subject FROM identities WHERE provider=? AND account_id=? ORDER BY subject LIMIT 1`, ProviderGitHub, owner).Scan(&subject)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		github, err = strconv.ParseInt(subject, 10, 64)
		repos = seal.Request.Automation.Value.Repos
		return err
	})
	return github, repos, err
}
