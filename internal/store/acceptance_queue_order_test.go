package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestStoreQueueOrder(t *testing.T) {
	for _, preferred := range []bool{false, true} {
		name := "fifo"
		if preferred {
			name = "preferred"
		}
		t.Run(name, func(t *testing.T) { checkStoreQueueOrder(t, preferred) })
	}
}

func checkStoreQueueOrder(t *testing.T, preferred bool) {
	t.Helper()
	ctx := t.Context()
	s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies), fixturePolicies(store.Policies{ClaimProfiles: &matchingEnrollment{profile: match.Profile{Capacity: match.Resources{Cores: 8}}}}))
	tenant, plan, err := storePlanSealRequestWith(ctx, s, func(input *store.IntakeRequest, doc *nodeproto.PlanDoc) {
		input.Request.Git.Ref, input.Request.Git.SHA = "refs/heads/main", "0123456789abcdef0123456789abcdef01234567"
		input.Request.Git.Repository.Key = "gh:42"
		doc.Jobs = []nodeproto.JobSpec{
			{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}},
			{ID: "z-preferred", Prefers: []string{"gpu"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
			{ID: "a-blocked", Requires: []string{"unavailable"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-blocked", "job", "z-preferred"} {
		if _, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: controllerwire.NodeRef{RunID: plan.RunID, NodeID: id}, Actor: plan.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: plan.RunID, NodeID: "a-blocked"})
	if err != nil {
		t.Fatal(err)
	}
	authority := plan.Seal.Request.AuthorityRef
	admin := capability.Authorized{Principal: capability.Principal{Authority: authority}, Target: capability.Target{Team: string(tenant.Team())}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	if _, err := tenant.RegisterGitHubRunner(ctx, admin, store.GitHubRunnerBinding{RepositoryID: 42, RepositoryOwnerID: 7, Repository: "acme/repo"}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(30 * time.Minute)
	issued, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionActions, Team: string(tenant.Team()), RegistrationID: "github:42:7", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "github:42:7:1000"}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:42", Ref: "refs/heads/main", SHA: "0123456789abcdef0123456789abcdef01234567", WorkflowRunID: "1000"}}, Repositories: []string{"gh:42"}, ExpiresAt: &until})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := tenant.AdmitRun(ctx, store.IntakeRequest{Authority: authority, Request: plan.Seal.Request.Request})
	if err != nil {
		t.Fatal(err)
	}
	foreign := plan.Seal.Request.Request
	foreign.Git.Repository.Key = "gh:43"
	foreign.Git.Repository.Display = "https://github.com/acme/other"
	if _, err := tenant.AdmitRun(ctx, store.IntakeRequest{Authority: authority, Request: foreign}); err != nil {
		t.Fatal(err)
	}
	demand := store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitTrigger}, Available: match.Resources{Cores: 8}}
	checkStoreQueueCounts(t, s, map[string]int{store.QueueStateReady: 3})
	claim, err := s.AsOperator().ClaimUnit(ctx, demand)
	if err != nil || claim.RunID != pending.RunID || claim.Kind != capability.UnitTrigger {
		t.Fatalf("scoped trigger: %s/%s, %v", claim.Kind, claim.RunID, err)
	}
	if _, err := s.AsOperator().ClaimUnit(ctx, demand); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign push remained claimable: %v", err)
	}
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	order := []string{"job", "z-preferred"}
	if preferred {
		demand.Labels = []string{"gpu"}
		order[0], order[1] = order[1], order[0]
	}
	for _, id := range order {
		claim, err := s.AsOperator().ClaimUnit(ctx, demand)
		if err != nil || claim.NodeID != id {
			t.Fatalf("job order wants %s, got %s/%s: %v", id, claim.RunID, claim.NodeID, err)
		}
	}
	checkStoreQueueCounts(t, s, map[string]int{store.QueueStateReady: 1, store.QueueStateClaimed: 2})
	for range 2 {
		if _, err := s.AsOperator().ClaimUnit(ctx, demand); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ineligible queue: %v", err)
		}
	}
	blocked, err := tenant.ReadNode(ctx, before.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, blocked) {
		t.Fatal("mismatch changed blocked job's position or state")
	}
	demand.Labels = []string{"unavailable"}
	claim, err = s.AsOperator().ClaimUnit(ctx, demand)
	if err != nil || claim.NodeID != "a-blocked" {
		t.Fatalf("later eligible claimant: %s/%s, %v", claim.RunID, claim.NodeID, err)
	}
	checkStoreQueueCounts(t, s, map[string]int{store.QueueStateClaimed: 3})
}

func checkStoreQueueCounts(t *testing.T, s *store.Store, want map[string]int) {
	t.Helper()
	for _, state := range store.QueueStates() {
		want[state] += 0
	}
	counts, err := s.AsOperator().CountNodesByQueueState(t.Context())
	if err != nil || !reflect.DeepEqual(counts, want) {
		t.Fatalf("queue states: %v, %v; want %v", counts, err, want)
	}
}
