package store_test

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestClaimUnitLocksControllerCapacityBeforeBilling(t *testing.T) {
	for _, metered := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "metered"}[metered], func(t *testing.T) {
			ctx := t.Context()
			s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
			configure := func(input *store.IntakeRequest, doc *nodeproto.PlanDoc) {
				input.Request.Dispatch = runspec.DispatchController
				doc.Concurrency = []nodeproto.ConcurrencySpec{admissionGroup(1, 1)}
			}
			tenant, first, err := storePlanSealRequestWith(ctx, s, configure)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.AcceptPlan(ctx, first); err != nil {
				t.Fatal(err)
			}
			authority := first.Seal.Request.AuthorityRef
			if metered {
				operator, err := acceptanceBootstrap(ctx, s)
				if err != nil {
					t.Fatal(err)
				}
				authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
			}
			runner, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()), RegistrationID: "runner:" + string(tenant.Team()) + ":desktop", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "desktop"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, Metered: metered})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: runner.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(first.RunID), NodeID: "alpha"}})
			if err != nil {
				t.Fatal(err)
			}
			actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(first.RunID), NodeID: "alpha", Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
			admission := runstate.CapacityBinding{RunID: first.RunID, NodeID: "alpha", Generation: int64(actor.Generation)}
			if got, err := tenant.MayStart(ctx, admission, actor); err != nil || got.Kind != runstate.AcquireGranted {
				t.Fatalf("first job admission: %+v %v", got, err)
			}
			admissionFinishJob(t, &storeRecoveryFixture{tenant: tenant}, admission, actor)
			_, second, err := storePlanSealRequestWith(ctx, s, configure)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.AcceptPlan(ctx, second); err != nil {
				t.Fatal(err)
			}
			before, err := tenant.GetConcurrencyState(ctx, "plan:global:key")
			if err != nil || len(before.Holders) != 1 || before.Holders[0].RunID != first.RunID || len(before.Waiters) != 1 || before.Waiters[0].RunID != second.RunID {
				t.Fatalf("plan holder and waiter: %+v %v", before, err)
			}
			next := runspec.NodeRef{RunID: runspec.RunID(first.RunID), NodeID: "beta"}
			ready, err := tenant.ReadNode(ctx, next)
			if err != nil || ready.Status != runstate.StatusPending || ready.ReadyAt == nil {
				t.Fatalf("next job ready: %+v %v", ready, err)
			}
			claim, err = s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: runner.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}})
			if err != nil || claim.RunID != first.RunID || claim.NodeID != "beta" {
				t.Fatalf("claim ready job with plan waiter: %+v %v", claim, err)
			}
			after, err := tenant.GetConcurrencyState(ctx, "plan:global:key")
			if err == nil && len(after.Holders) == 1 && !after.Holders[0].LeaseExpiresAt.Before(before.Holders[0].LeaseExpiresAt) {
				before.Holders[0].LeaseExpiresAt = after.Holders[0].LeaseExpiresAt
			}
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("claim changed plan ordering: before=%+v after=%+v %v", before, after, err)
			}
			var billed bool
			var executor string
			var reserved int64
			if err := s.DB().QueryRowContext(ctx, storetest.Rebind(s, "SELECT metered,executor_class,(SELECT COALESCE(SUM(amount_micro),0) FROM credit_charges c WHERE c.team=b.team AND c.capability_prefix=b.capability_prefix AND c.kind='reservation') FROM billing_awards b WHERE capability_prefix=?"), claim.Award.CapabilityPrefix).Scan(&billed, &executor, &reserved); err != nil || billed != metered || executor == "" || metered && reserved <= 0 || !metered && reserved != 0 {
				t.Fatalf("claim accounting: metered=%t executor=%s %v", billed, executor, err)
			}
		})
	}
}
