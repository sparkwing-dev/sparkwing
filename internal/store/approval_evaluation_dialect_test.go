package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestControllerApprovalEvaluationCommitsInEachDialect(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			for _, policy := range []string{"open", "skip", "fail"} {
				t.Run(policy, func(t *testing.T) {
					s := dialect.target(t).Open(t, store.WithPolicies(sealStorePolicies))
					tenant, plan, err := storePlanSealRequestWith(t.Context(), s, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) {
						doc.Jobs = []nodeproto.JobSpec{{ID: "gate", Approval: &nodeproto.ApprovalSpec{}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"guard"}}}}
					})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
						t.Fatal(err)
					}
					if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, `UPDATE runs SET dispatch='controller' WHERE team=? AND id=?`), tenant.Team(), plan.RunID); err != nil {
						t.Fatal(err)
					}
					lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "controller", time.Hour)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", 100); err != nil {
						t.Fatal(err)
					}
					authority := plan.Seal.Request.AuthorityRef
					machine, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()), RegistrationID: "runner:planner:approval", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "approval"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
					if err != nil {
						t.Fatal(err)
					}
					ref := controllerwire.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "gate"}
					claim, err := s.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: machine.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitEval, RunID: ref.RunID, NodeID: ref.NodeID, SourceGeneration: 0}, Available: match.Resources{Cores: 8}})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err != nil {
						t.Fatal(err)
					}
					actor := store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
					var command store.NodeCommand = store.OpenApprovalNode{Ref: ref, Actor: actor}
					want := store.NodeState("")
					if policy != "open" {
						kind := store.PolicyApprovalBypass
						want = store.NodeSkipped
						reason := "skip_if"
						if policy == "fail" {
							kind = store.PolicyApprovalFail
							want = store.NodeFailed
							reason = "predicate failed"
						}
						command = store.OwnerPolicyNode{Ref: ref, Actor: actor, Outcome: want, OwnerPolicy: &store.OwnerPolicyResult{Kind: kind, Generation: 0}, Result: &store.NodeResult{Outcome: want, Reason: reason}}
					}
					if _, err := tenant.TransitionNode(t.Context(), command); err != nil {
						t.Fatal(err)
					}
					row, err := tenant.ReadNode(t.Context(), ref)
					if err != nil || row.Attempt != 0 || policy == "open" && row.Status != store.StatusApprovalPending || policy != "open" && (row.Status != store.StatusDone || row.Outcome != want) {
						t.Fatalf("approval result: %+v %v", row, err)
					}
					var status string
					if err := s.DB().QueryRowContext(t.Context(), storetest.Rebind(s, `SELECT status FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=0`), tenant.Team(), ref.RunID, ref.NodeID).Scan(&status); err != nil || status != "done" {
						t.Fatalf("evaluation completion: %s %v", status, err)
					}
				})
			}
		})
	}
}
