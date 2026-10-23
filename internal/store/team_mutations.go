package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func (t *Tenant) teamMutationTx(ctx context.Context, a capability.Authorized, operation, subject string, write func(*storeTx, capability.Principal) error) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		return t.teamMutationInTx(ctx, tx, a, operation, subject, write)
	})
}

func (t *Tenant) teamMutationInTx(ctx context.Context, tx *storeTx, a capability.Authorized, operation, subject string, write func(*storeTx, capability.Principal) error) error {
	if a.Target.RunID != "" || a.Target.NodeID != "" || a.ResultReplay || a.LogTailAttempt != 0 {
		return bindingRefusal()
	}
	if err := acquireLockSet(ctx, tx, func() lockSet {
		set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
		set.add(lockTeam, true, string(t.team))
		return set
	}); err != nil {
		return err
	}
	if err := t.credentialAdminTx(ctx, tx, a); err != nil {
		return err
	}
	p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
	if err != nil {
		return err
	}
	if err := write(tx, p); err != nil {
		return err
	}
	if err := t.credentialAdminTx(ctx, tx, a); err != nil {
		return err
	}
	return recordMutationAuditTx(ctx, tx, t.team, auditPrincipalActor(p), operation, subject)
}

func recordMutationAuditTx(ctx context.Context, tx *storeTx, team Team, actor auditActor, operation, subject string) error {
	return tx.operator.recordAudit(ctx, tx, auditContext{At: tx.now(ctx), Team: team, Actor: actor}, auditMutation{SubjectID: subject, Operation: auditOperation(operation)})
}

// RenameAuthorizedTeam changes the display name under current team administration.
func (t *Tenant) RenameAuthorizedTeam(ctx context.Context, a capability.Authorized, name string) (TeamInfo, error) {
	var out TeamInfo
	err := t.teamMutationTx(ctx, a, "RenameAuthorizedTeam", string(t.team), func(tx *storeTx, _ capability.Principal) error {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > maxDisplayNameLen {
			return fmt.Errorf("%w: a display name is 1 to %d characters", ErrInvalidInput, maxDisplayNameLen)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE teams SET display_name=?,updated_at=? WHERE name=?`, name, tx.now(ctx).UnixNano(), t.team); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT name,display_name,created_by FROM teams WHERE name=?`, t.team).Scan(&out.Slug, &out.DisplayName, &out.CreatedBy)
	})
	if err != nil {
		return TeamInfo{}, err
	}
	return out, nil
}

// SetAuthorizedBuildTrust commits this team's execution policy under current administration.
func (t *Tenant) SetAuthorizedBuildTrust(ctx context.Context, a capability.Authorized, trust bool) error {
	return t.teamMutationTx(ctx, a, "SetAuthorizedBuildTrust", string(t.team), func(tx *storeTx, _ capability.Principal) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO team_build_trust(team,trust_local_builds) VALUES (?,?) ON CONFLICT(team) DO UPDATE SET trust_local_builds=excluded.trust_local_builds`, t.team, boolInt(trust))
		return err
	})
}

// CreateAuthorizedInvitation creates or replays an invitation under current administration.
func (t *Tenant) CreateAuthorizedInvitation(ctx context.Context, a capability.Authorized, email string, role Role, key string) (Invitation, error) {
	var out Invitation
	identity, err := json.Marshal([]string{string(t.team), a.Principal.Capability.Subject.ID, key})
	if err != nil {
		return Invitation{}, err
	}
	sum := sha256.Sum256(identity)
	id := hex.EncodeToString(sum[:])
	err = t.teamMutationTx(ctx, a, "CreateAuthorizedInvitation", id, func(tx *storeTx, p capability.Principal) error {
		if p.Capability.Subject != a.Principal.Capability.Subject {
			return bindingRefusal()
		}
		email = NormalizeEmail(email)
		if !LooksLikeEmail(email) || !role.Valid() || strings.TrimSpace(key) == "" {
			return ErrInvalidInput
		}
		var created, expires int64
		err := tx.QueryRowContext(ctx, `SELECT id,team,email,role,invited_by,created_at,expires_at FROM invitations WHERE team=? AND id=?`, t.team, id).Scan(&out.ID, &out.Team, &out.Email, &out.Role, &out.InvitedBy, &created, &expires)
		if err == nil {
			if out.Email != email || out.Role != role {
				return authorityError(apierr.Conflict, "idempotency key names another invitation")
			}
			out.CreatedAt, out.ExpiresAt = time.Unix(0, created).UTC(), time.Unix(0, expires).UTC()
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out, err = t.createInvitationTx(ctx, tx, p.Capability.Subject.ID, email, role, id, tx.now(ctx))
		return err
	})
	if err != nil {
		return Invitation{}, err
	}
	return out, nil
}

// DeleteAuthorizedInvitation withdraws an open invitation under current administration.
func (t *Tenant) DeleteAuthorizedInvitation(ctx context.Context, a capability.Authorized, id string) error {
	return t.teamMutationTx(ctx, a, "DeleteAuthorizedInvitation", id, func(tx *storeTx, _ capability.Principal) error {
		result, err := tx.ExecContext(ctx, `UPDATE invitations SET withdrawn_at=? WHERE team=? AND id=? AND accepted_at IS NULL AND withdrawn_at IS NULL`, tx.now(ctx).UnixNano(), t.team, id)
		return credentialRowChanged(result, err)
	})
}

// DeleteAuthorizedTeam closes the current owner's team after its deletion guards pass.
func (t *Tenant) DeleteAuthorizedTeam(ctx context.Context, a capability.Authorized) (TeamDeletion, error) {
	var out TeamDeletion
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if a.Target.RunID != "" || a.Target.NodeID != "" || a.ResultReplay || a.LogTailAttempt != 0 {
			return bindingRefusal()
		}
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			return discoverTeamDeletionLocks(ctx, tx, set, t.team)
		}); err != nil {
			return err
		}
		if err := t.credentialAdminTx(ctx, tx, a); err != nil {
			return err
		}
		p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
		if err != nil {
			return err
		}
		role, err := t.roleTx(ctx, tx, p.Capability.Subject.ID)
		if err != nil {
			return err
		}
		if role != RoleOwner {
			return ErrRoleAboveOwn
		}
		hasOther, err := tx.operator.accountHasOtherTeamTx(ctx, tx, p.Capability.Subject.ID, string(t.team))
		if err != nil {
			return err
		}
		if !hasOther {
			return ErrOnlyTeam
		}
		if _, err := t.markForDeletionTx(ctx, tx, p.Capability.Subject.ID, tx.now(ctx)); err != nil {
			return err
		}
		out, err = teamDeletionTx(ctx, tx, t.team)
		if err != nil {
			return err
		}
		return recordMutationAuditTx(ctx, tx, t.team, auditPrincipalActor(p), "DeleteAuthorizedTeam", string(t.team))
	})
	if err != nil {
		return TeamDeletion{}, err
	}
	return out, nil
}

func (op *Operator) accountHasOtherTeamTx(ctx context.Context, tx *storeTx, accountID, excludedTeam string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships WHERE account_id=? AND team<>?`, accountID, excludedTeam).Scan(&count)
	return count > 0, err
}

// DeleteAuthorizedRun removes a finished run under current control authority.
func (t *Tenant) DeleteAuthorizedRun(ctx context.Context, a capability.Authorized) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if a.Target.Team != string(t.team) || a.Target.RunID == "" || a.Target.NodeID != "" || a.ResultReplay || a.LogTailAttempt != 0 {
			return bindingRefusal()
		}
		p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
		if err != nil {
			return err
		}
		route, ok := capability.LookupRoute("DELETE", "/api/v1/runs/{run}")
		if !ok || len(route.Access) != 1 || !reflect.DeepEqual(a.Access, route.Access[0]) {
			return bindingRefusal()
		}
		target := a.Target
		target.RunID = ""
		if _, err := capability.Authorize(ctx, tx, route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, target); err != nil {
			return err
		}
		if err := lockStateRunTx(ctx, tx, t.team, a.Target.RunID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		run, err := readRunTx(ctx, tx, t.team, a.Target.RunID)
		if err != nil {
			return err
		}
		role, err := t.roleTx(ctx, tx, p.Capability.Subject.ID)
		if err != nil {
			return err
		}
		if role != RoleOwner {
			var creator, kind string
			if err := tx.QueryRowContext(ctx, `SELECT created_principal,created_subject_kind FROM runs WHERE team=? AND id=?`, t.team, run.ID).Scan(&creator, &kind); err != nil {
				return err
			}
			if role != RoleEditor || kind != string(capability.SubjectAccount) || creator != p.Capability.Subject.ID {
				return bindingRefusal()
			}
		}
		if run.FinishedAt == nil {
			return authorityError(apierr.Conflict, "run is still active")
		}
		var held int
		if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN
 EXISTS(SELECT 1 FROM nodes WHERE team=? AND run_id=? AND claimed_by IS NOT NULL) OR
 EXISTS(SELECT 1 FROM triggers WHERE team=? AND id=? AND claimed_by IS NOT NULL) OR
 EXISTS(SELECT 1 FROM eval_units WHERE team=? AND run_id=? AND claimed_by IS NOT NULL) THEN 1 ELSE 0 END`, t.team, a.Target.RunID, t.team, a.Target.RunID, t.team, a.Target.RunID).Scan(&held); err != nil {
			return err
		}
		if held != 0 {
			return authorityError(apierr.Conflict, "run still has claimed work")
		}
		if _, err := tx.ResolveAuthority(ctx, a.Principal.Authority); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_success_pins WHERE team=? AND run_id=?`, t.team, a.Target.RunID); err != nil {
			return err
		}
		if err := tx.operator.recordAudit(ctx, tx, auditContext{At: tx.now(ctx), Team: t.team, Actor: auditPrincipalActor(p)}, auditRunDeletion{RunID: a.Target.RunID}); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM runs WHERE team=? AND id=?`, t.team, a.Target.RunID)
		return err
	})
}
