package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func checkStoreGeneratedDependencies(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	tenant, plan, err := acceptStoreGeneratedPlan(ctx, s)
	if err != nil {
		return err
	}
	source, err := finishGeneratedDependencyJob(ctx, s, tenant, plan, "alpha")
	if err != nil {
		return err
	}
	before, err := tenant.ReadSnapshot(ctx, plan.RunID)
	if err != nil {
		return err
	}
	graph, err := before.Scheduling()
	if err != nil || store.Decide(graph).Outcome != "" {
		return fmt.Errorf("generator settled before expansion: %v", err)
	}
	for _, decision := range store.Decide(graph).Nodes {
		if decision.NodeID == "beta" {
			return errors.New("dependent released before expansion")
		}
	}
	owner, err := s.AsOperator().ResolveAuthority(ctx, plan.Actor.Authority)
	if err != nil {
		return err
	}
	binding := owner.Capability.Binding
	binding.Kind, binding.NodeID, binding.Generation = capability.BindingNode, "alpha", int64(source.Generation)
	binding.Expansion = &capability.LeaseRef{Kind: capability.UnitTrigger, ID: plan.RunID, Holder: plan.Actor.Owner.Holder, Generation: int64(plan.Actor.Owner.Generation)}
	expander, err := s.AsOperator().DelegateCapability(ctx, capability.DelegateSpec{
		Parent: plan.Actor.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.ExpansionSubmit},
		Binding: binding, ExpiresAt: owner.Capability.ExpiresAt, Repositories: owner.Capability.Repositories,
	})
	if err != nil {
		return err
	}
	request := store.ExpansionSubmission{Source: source.Ref, SourceGeneration: source.Generation, Actor: store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: expander.Capability.Prefix}, CapabilityPrefix: expander.Capability.Prefix}, Body: []byte(`{"jobs":[{"id":"child","groups":["children"],"deps":["alpha"],"steps":[{"id":"body"}]},{"id":"other","groups":["children"],"deps":["alpha"],"steps":[{"id":"body"}]}]}`)}
	accepted, err := tenant.AcceptExpansion(ctx, request)
	if err != nil || len(accepted.Nodes) != 2 {
		return fmt.Errorf("accept expansion: %+v, %v", accepted, err)
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		return err
	}
	s = reopened
	return checkStoreGeneratedMembership(ctx, s, plan, before, request)
}

func checkStoreGeneratedMembership(ctx context.Context, s *store.Store, plan store.PlanAcceptance, before store.RunSnapshot, request store.ExpansionSubmission) error {
	tenant, err := s.ForTeam(ctx, acceptanceTeam)
	if err != nil {
		return err
	}
	snapshot, err := tenant.ReadSnapshot(ctx, plan.RunID)
	if err != nil {
		return err
	}
	graph, err := snapshot.Scheduling()
	if err != nil || len(graph.Expansions) != 2 || !reflect.DeepEqual(graph.Expansions[0].Members, []string{"child", "other"}) || len(graph.Expansions[1].Members) != 0 {
		return fmt.Errorf("persisted membership: %+v, %v", graph.Expansions, err)
	}
	if got := store.Decide(graph); got.Outcome != "" || len(got.Nodes) != 2 || got.Nodes[0].NodeID != "child" || got.Nodes[1].NodeID != "other" || got.Nodes[0].Kind != store.DecisionReady || got.Nodes[1].Kind != store.DecisionReady {
		return fmt.Errorf("dependent did not wait for member: %+v", got)
	}
	if _, err := finishGeneratedDependencyJob(ctx, s, tenant, plan, "child"); err != nil {
		return err
	}
	snapshot, err = tenant.ReadSnapshot(ctx, plan.RunID)
	if err != nil {
		return err
	}
	graph, err = snapshot.Scheduling()
	if err != nil {
		return err
	}
	if got := store.Decide(graph); len(got.Nodes) != 1 || got.Nodes[0].NodeID != "other" || got.Nodes[0].Kind != store.DecisionReady {
		return fmt.Errorf("dependent released before every member: %+v", got)
	}
	if _, err := finishGeneratedDependencyJob(ctx, s, tenant, plan, "other"); err != nil {
		return err
	}
	replayed, err := tenant.AcceptExpansion(ctx, request)
	if err != nil || !replayed.Replayed || len(replayed.Nodes) != 2 || replayed.Nodes[0].Outcome != store.NodeSuccess || replayed.Nodes[1].Outcome != store.NodeSuccess {
		return fmt.Errorf("replay reset generated state: %+v, %v", replayed, err)
	}
	snapshot, err = tenant.ReadSnapshot(ctx, plan.RunID)
	if err != nil {
		return err
	}
	if snapshot.Run.PlanHash != before.Run.PlanHash || !reflect.DeepEqual(snapshot.Run.Plan, before.Run.Plan) || !reflect.DeepEqual(snapshot.Nodes[1].Spec, before.Nodes[1].Spec) {
		return errors.New("expansion rewrote accepted declarations")
	}
	graph, err = snapshot.Scheduling()
	if err != nil {
		return err
	}
	if got := store.Decide(graph); len(got.Nodes) != 1 || got.Nodes[0].NodeID != "beta" || got.Nodes[0].Kind != store.DecisionReady {
		return fmt.Errorf("completed membership did not release dependent: %+v", got)
	}
	request.Body = []byte(`{"jobs":[]}`)
	if _, err := tenant.AcceptExpansion(ctx, request); err == nil {
		return errors.New("different expansion replaced accepted membership")
	}
	after, err := tenant.ReadSnapshot(ctx, plan.RunID)
	snapshot.StoreNow = after.StoreNow
	if err != nil || !reflect.DeepEqual(snapshot, after) {
		return fmt.Errorf("refused expansion changed state: %v", err)
	}
	return nil
}

func finishGeneratedDependencyJob(ctx context.Context, s *store.Store, tenant *store.Tenant, plan store.PlanAcceptance, id string) (store.NodeRow, error) {
	ref := controllerwire.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: runspec.NodeID(id)}
	if _, err := tenant.TransitionNode(ctx, store.ReadyNode{Ref: ref, Actor: plan.Actor}); err != nil {
		return store.NodeRow{}, err
	}
	claim, err := s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: plan.Actor.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Available: match.Resources{Cores: 8}})
	if err != nil {
		return store.NodeRow{}, err
	}
	holder := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	if admitted, err := tenant.MayStart(ctx, store.CapacityBinding{RunID: string(ref.RunID), NodeID: string(ref.NodeID), Generation: int64(holder.Generation)}, holder); err != nil || admitted.Kind != store.AcquireGranted {
		return store.NodeRow{}, fmt.Errorf("execution admission: %+v, %v", admitted, err)
	}
	start, err := tenant.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: holder, SourceAttempt: acceptanceAttempt(0)})
	if err != nil {
		return store.NodeRow{}, err
	}
	if _, err := tenant.TransitionNode(ctx, store.ReportNode{Ref: ref, Actor: holder, SourceAttempt: acceptanceAttempt(int64(start.Attempt)), Outcome: store.NodeSuccess}); err != nil {
		return store.NodeRow{}, err
	}
	return tenant.ReadNode(ctx, ref)
}

func acceptStoreGeneratedPlan(ctx context.Context, s *store.Store) (*store.Tenant, store.PlanAcceptance, error) {
	tenant, plan, err := storePlanSealRequest(ctx, s)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	declaration := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "alpha", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{Generate: []string{"children", "empty"}}},
		{ID: "beta", NeedsGenerated: []string{"children", "empty"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}
	plan.Seal.Plan, err = json.Marshal(declaration)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	plan.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: plan.Seal.Request, Declarations: plan.Seal.Declarations, Plan: declaration})
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	return tenant, plan, nil
}

func TestStoreGeneratedDependencies(t *testing.T) {
	if err := checkStoreGeneratedDependencies(t); err != nil {
		t.Fatal(err)
	}
}
