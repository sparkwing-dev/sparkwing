package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

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
		prefix, owner, err := personalMachineTx(ctx, tx, t.team, machine)
		if err != nil {
			return err
		}
		if owner != p.Capability.Subject.ID {
			return authorityError(apierr.Forbidden, "only a personal machine's owner requests its automated runs")
		}
		kind := "runner.automation_withdrawn"
		if requested {
			kind = "runner.automation_requested"
			_, err = tx.ExecContext(ctx, `INSERT INTO machine_automation(team,prefix,owner,requested_at) VALUES (?,?,?,?) ON CONFLICT(team,prefix) DO NOTHING`, t.team, prefix, owner, tx.now().UnixNano())
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM machine_automation WHERE team=? AND prefix=?`, t.team, prefix)
		}
		if err != nil {
			return err
		}
		if state, err = machineAutomationTx(ctx, tx, t.team, prefix); err != nil {
			return err
		}
		return automationAuditTx(tx, t.team, kind, owner, machine)
	})
	return state, err
}

// ApproveMachineAutomation lets a team administrator approve or revoke a personal machine's automated runs.
func (t *Tenant) ApproveMachineAutomation(ctx context.Context, a capability.Authorized, machine string, approved bool) (string, error) {
	var state string
	err := t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		prefix, _, err := personalMachineTx(ctx, tx, t.team, machine)
		if err != nil {
			return err
		}
		kind := "runner.automation_revoked"
		if approved {
			kind = "runner.automation_approved"
			var result sql.Result
			result, err = tx.ExecContext(ctx, `UPDATE machine_automation SET approved_by=?,approved_at=? WHERE team=? AND prefix=?`, p.Capability.Subject.ID, tx.now().UnixNano(), t.team, prefix)
			if err == nil {
				if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
					err = errors.Join(countErr, authorityError(apierr.BadRequest, "the machine's owner has not requested automated runs"))
				}
			}
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM machine_automation WHERE team=? AND prefix=?`, t.team, prefix)
		}
		if err != nil {
			return err
		}
		if state, err = machineAutomationTx(ctx, tx, t.team, prefix); err != nil {
			return err
		}
		return automationAuditTx(tx, t.team, kind, p.Capability.Subject.ID, machine)
	})
	return state, err
}

func personalMachineTx(ctx context.Context, tx *storeTx, team Team, machine string) (string, string, error) {
	if !ValidRunnerName(machine) {
		return "", "", authorityError(apierr.BadRequest, "invalid runner name")
	}
	now := tx.now().UnixNano()
	rows, err := tx.QueryContext(ctx, `SELECT prefix,created_by FROM capabilities WHERE team=? AND subject_kind='machine' AND subject_id=? AND bind_kind='none' AND parent_id IS NULL AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?)`, team, machine, now)
	if err != nil {
		return "", "", err
	}
	type root struct{ prefix, owner string }
	var roots []root
	for rows.Next() {
		var r root
		if err := rows.Scan(&r.prefix, &r.owner); err != nil {
			return "", "", errors.Join(err, rows.Close())
		}
		roots = append(roots, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", "", err
	}
	for _, r := range roots {
		record, err := loadCapabilityRecordTx(ctx, tx, r.prefix)
		if err != nil {
			return "", "", err
		}
		if record.Origin != nil && record.Origin.Personal {
			return r.prefix, r.owner, nil
		}
	}
	return "", "", notFound("personal machine", machine)
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

// PersonalRepoAccessNeed is one repository whose access the controller must check for an approved personal machine.
type PersonalRepoAccessNeed struct {
	OwnerGitHubID int64
	Repository    string
}

// PersonalRepoAccessNeeds lists stale repository checks for the approved personal machine's waiting automated runs.
func (t *Tenant) PersonalRepoAccessNeeds(ctx context.Context, prefix string) ([]PersonalRepoAccessNeed, error) {
	var out []PersonalRepoAccessNeed
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		out = nil
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT owner FROM machine_automation WHERE team=? AND prefix=? AND approved_at>0`, t.team, prefix).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var github string
		err = tx.QueryRowContext(ctx, `SELECT subject FROM identities WHERE provider=? AND account_id=? ORDER BY subject LIMIT 1`, ProviderGitHub, owner).Scan(&github)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(github, 10, 64)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT request_json FROM runs WHERE team=? AND dispatch='controller' AND status IN ('pending','running') AND trigger_source IN ('github_app','schedule') ORDER BY created_at DESC LIMIT 50`, t.team)
		if err != nil {
			return err
		}
		var seals []runspec.SealedRequest
		for rows.Next() {
			var body []byte
			var seal runspec.SealedRequest
			if err := rows.Scan(&body); err != nil {
				return errors.Join(err, rows.Close())
			}
			if err := json.Unmarshal(body, &seal); err != nil {
				return errors.Join(err, rows.Close())
			}
			seals = append(seals, seal)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, seal := range seals {
			repos, err := runReposTx(ctx, tx, t.team, seal.Request)
			if err != nil {
				return err
			}
			for _, repo := range repos {
				var checked int64
				err := tx.QueryRowContext(ctx, `SELECT checked_at FROM machine_repo_access WHERE team=? AND prefix=? AND repo=?`, t.team, prefix, repo).Scan(&checked)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if seen[repo] || checked > tx.now().Add(-limits.PersonalRepoAccess/2).UnixNano() {
					continue
				}
				seen[repo] = true
				out = append(out, PersonalRepoAccessNeed{OwnerGitHubID: id, Repository: repo})
			}
		}
		return nil
	})
	return out, err
}

// RecordPersonalRepoAccess stores the controller's server-side access checks for an approved personal machine.
func (t *Tenant) RecordPersonalRepoAccess(ctx context.Context, prefix string, access map[string]bool) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		for repo, allowed := range access {
			if _, err := tx.ExecContext(ctx, `INSERT INTO machine_repo_access(team,prefix,repo,allowed,checked_at) VALUES (?,?,?,?,?) ON CONFLICT(team,prefix,repo) DO UPDATE SET allowed=excluded.allowed,checked_at=excluded.checked_at`, t.team, prefix, strings.ToLower(repo), boolInt(allowed), tx.now().UnixNano()); err != nil {
				return err
			}
		}
		return nil
	})
}
