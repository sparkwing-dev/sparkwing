package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func checkStoreLauncherSync(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	tenant, plan, err := storePlanSealRequest(ctx, s)
	if err != nil {
		return err
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		return err
	}
	if _, err := tenant.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100000000, Reference: "launcher-fixture", CreatedBy: "fixture"}); err != nil {
		return err
	}
	runID := plan.RunID
	if _, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: controllerwire.NodeRef{RunID: runID, NodeID: "alpha"}, Actor: plan.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
		return err
	}
	operator, err := s.AsOperator().ProvisionCapability(ctx, storeNativeBootstrapSpec())
	if err != nil {
		return err
	}
	operatorRef := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
	until := time.Now().UTC().Add(time.Hour)
	launcher, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "service:launcher", Authority: &operatorRef, Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}, ExpiresAt: &until})
	if err != nil {
		return err
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: launcher.Capability.Prefix}
	reopen := func() error {
		if err := s.Close(); err != nil {
			return err
		}
		var err error
		s, err = target.TryOpen(store.WithPolicies(sealStorePolicies))
		if err != nil {
			return err
		}
		tenant, err = s.ForTeam(ctx, store.Team(plan.Seal.Request.Request.Team))
		return err
	}
	a := capability.Authorized{Principal: capability.Principal{Authority: ref}}
	demand := store.UnitDemand{CapabilityPrefix: launcher.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runID, NodeID: "alpha"}, Available: match.Resources{Cores: 8}}
	first, err := s.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		return fmt.Errorf("initial launcher claim: %w", err)
	}
	p, err := s.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: first.Award.CapabilityPrefix})
	if err != nil {
		return err
	}
	old := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: p.Capability.UnitID, Holder: first.Award.Holder, Generation: first.Award.Generation}}
	for exchange := range 2 {
		if err := reopen(); err != nil {
			return err
		}
		response, err := (controller.NewEngineController(s, controller.EngineController{})).SyncLauncher(ctx, a, controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{old}})
		if err != nil {
			return err
		}
		if len(response.Awards) != 1 || len(response.Retired) != 0 || response.Awards[0].Capability != "" || response.Awards[0].UnitID != old.Award.ID || response.Awards[0].Binding.Generation != first.Award.Generation || response.Awards[0].Delivery != capability.DeliveryBootstrap {
			return errors.New("reopening or exchange lost the immutable launcher award")
		}
		if exchange == 0 {
			issued, err := s.AsOperator().ExchangeCapability(ctx, capability.ExchangeSpec{BootstrapPrefix: first.Award.CapabilityPrefix})
			if err != nil {
				return err
			}
			first.Award.CapabilityPrefix = issued.Capability.Prefix
		}
	}
	if err := s.ReleaseUnit(ctx, first, first.Award.CapabilityPrefix); err != nil {
		return fmt.Errorf("release exchanged claim: %w", err)
	}
	if _, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: controllerwire.NodeRef{RunID: runID, NodeID: "alpha"}, Actor: plan.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
		return fmt.Errorf("owner redispatch: %w", err)
	}
	second, err := s.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		return fmt.Errorf("claim replacement: %w", err)
	}
	current := old
	current.Award.Generation = second.Award.Generation

	if err := reopen(); err != nil {
		return err
	}
	response, err := (controller.NewEngineController(s, controller.EngineController{})).SyncLauncher(ctx, a, controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{old, current}})
	if err != nil {
		return err
	}
	if len(response.Awards) != 1 || response.Awards[0].Capability != "" || response.Awards[0].Binding.Generation != second.Award.Generation || len(response.Retired) != 1 || response.Retired[0] != old.Award {
		return errors.New("stale Job cleanup selected the replacement award")
	}
	row, err := tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runID, NodeID: "alpha"})
	if err != nil {
		return err
	}
	if row.Generation != second.Award.Generation || row.Attempt != 0 || row.CapabilityPrefix != second.Award.CapabilityPrefix {
		return errors.New("launcher synchronization changed execution state")
	}
	return nil
}

func TestStoreLauncherSync(t *testing.T) {
	if err := checkStoreLauncherSync(t); err != nil {
		t.Fatal(err)
	}
}
