package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type matchingEnrollment struct{ profile match.Profile }

func (e *matchingEnrollment) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	return e.profile, e.profile.Capacity, nil
}

func TestStoreClaimMatching(t *testing.T) {
	for _, named := range []bool{false, true} {
		name := "queued"
		if named {
			name = "named"
		}
		t.Run(name, func(t *testing.T) {
			for _, scenario := range []string{"capacity", "name", "missing_labels", "named_claim", "metered_runner", "pool_label", "pool_toleration", "trigger_requirements"} {
				t.Run(scenario, func(t *testing.T) { checkStoreClaimMatching(t, named, scenario) })
			}
		})
	}
}

func checkStoreClaimMatching(t *testing.T, named bool, scenario string) {
	t.Helper()
	ctx := t.Context()
	enrollment := &matchingEnrollment{profile: match.Profile{Capacity: match.Resources{Cores: 8}}}
	s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies), fixturePolicies(store.Policies{ClaimProfiles: enrollment}))
	job := nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}
	labels := []string(nil)
	pools := []string(nil)
	switch scenario {
	case "capacity":
		job.Resources = &nodeproto.ResourceSpec{Cores: new(float64(4))}
		enrollment.profile.Capacity.Cores = 2
	case "name":
		job.Requires, labels = []string{"name=other"}, []string{"name=other"}
	case "missing_labels":
		job.Requires = []string{"gpu"}
	case "named_claim", "metered_runner":
		var err error
		enrollment.profile.Accept, err = match.ParseAccept([]string{"github.com/other/*"})
		if err != nil {
			t.Fatal(err)
		}
		job.Requires, labels = []string{"gpu"}, []string{"gpu"}
	case "pool_label":
		job.Requires, labels = []string{"pool=prod"}, []string{"pool=prod"}
		enrollment.profile.ReservedPools = []string{"prod"}
	case "pool_toleration":
		job.Requires, pools = []string{"pool=prod"}, []string{"prod"}
	}
	expectedProfile := enrollment.profile
	enrollment.profile = match.Profile{Capacity: match.Resources{Cores: 8}}
	tenant, plan, err := storePlanSealRequestWith(ctx, s, func(input *store.IntakeRequest, doc *nodeproto.PlanDoc) {
		doc.Jobs = []nodeproto.JobSpec{job}
	})
	if err != nil {
		t.Fatal(err)
	}
	enrollment.profile = expectedProfile
	runID := plan.RunID
	unit := store.UnitRef{Kind: capability.UnitNode, RunID: runID, NodeID: "job"}
	if scenario == "trigger_requirements" {
		request := plan.Seal.Request.Request
		request.Options.Requires.Set, request.Options.Requires.Value = true, []string{"gpu"}
		admitted, err := tenant.AdmitRun(ctx, store.IntakeRequest{Authority: plan.Seal.Request.AuthorityRef, Request: request})
		if err != nil {
			t.Fatal(err)
		}
		unit = store.UnitRef{Kind: capability.UnitTrigger, RunID: admitted.RunID}
	} else {
		if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: controllerwire.NodeRef{RunID: runID, NodeID: "job"}, Actor: plan.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
			t.Fatal(err)
		}
	}
	authority := plan.Seal.Request.AuthorityRef
	if scenario == "metered_runner" {
		operator, err := s.AsOperator().ProvisionCapability(ctx, storeNativeBootstrapSpec())
		if err != nil {
			t.Fatal(err)
		}
		authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
	}
	issued, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()), RegistrationID: "runner:" + string(tenant.Team()) + ":enrolled", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "enrolled"}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, ReservedPools: pools, Metered: scenario == "metered_runner"})
	if err != nil {
		t.Fatal(err)
	}
	demand := store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{unit.Kind}, Available: match.Resources{Cores: 8}, Labels: labels}
	if named {
		demand.Target = &unit
	}
	before := readMutationSnapshot(t, tenant, s, plan.RunID)
	_, refused := s.AsOperator().ClaimUnit(ctx, demand)
	assertMatchingRefusal(t, refused, named)
	if after := readMutationSnapshot(t, tenant, s, plan.RunID); !reflect.DeepEqual(before, after) {
		t.Fatal("mismatched claim mutated queue, events or billing")
	}
	switch scenario {
	case "capacity":
		enrollment.profile.Capacity.Cores = 8
	case "name":
		replacement, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()), RegistrationID: "runner:" + string(tenant.Team()) + ":other", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "other"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
		if err != nil {
			t.Fatal(err)
		}
		demand.CapabilityPrefix = replacement.Capability.Prefix
	case "missing_labels", "trigger_requirements":
		demand.Labels = []string{"gpu"}
	case "named_claim":
		enrollment.profile.Accept, err = match.ParseAccept([]string{"github.com/acme/*"})
		if err != nil {
			t.Fatal(err)
		}
		demand.Labels = nil
		_, refused = s.AsOperator().ClaimUnit(ctx, demand)
		assertMatchingRefusal(t, refused, named)
		demand.Labels = []string{"gpu"}
	case "pool_toleration":
		if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET allowed_pools=? WHERE team=? AND id=?`, []byte(`["prod"]`), tenant.Team(), plan.RunID); err != nil {
			t.Fatal(err)
		}
	case "metered_runner", "pool_label":
		return
	}
	claim, err := s.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		t.Fatal(err)
	}
	holder := "enrolled"
	if scenario == "name" {
		holder = "other"
	}
	if claim.Award.Holder != holder || claim.Team != tenant.Team() || claim.RunID != unit.RunID || claim.Kind != unit.Kind || len(claim.Bearer) == 0 {
		t.Fatalf("matched award lost enrolled identity: %+v", claim)
	}
}

func assertMatchingRefusal(t *testing.T, err error, named bool) {
	t.Helper()
	var denied *apierr.Error
	if named {
		if !errors.As(err, &denied) || denied.Code != apierr.Forbidden && denied.Code != apierr.LockHeld {
			t.Fatalf("named claim failed outside matching: %v", err)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("queued claim failed outside matching: %v", err)
	}
}

type mutationSnapshot struct {
	State                                   store.RunSnapshot
	Awards, Events, Effects, BusinessEvents int
}

func readMutationSnapshot(t *testing.T, tenant *store.Tenant, s *store.Store, runID string) mutationSnapshot {
	t.Helper()
	snapshot, err := tenant.ReadSnapshot(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.StoreNow = time.Time{}
	var awards, events, effects, businessEvents int
	if err := s.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM billing_awards`).Scan(&awards); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM run_effects`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM business_events`).Scan(&businessEvents); err != nil {
		t.Fatal(err)
	}
	return mutationSnapshot{snapshot, awards, events, effects, businessEvents}
}
