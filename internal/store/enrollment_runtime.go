package store

import (
	"context"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// ValidRunnerName checks the enrolled machine-name grammar.
func ValidRunnerName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func machineAdministrationTx(ctx context.Context, tx *storeTx, p capability.Principal, c capability.Capability) error {
	if c.Subject.Kind != capability.SubjectMachine || c.Binding.Kind != capability.BindingNone || c.Unit != "" || slices.Contains(p.Capability.Grants, capability.TeamAdmin) {
		return nil
	}
	facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: capability.Principal{Capability: c}}}, runspec.Request{})
	if err != nil {
		return err
	}
	if !facts.Personal {
		return authorityError(apierr.Forbidden, "shared runner management requires team administration")
	}
	return nil
}

func authorizeEnrollmentTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) error {
	allowed := []capability.Grant{capability.ClaimsLaunch, capability.OperatorLimits, capability.OperatorObjectStore, capability.OperatorTokens}
	if spec.Kind == capability.ProvisionMachine {
		if !ValidRunnerName(spec.Subject.ID) || spec.RegistrationID != "runner:"+spec.Team+":"+spec.Subject.ID || spec.Data != nil || len(spec.Repositories) == 0 || len(spec.Repositories) > 32 {
			return bindingRefusal()
		}
		allow, err := sourceurl.ParseRepoAllowlist(spec.Repositories)
		if err != nil || !slices.Equal(allow.Patterns(), spec.Repositories) {
			return authorityError(apierr.BadRequest, "enrollment requires canonical repository patterns")
		}
		if err := (&Tenant{s: tx.store, team: Team(spec.Team)}).lockTeamTx(ctx, tx); err != nil {
			return err
		}
		allowed = []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.ClaimsLaunch}
		if slices.Contains(spec.Grants, capability.ClaimsLaunch) && (spec.Personal || len(spec.ReservedPools) == 0 || len(spec.Grants) != 1 || spec.Metered) {
			return bindingRefusal()
		}
	} else {
		if spec.RegistrationID != "service:"+spec.Subject.ID || strings.TrimSpace(spec.Subject.ID) != spec.Subject.ID {
			return bindingRefusal()
		}
		if err := advisoryLockTx(ctx, tx, "service-enrollment"); err != nil {
			return err
		}
	}
	if len(spec.Grants) == 0 {
		return bindingRefusal()
	}
	seen := map[capability.Grant]bool{}
	for _, grant := range spec.Grants {
		if seen[grant] || !slices.Contains(allowed, grant) {
			return bindingRefusal()
		}
		seen[grant] = true
	}
	pools := map[string]bool{}
	for _, pool := range spec.ReservedPools {
		if strings.TrimSpace(pool) == "" || pools[pool] {
			return bindingRefusal()
		}
		pools[pool] = true
	}
	return checkEnrollmentNamesTx(ctx, tx, spec)
}

func checkEnrollmentNamesTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) error {
	rows, err := tx.QueryContext(ctx, `SELECT prefix,subject_id,revoked_at FROM capabilities WHERE COALESCE(team,'')=? AND subject_kind=? AND audience='controller' AND bind_kind='none' AND unit_kind IS NULL AND parent_id IS NULL AND `+liveCredentialSQL("")+``, spec.Team, spec.Subject.Kind, tx.now().UnixNano(), tx.now().UnixNano())
	if err != nil {
		return err
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var prefix, name string
		var revokes *int64
		if err := rows.Scan(&prefix, &name, &revokes); err != nil {
			return err
		}
		names[name] = true
		if name == spec.Subject.ID && (tx.rotatingRoot == nil || prefix != tx.rotatingRoot.Prefix && revokes == nil) {
			return ErrAgentNameTaken
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if spec.Kind == capability.ProvisionMachine && tx.rotatingRoot == nil && len(names) >= MaxRunnerTokensPerTeam {
		return ErrRunnerTokenLimit
	}
	return nil
}

func enrolledClaimProfileTx(ctx context.Context, tx *storeTx, chain []authorityNode) (match.Profile, error) {
	for _, ancestor := range chain {
		if ancestor.principal.Capability.Purpose == capability.PurposeHere {
			return match.Profile{}, nil
		}
	}
	root := chain[len(chain)-1].principal
	if root.Authority.Kind != capability.AuthorityCapability {
		return match.Profile{}, authorityError(apierr.Unavailable, "claim enrollment is unavailable")
	}
	record, err := loadCapabilityRecordTx(ctx, tx, root.Authority.ID)
	if err != nil {
		return match.Profile{}, err
	}
	if record.Origin == nil {
		return match.Profile{}, authorityError(apierr.Unavailable, "claim registration is unavailable")
	}
	var profile match.Profile
	switch record.Origin.Kind {
	case capability.ProvisionMachine:
		profile.Accept, err = match.ParseAccept(root.Capability.Repositories)
	case capability.ProvisionService:
		if !launcherClaimant(chain[0].principal.Capability) {
			return profile, bindingRefusal()
		}
	case capability.ProvisionActions:
	default:
		return profile, bindingRefusal()
	}
	return profile, err
}
