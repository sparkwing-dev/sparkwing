package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// CheckBinding checks committed resource relations against current authority in one snapshot.
// Mutations must repeat the relation in their committing transaction.
func (o *Operator) CheckBinding(ctx context.Context, principal capability.Principal, rule capability.BindingRule, target capability.Target) error {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := tx.CheckBinding(ctx, principal, rule, target); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckBinding implements relational authorization within the transaction owning a write.
func (tx *storeTx) CheckBinding(ctx context.Context, principal capability.Principal, rule capability.BindingRule, target capability.Target) error {
	current, err := resolveProvisionAuthorityTx(ctx, tx, principal.Authority, capability.ProofForConsole(principal))
	if err != nil {
		return err
	}
	if principal.Class == capability.CredentialPeer {
		if current.Capability.Subject != principal.Capability.Subject || current.Capability.Parent != nil || current.Capability.Binding != (capability.Binding{Kind: capability.BindingNone}) || current.Capability.Audience != capability.Controller || current.Capability.Team != string(DefaultTeam) {
			return bindingRefusal()
		}
		if principal.Capability.Subject.Kind != capability.SubjectLocal || principal.Capability.Subject.ID == "" || principal.Capability.Team != string(DefaultTeam) || target.Team != "" && target.Team != string(DefaultTeam) {
			return bindingRefusal()
		}
	}
	c := current.Capability
	deployment := rule == capability.BindNone && target.RunID == "" && target.NodeID == "" && target.Team == "" || rule == capability.BindOperatorSettings && target.RunID == "" && target.NodeID == "" && (capability.DeploymentAuthority(current) || current.Class == capability.CredentialConsole) || rule == capability.BindClaimSelection && launcherClaimant(c)
	if c.Audience != capability.Controller || c.Team == "" && !deployment || c.Team != "" && target.Team != "" && c.Team != target.Team {
		return bindingRefusal()
	}
	if target.RunID != "" && rule != capability.BindClaimSelection {
		if err := bindingRunExistsTx(ctx, tx, c.Team, target.RunID); err != nil {
			return err
		}
	}
	err = checkRelationTx(ctx, tx, current, rule, target)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return bindingRefusal()
	}
	return err
}

func bindingRefusal() error {
	return apierr.Authorization(apierr.ReasonBinding, "resource is outside the authorized binding")
}

func bindingRunExistsTx(ctx context.Context, tx *storeTx, team, run string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE team=? AND id=?`, team, run).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return bindingRefusal()
	}
	return err
}

func checkRelationTx(ctx context.Context, tx *storeTx, p capability.Principal, rule capability.BindingRule, target capability.Target) error {
	c, b := p.Capability, p.Capability.Binding
	allowed := false
	switch rule {
	case capability.BindNone:
		allowed = true
	case capability.BindOperatorSettings:
		allowed = c.Team == "" && b.Kind == capability.BindingNone && target.RunID == "" && target.NodeID == "" && (capability.DeploymentAuthority(p) || p.Class == capability.CredentialConsole)
	case capability.BindRun:
		allowed = b.RunID != "" && b.RunID == target.RunID
	case capability.BindOwnNode:
		allowed = b.Kind == capability.BindingNode && b.Expansion == nil && b.RunID == target.RunID && b.NodeID != "" && b.NodeID == target.NodeID
	case capability.BindRunOfTrigger:
		allowed = b.Kind == capability.BindingTrigger && c.Unit == capability.UnitTrigger && b.RunID != "" && b.RunID == target.RunID
		if allowed && target.NodeID != "" {
			_, err := readNodeTx(ctx, tx, Team(c.Team), runspec.NodeRef{RunID: runspec.RunID(target.RunID), NodeID: runspec.NodeID(target.NodeID)})
			if errors.Is(err, ErrNotFound) {
				return bindingRefusal()
			}
			return err
		}
	case capability.BindPlanOwner:
		allowed = b.RunID != "" && b.RunID == target.RunID && (b.Kind == capability.BindingTrigger && c.Unit == capability.UnitTrigger || b.Kind == capability.BindingRun && c.Unit == capability.UnitPlan)
	case capability.BindBodyNamesRun:
		return bindingBodyRunTx(ctx, tx, c, target)
	case capability.BindPipelineOfBoundRun:
		if b.RunID == "" || target.Pipeline == "" {
			return bindingRefusal()
		}
		var pipeline string
		if err := tx.QueryRowContext(ctx, `SELECT pipeline FROM runs WHERE team=? AND id=?`, c.Team, b.RunID).Scan(&pipeline); err != nil {
			return err
		}
		allowed = pipeline == target.Pipeline
	case capability.BindChildOfBoundNode:
		return bindingChildTx(ctx, tx, c, target)
	case capability.BindClaimSelection:
		return bindingSelectionTx(ctx, tx, c, target)
	case capability.BindOwnClaim, capability.BindOwnEval:
		return bindingClaim(c, rule, target)
	case capability.BindSelfOrTeamAdmin:
		allowed = target.AccountID != "" && (slices.Contains(c.Grants, capability.TeamAdmin) || c.Subject.Kind == capability.SubjectAccount && c.Subject.ID == target.AccountID)
	case capability.BindOwnToken:
		return bindingTokenTx(ctx, tx, c, target)
	case capability.BindExpansion:
		return bindingExpansionTx(ctx, tx, c, target)
	case capability.BindSourceReport:
		return bindingSourceReportTx(ctx, tx, c, target)
	case capability.BindReadSet, capability.BindData, capability.BindSourceBundle:
		if target.Data != nil {
			var checker capability.DataAccessChecker = tx
			return checker.CheckDataAccess(ctx, capability.Authorized{Principal: p, Binding: b, Target: target}, *target.Data)
		}
		if rule == capability.BindReadSet {
			return bindingDeclaredReadTx(ctx, tx, c, target)
		}
		if rule == capability.BindData && c.Data != nil && c.Data.Use == capability.DataPublish && target.RunID == c.Data.RunID {
			return checkPublisherTx(ctx, tx, Team(c.Team), runstate.Actor{Kind: runstate.ActorOperator, Authority: p.Authority, CapabilityPrefix: c.Prefix}, target.RunID)
		}
	case capability.BindSource:
		return bindingSourceTx(ctx, tx, c, target)
	case capability.BindDeclaredSecret:
		return bindingSecretTx(ctx, tx, c, target)
	case capability.BindSlotOfBoundNode:
		return bindingSlotTx(ctx, tx, c, target)
	}
	if !allowed {
		return bindingRefusal()
	}
	return nil
}

func bindingBodyRunTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	if b.Kind == capability.BindingNone {
		if target.BodyRunID == "" {
			return nil
		}
		return bindingRunExistsTx(ctx, tx, c.Team, target.BodyRunID)
	}
	if b.Kind != capability.BindingNode || b.Expansion != nil || b.RunID == "" || b.NodeID == "" || target.BodyRunID != b.RunID || target.RunID != "" && target.RunID != b.RunID {
		return bindingRefusal()
	}
	return nil
}

func bindingChildTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	if b.Kind != capability.BindingNode || b.Expansion != nil || b.RunID == "" || b.NodeID == "" || b.Generation <= 0 || target.RunID != b.RunID || target.ChildRunID == "" {
		return bindingRefusal()
	}
	child, err := readRunTx(ctx, tx, Team(c.Team), target.ChildRunID)
	if err != nil {
		return err
	}
	lineage := child.Request.Request.Lineage
	if lineage.ParentRunID != b.RunID || lineage.ParentJobID != b.NodeID || child.Request.AuthorityRef.Kind != capability.AuthorityCapability {
		return bindingRefusal()
	}
	issuer, err := loadCapabilityTx(ctx, tx, child.Request.AuthorityRef.ID)
	if err != nil {
		return err
	}
	parent := issuer.Binding
	if issuer.Team != c.Team || parent.Kind != capability.BindingNode || parent.RunID != b.RunID || parent.NodeID != b.NodeID || parent.Holder != b.Holder || parent.Generation != b.Generation {
		return bindingRefusal()
	}
	return nil
}

func bindingSelectionTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	demand := UnitDemand{CapabilityPrefix: c.Prefix, Kinds: target.ClaimKinds}
	if target.RunID != "" {
		kind := capability.UnitTrigger
		if target.NodeID != "" {
			kind = capability.UnitNode
		} else if slices.Contains(target.ClaimKinds, capability.UnitPlan) && !slices.Contains(target.ClaimKinds, capability.UnitTrigger) {
			kind = capability.UnitPlan
		}
		demand.Target = &UnitRef{Kind: kind, RunID: runspec.RunID(target.RunID), NodeID: runspec.NodeID(target.NodeID)}
	}
	if _, err := claimKinds(c, demand); err != nil {
		return err
	}
	if demand.Target != nil {
		_, _, err := readMatchingInputsTx(ctx, tx, demand, *demand.Target, false)
		if err != nil {
			return err
		}
		if c.Binding.Kind != capability.BindingNone && (c.Binding.Kind != capability.BindingTrigger || c.Binding.RunID != target.RunID) {
			return bindingRefusal()
		}
		// safety: claim admission evaluates observed labels and availability in its awarding transaction.
		return nil
	}
	return nil
}

func bindingClaim(c capability.Capability, rule capability.BindingRule, target capability.Target) error {
	b := c.Binding
	generation, holder := b.Generation, b.Holder
	if c.Unit == capability.UnitEval && b.Expansion != nil {
		generation, holder = b.Expansion.Generation, b.Expansion.Holder
	}
	if c.Unit == "" || c.UnitID == "" || target.ClaimID != c.UnitID {
		return bindingRefusal()
	}
	if target.RunID != "" && target.RunID != b.RunID || target.Unit != "" && target.Unit != c.Unit || target.UnitID != "" && target.UnitID != c.UnitID {
		return bindingRefusal()
	}
	if target.UnitGeneration != 0 && target.UnitGeneration != generation || target.UnitHolder != "" && target.UnitHolder != holder {
		return bindingRefusal()
	}
	if rule == capability.BindOwnEval && (c.Unit != capability.UnitEval || b.Expansion == nil || target.UnitSourceNodeID != b.NodeID || target.UnitSourceGeneration != b.Generation) {
		return bindingRefusal()
	}
	return nil
}

func bindingTokenTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	if target.TokenPrefix == "" {
		return bindingRefusal()
	}
	_, metadata, err := capabilityMetadataTx(ctx, tx, target.TokenPrefix)
	if err != nil {
		return err
	}
	if !capability.OwnsToken(capability.Principal{Capability: c, Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}}, metadata) {
		return bindingRefusal()
	}

	return nil
}

var (
	_ capability.BindingChecker = (*Operator)(nil)
	_ capability.BindingChecker = (*storeTx)(nil)
)
