package store

import (
	"context"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func operatorSettingsWrite(ctx context.Context, o *Operator, a capability.Authorized, grant capability.Grant, write func(*storeTx, capability.Principal) error) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			if a.Target.Team != "" {
				set.add(lockTeam, true, a.Target.Team)
			}
			return set
		}); err != nil {
			return err
		}
		check := func() (capability.Principal, error) {
			p, err := resolveProvisionAuthorityTx(ctx, tx, a.Principal.Authority, capability.ProofForConsole(a.Principal))
			if err != nil {
				return p, err
			}
			if !(capability.DeploymentAuthority(p) && slices.Contains(p.Capability.Grants, grant) || p.Class == capability.CredentialConsole && slices.Contains(p.Capability.Grants, capability.OperatorConsole)) {
				return p, bindingRefusal()
			}
			return p, nil
		}
		p, err := check()
		if err != nil {
			return err
		}
		if err := write(tx, p); err != nil {
			return err
		}
		_, err = check()
		return err
	})
}

// WriteComputeLimits commits a validated batch under current deployment authority.
func (o *Operator) WriteComputeLimits(ctx context.Context, a capability.Authorized, updates map[string]int64) (ComputeLimits, error) {
	var out ComputeLimits
	err := operatorSettingsWrite(ctx, o, a, capability.OperatorLimits, func(tx *storeTx, _ capability.Principal) error {
		var err error
		out, err = setComputeLimitsTx(ctx, tx, updates)
		return err
	})
	return out, err
}

// WriteSignUpSettings commits supplied fields and operator provenance atomically.
func (o *Operator) WriteSignUpSettings(ctx context.Context, a capability.Authorized, req SignUpSettingsChange) error {
	return operatorSettingsWrite(ctx, o, a, capability.OperatorLimits, func(tx *storeTx, p capability.Principal) error {
		g, err := lockSignUpGateTx(ctx, tx)
		if err != nil {
			return err
		}
		if req.Mode == nil && !req.LimitsChanged() {
			return ErrInvalidInput
		}
		if req.Mode != nil {
			mode, err := ParseSignUpMode(*req.Mode)
			if err != nil {
				return err
			}
			g.Mode, g.Source, g.Reason, g.SetBy, g.UpdatedAt = mode, WaitlistReasonOperator, strings.TrimSpace(req.Reason), p.Capability.Subject.ID, tx.time
		}
		for _, field := range []struct{ supplied, stored *int }{
			{req.HourlyLimit, &g.Limits.HourlyLimit},
			{req.DailyLimit, &g.Limits.DailyLimit},
			{req.HourlyWarn, &g.Limits.HourlyWarn},
			{req.GitHubMinAccountDays, &g.Limits.GitHubMinAccountDays},
			{req.FreeTeamMembers, &g.Limits.FreeTeamMembers},
		} {
			if field.supplied != nil {
				if *field.supplied < 0 {
					return ErrInvalidInput
				}
				*field.stored = *field.supplied
			}
		}
		return writeSignUpGateTx(ctx, tx, g)
	})
}

// WriteStorageSettings replaces deployment storage policy under current authority.
func (o *Operator) WriteStorageSettings(ctx context.Context, a capability.Authorized, in StorageSettings) error {
	return operatorSettingsWrite(ctx, o, a, capability.OperatorObjectStore, func(tx *storeTx, _ capability.Principal) error {
		return setStorageSettingsTx(ctx, tx, in)
	})
}

func operatorStorageTeam(ctx context.Context, tx *storeTx, a capability.Authorized) (Team, error) {
	team := Team(a.Target.Team)
	if team == "" || NormalizeTeam(team) != team {
		return "", authorityError(apierr.BadRequest, "team is required")
	}
	if err := (&Tenant{s: tx.store, team: team}).lockTeamTx(ctx, tx); err != nil {
		return "", err
	}
	return team, nil
}

// WriteStorageQuota replaces the selected team's write quota while preserving its allowance.
func (o *Operator) WriteStorageQuota(ctx context.Context, a capability.Authorized, in StorageQuota) (StorageQuota, error) {
	var out StorageQuota
	err := operatorSettingsWrite(ctx, o, a, capability.OperatorObjectStore, func(tx *storeTx, _ capability.Principal) error {
		team, err := operatorStorageTeam(ctx, tx, a)
		if err != nil {
			return err
		}
		if err := setStorageQuotaTx(ctx, tx, team, in); err != nil {
			return err
		}
		out, err = storageQuotaForTeamTx(ctx, tx, team)
		return err
	})
	return out, err
}

// WriteStorageAllowance changes only the selected team's retained-byte allowance.
func (o *Operator) WriteStorageAllowance(ctx context.Context, a capability.Authorized, bytes int64) (StorageQuota, error) {
	var out StorageQuota
	err := operatorSettingsWrite(ctx, o, a, capability.OperatorObjectStore, func(tx *storeTx, _ capability.Principal) error {
		team, err := operatorStorageTeam(ctx, tx, a)
		if err != nil {
			return err
		}
		out, err = setStorageAllowanceTx(ctx, tx, team, bytes)
		return err
	})
	return out, err
}
