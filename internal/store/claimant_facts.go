package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

const githubHostedRunner = "github-hosted"

type claimantFacts struct {
	HostKind                                                            capability.HostKind
	Launcher, Personal, Shared, Cloud, Observable, PersistentKubernetes bool
	OwnerAccount, Tier, OIDCKind, ExecutorClass                         string
	// safety: FreshHost is a host no job of another run touched: a launcher or Cloud pod, whose warm node
	// serves only its own run, or a GitHub-hosted Actions runner. A self-hosted Actions runner is a long-lived
	// host like any other. Host names any other host.
	FreshHost bool
	Host      string
}

func claimantFactsTx(ctx context.Context, tx *storeTx, chain []authorityNode, request runspec.Request) (claimantFacts, error) {
	var facts claimantFacts
	if len(chain) == 0 {
		return facts, bindingRefusal()
	}
	leaf, root := chain[0].principal.Capability, chain[len(chain)-1].principal.Capability
	if host := leaf.Host; host != nil {
		facts.HostKind, facts.FreshHost, facts.OIDCKind = host.Kind, host.Fresh, host.RunnerKind
		facts.Personal = host.Kind == capability.HostPersonal || host.Kind == capability.HostHere && host.RunnerKind == tierPersonal
		facts.Shared = host.Kind == capability.HostSelfHosted
		facts.OwnerAccount, facts.Host = host.OwnerAccountID, host.MachineID
		facts.Cloud = host.Kind == capability.HostCloud && host.RunnerKind != tierActions
		facts.Launcher, facts.Observable = capability.Launcher(leaf), true
		facts.Tier, facts.ExecutorClass = tierSelfHosted, "runner"
		if leaf.Billing != nil {
			facts.ExecutorClass = leaf.Billing.ExecutorClass
		}
		switch host.Kind {
		case capability.HostPersonal:
			facts.Tier = tierPersonal
		case capability.HostHere:
			facts.Tier = tierHere
		case capability.HostCloud:
			facts.Tier = TierCloud
		}
		if facts.Personal && request.OriginMachine != "" && request.OriginMachine == host.MachineID {
			facts.Tier = tierHere
		}
		if host.RunnerKind == tierActions {
			facts.Tier = tierActions
		}
		return facts, nil
	}
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
		facts.Tier, facts.ExecutorClass = tierSelfHosted, "local"
	}
	facts.Host = root.Team + "/" + root.Prefix
	if root.Subject.Kind == capability.SubjectMachine && root.Parent == nil {
		record, err := tx.operator.loadCapabilityRecordTx(ctx, tx, root.Prefix)
		if err != nil {
			return facts, err
		}
		if record.Origin != nil && record.Origin.RegistrationID != "" {
			facts.Host = root.Team + "/" + record.Origin.RegistrationID
		}
		facts.PersistentKubernetes = record.Origin != nil && record.Origin.Platform == "kubernetes" && !capability.Launcher(root)
		facts.Personal = record.Origin != nil && record.Origin.Personal
		facts.Shared = record.Origin != nil && record.Origin.Kind == capability.ProvisionMachine && !facts.Personal
		if err := tx.QueryRowContext(ctx, `SELECT created_by FROM capabilities WHERE team=? AND prefix=?`, root.Team, root.Prefix).Scan(&facts.OwnerAccount); err != nil {
			return facts, err
		}
		facts.Tier = tierSelfHosted
		if facts.Personal {
			facts.Tier, facts.OIDCKind = tierPersonal, tierPersonal
			if request.OriginMachine == root.Subject.ID {
				facts.Tier = tierHere
			}
		}
	}
	githubHosted := false
	for _, ancestor := range chain {
		c := ancestor.principal.Capability
		facts.Cloud = facts.Cloud || capability.CloudLauncher(c)
		if c.Binding.Kind == capability.BindingPush {
			facts.PersistentKubernetes = false
			facts.Tier, facts.OIDCKind = tierActions, tierActions
			githubHosted = c.Binding.Push != nil && c.Binding.Push.RunnerEnvironment == githubHostedRunner
		}
	}
	facts.Cloud = facts.Cloud || leaf.Billing != nil && leaf.Billing.ExecutorClass == string(match.ClassCloud)
	if facts.Cloud {
		facts.Tier, facts.ExecutorClass = TierCloud, string(match.ClassCloud)
		// A Cloud pod claims with the deployment's launcher service credential,
		// which no team can mint; to a trust policy it is a runner pod like one
		// a team's own launcher starts.
		if facts.OIDCKind == "service" {
			facts.OIDCKind = "runner"
		}
	}
	for _, ancestor := range chain {
		if ancestor.principal.Capability.Purpose == capability.PurposeHere {
			facts.ExecutorClass = tierHere
		}
	}
	facts.HostKind = capability.HostSelfHosted
	if facts.Personal {
		facts.HostKind = capability.HostPersonal
	}
	if root.Subject.Kind == capability.SubjectLocal || facts.ExecutorClass == tierHere {
		facts.HostKind = capability.HostHere
	}
	if facts.Cloud || githubHosted {
		facts.HostKind = capability.HostCloud
	}
	facts.FreshHost = facts.Launcher || facts.Cloud || facts.Tier == tierActions && githubHosted
	return facts, nil
}
