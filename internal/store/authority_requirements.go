package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// authorityRequirement records the membership and policy that justified a credential.
// Team-owned runners have no membership requirement.
type authorityRequirement struct {
	Account               string           `json:"account"`
	Epoch                 int64            `json:"epoch"`
	Grant                 capability.Grant `json:"grant,omitempty"`
	MembersCanAddMachines bool             `json:"members_can_add_machines,omitempty"`
}

func membershipEpochTx(ctx context.Context, tx *storeTx, team, account string) (int64, error) {
	var epoch int64
	err := tx.QueryRowContext(ctx, `SELECT epoch FROM membership_epochs WHERE team=? AND account_id=?`, team, account).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return epoch, err
}

func provisionRequirementTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) (*authorityRequirement, error) {
	account := ""
	if spec.Subject.Kind == capability.SubjectAccount {
		account = spec.Subject.ID
	}
	var p capability.Principal
	if spec.Personal && spec.Authority != nil {
		var err error
		p, err = resolveProvisionAuthorityTx(ctx, tx, *spec.Authority, spec.ConsoleProof)
		if err != nil {
			return nil, err
		}
		account = p.Capability.Subject.ID
	}
	if account == "" {
		return nil, nil
	}
	epoch, err := membershipEpochTx(ctx, tx, spec.Team, account)
	if err != nil {
		return nil, err
	}
	requirement := &authorityRequirement{Account: account, Epoch: epoch}
	if spec.Personal {
		requirement.Grant = capability.RunsWrite
		requirement.MembersCanAddMachines = true
		if len(spec.ReservedPools) != 0 {
			requirement.Grant = capability.TeamAdmin
		}
	}
	return requirement, nil
}

func checkAuthorityRequirementTx(ctx context.Context, tx *storeTx, team string, requirement authorityRequirement) error {
	var role Role
	var epoch int64
	err := tx.QueryRowContext(ctx, `SELECT role,COALESCE((SELECT epoch FROM membership_epochs WHERE team=m.team AND account_id=m.account_id),0) FROM memberships m WHERE team=? AND account_id=?`, team, requirement.Account).Scan(&role, &epoch)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!role.Valid() || epoch != requirement.Epoch || requirement.Grant != "" && !slices.Contains(ScopesForRole(role), string(requirement.Grant))) {
		return authorityError(apierr.Unauthenticated, "credential membership authority has ended")
	}
	if err != nil {
		return err
	}
	if requirement.MembersCanAddMachines {
		var allowed bool
		if err := tx.QueryRowContext(ctx, `SELECT members_can_add_machines FROM teams WHERE name=?`, team).Scan(&allowed); err != nil {
			return err
		}
		if !allowed && !slices.Contains(ScopesForRole(role), ScopeTeamAdmin) {
			return authorityError(apierr.Unauthenticated, "credential enrollment setting has ended")
		}
	}
	return nil
}

func memberIssuancePolicy(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec, c capability.Capability) (capability.Capability, error) {
	admin := slices.Contains(c.Grants, capability.TeamAdmin)
	maximum := 90 * 24 * time.Hour
	if admin {
		if !spec.AllowTeamAdmin {
			return c, authorityError(apierr.Forbidden, "team.admin requires explicit authorization and an expiry of at most 30 days")
		}
		maximum = 30 * 24 * time.Hour
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return capability.Capability{}, clockErr1
	}
	ceiling := clockTime1.Add(maximum)
	if c.ExpiresAt == nil {
		c.ExpiresAt = &ceiling
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return capability.Capability{}, clockErr2
	}
	if c.ExpiresAt.After(ceiling) || !c.ExpiresAt.After(clockTime2) {
		return c, authorityError(apierr.BadRequest, "member credential expiry exceeds its lifetime ceiling")
	}
	if tx.rotatingRoot != nil {
		c.Parent = tx.rotatingRoot.Parent
	} else {
		if spec.Authority == nil {
			return c, bindingRefusal()
		}
		p, err := resolveProvisionAuthorityTx(ctx, tx, *spec.Authority, spec.ConsoleProof)
		if err != nil {
			return c, err
		}
		if !p.DirectAccountSession() {
			return c, bindingRefusal()
		}
		parent := p.Authority
		c.Parent = &parent
	}
	return c, nil
}
