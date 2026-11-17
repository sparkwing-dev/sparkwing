package store_test

import (
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestNodeDefaultKeepsDeclaredStepOrder(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			s := dialect.target(t).Open(t, store.WithPolicies(sealStorePolicies))
			source := admitArgumentRun(t, s, "metadata", []nodewire.JobSpec{
				{ID: "alpha", Steps: []nodewire.StepSpec{{ID: "first"}, {ID: "second"}}, Arguments: []nodewire.ArgumentDescription{jobArgument()}},
				{ID: "beta", Steps: []nodewire.StepSpec{{ID: "control"}}},
			})
			steps, err := teamOf(t, s, source).ListDeclaredSteps(t.Context(), source.RunID)
			if err != nil || !slices.Equal(steps["alpha"], []string{"first", "second"}) || !slices.Equal(steps["beta"], []string{"control"}) {
				t.Fatalf("declared step order lost: %v %v", steps, err)
			}
		})
	}
}

func TestNodeDefaultKeepsBlobAndChildAuthority(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			s := dialect.target(t).Open(t, store.WithPolicies(sealStorePolicies))
			plan := admitArgumentRun(t, s, "reader", []nodewire.JobSpec{
				{ID: "build", Steps: []nodewire.StepSpec{{ID: "body"}}},
				{ID: "alpha", Needs: []string{"build"}, Memo: &nodewire.MemoSpec{}, Closures: nodewire.ClosureSpec{MemoKey: "memo"}, Children: []nodewire.ChildPipeline{{Pipeline: "child"}}, Steps: []nodewire.StepSpec{{ID: "body"}}, Arguments: []nodewire.ArgumentDescription{jobArgument()}},
			})
			tenant := teamOf(t, s, plan)
			live, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "alpha"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, `UPDATE nodes SET spec_json=? WHERE team=? AND run_id=? AND node_id='alpha'`), []byte(live.Spec), tenant.Team(), plan.RunID); err != nil {
				t.Fatal(err)
			}
			if err := s.ResealNodeArgumentsForTest(t.Context(), tenant.Team(), plan.RunID, "alpha"); err != nil {
				t.Fatal(err)
			}
			if err := s.CheckNodeArgumentBlobMetadataForTest(t.Context(), tenant.Team(), plan.RunID, "alpha", "build"); err != nil {
				t.Fatal(err)
			}
			if _, err := finishGeneratedDependencyJob(t.Context(), s, tenant, plan, "build"); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "alpha"}, Actor: plan.Actor}); err != nil {
				t.Fatal(err)
			}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: plan.Actor.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(plan.RunID), NodeID: "alpha"}})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
			if err != nil || !slices.Contains(principal.Capability.Children, nodewire.ChildPipeline{Pipeline: "child", Repository: string(plan.Seal.Request.Request.Git.Repository.Key)}) {
				t.Fatalf("child declaration lost from award: %v %v", principal.Capability.Children, err)
			}
		})
	}
}
