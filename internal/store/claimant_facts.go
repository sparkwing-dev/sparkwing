package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type claimantFacts struct {
	Launcher, Personal, Shared, Cloud, Observable bool
	OwnerAccount, Tier, OIDCKind, ExecutorClass   string
}

func claimantFactsTx(ctx context.Context, tx *storeTx, chain []authorityNode, request runspec.Request) (claimantFacts, error) {
	var facts claimantFacts
	if len(chain) == 0 {
		return facts, bindingRefusal()
	}
	leaf, root := chain[0].principal.Capability, chain[len(chain)-1].principal.Capability
	facts.Launcher = capability.Launcher(leaf)
	facts.Observable = registeredClaimant(leaf) || actionsClaimant(leaf) || facts.Launcher || localClaimant(leaf)
	facts.ExecutorClass, facts.OIDCKind = "runner", "user"
	switch leaf.Subject.Kind {
	case capability.SubjectMachine:
		facts.OIDCKind = "runner"
	case capability.SubjectService, capability.SubjectOperator:
		facts.OIDCKind = "service"
	}
	if root.Subject.Kind == capability.SubjectLocal {
		facts.Tier, facts.ExecutorClass = TierSelfHosted, "local"
	}
	if root.Subject.Kind == capability.SubjectMachine && root.Parent == nil {
		record, err := loadCapabilityRecordTx(ctx, tx, root.Prefix)
		if err != nil {
			return facts, err
		}
		facts.Personal = record.Origin != nil && record.Origin.Personal
		facts.Shared = record.Origin != nil && record.Origin.Kind == capability.ProvisionMachine && !facts.Personal
		if err := tx.QueryRowContext(ctx, `SELECT created_by FROM capabilities WHERE team=? AND prefix=?`, root.Team, root.Prefix).Scan(&facts.OwnerAccount); err != nil {
			return facts, err
		}
		facts.Tier = TierSelfHosted
		if facts.Personal {
			facts.Tier, facts.OIDCKind = TierPersonal, TierPersonal
			if request.OriginMachine == root.Subject.ID {
				facts.Tier = TierHere
			}
		}
	}
	for _, ancestor := range chain {
		c := ancestor.principal.Capability
		facts.Cloud = facts.Cloud || capability.CloudLauncher(c)
		if c.Binding.Kind == capability.BindingPush {
			facts.Tier, facts.OIDCKind = TierActions, TierActions
		}
	}
	facts.Cloud = facts.Cloud || leaf.Billing != nil && leaf.Billing.ExecutorClass == string(match.ClassCloud)
	if facts.Cloud {
		facts.Tier, facts.ExecutorClass = TierCloud, string(match.ClassCloud)
	}
	for _, ancestor := range chain {
		if ancestor.principal.Capability.Purpose == capability.PurposeHere {
			facts.ExecutorClass = TierHere
		}
	}
	return facts, nil
}
