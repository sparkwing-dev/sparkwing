package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestNamedJobClaimCannotBypassReadinessOrAuthorityProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(719, 331))
	for trial := range 12 {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies))
			now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			s.SetTestClock(t, func() time.Time { return now })
			job := fmt.Sprintf("job-%x", random.Uint64())
			label := fmt.Sprintf("tool-%x", random.Uint64())
			tenant, plan, err := storePlanSealRequestWith(t.Context(), s, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) {
				doc.Jobs = []nodeproto.JobSpec{{ID: job, Requires: []string{label}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tenant.AcceptPlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: runspec.NodeID(job)})
			if err != nil || sealed.SpecHash == "" {
				t.Fatalf("accepted job has no spec hash: %v", err)
			}
			authority := plan.Seal.Request.AuthorityRef
			root, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
				Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()),
				RegistrationID: "runner:planner:worker", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "worker"},
				Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, Grants: []capability.Grant{capability.NodesClaim},
			})
			if err != nil {
				t.Fatal(err)
			}
			ref := controllerwire.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: runspec.NodeID(job)}
			demand := store.UnitDemand{CapabilityPrefix: root.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Labels: []string{label}}
			refuse := func(request store.UnitDemand) {
				t.Helper()
				before := readMutationSnapshot(t, tenant, s, plan.RunID)
				claim, err := s.AsOperator().ClaimUnit(t.Context(), request)
				if err == nil || len(claim.Bearer) != 0 {
					t.Fatalf("refused claim returned authority: %+v, %v", claim, err)
				}
				if after := readMutationSnapshot(t, tenant, s, plan.RunID); !reflect.DeepEqual(before, after) {
					t.Fatal("refused claim changed persisted state")
				}
			}
			refuse(demand)
			if _, err := tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: plan.Actor}); err != nil {
				t.Fatal(err)
			}
			bad := demand
			bad.CapabilityPrefix = fmt.Sprintf("forged-%x", random.Uint64())
			refuse(bad)
			bad = demand
			bad.Labels = []string{"name=worker", "class=cloud"}
			refuse(bad)
			bad = demand
			bad.Labels = []string{"missing-label"}
			refuse(bad)
			bad = demand
			target := *demand.Target
			target.NodeID = runspec.NodeID(fmt.Sprintf("missing-%x", random.Uint64()))
			bad.Target = &target
			refuse(bad)
			foreign := signIn(t, s, "foreign", "foreign@example.test")
			raw, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), foreign.Account, foreign.Account.ActiveTeam, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(raw))
			foreignAuthority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
			foreignRoot, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
				Kind: capability.ProvisionMachine, Authority: &foreignAuthority, Team: string(foreign.Account.ActiveTeam),
				RegistrationID: "runner:" + string(foreign.Account.ActiveTeam) + ":foreign", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "foreign"},
				Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, Grants: []capability.Grant{capability.NodesClaim},
			})
			if err != nil {
				t.Fatal(err)
			}
			if foreignRoot.Capability.Team == string(tenant.Team()) {
				t.Fatal("foreign fixture shares the job's tenant")
			}
			bad = demand
			bad.CapabilityPrefix = foreignRoot.Capability.Prefix
			refuse(bad)
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if claim.Team != tenant.Team() || claim.Award.Holder != "worker" || claim.Award.Generation != 1 || len(claim.Bearer) == 0 {
				t.Fatalf("award lost stored identity: %+v", claim)
			}
			refuse(demand)
			holder := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
			if result, err := tenant.MayStart(t.Context(), store.CapacityBinding{RunID: string(holder.RunID), NodeID: string(holder.NodeID), Generation: int64(holder.Generation)}, holder); err != nil || result.Kind != store.AcquireGranted {
				t.Fatalf("execution admission: %+v, %v", result, err)
			}
			for _, change := range []store.NodeCommand{
				store.StartNode{Ref: ref, Actor: holder, SourceAttempt: acceptanceAttempt(0)},
				store.ReportNode{Ref: ref, Actor: holder, Outcome: store.NodeSuccess, SourceAttempt: acceptanceAttempt(1)},
			} {
				if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
					t.Fatal(err)
				}
			}
			refuse(demand)
			row, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || row.SpecHash != sealed.SpecHash {
				t.Fatalf("award changed accepted spec: %s, %v", row.SpecHash, err)
			}
			if _, err := tenant.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Unit: plan.Actor.Owner, Transition: store.RunCompletion{RunID: ref.RunID, Actor: plan.Actor, Cause: store.RunFinish}}); err != nil {
				t.Fatal(err)
			}
			refuse(demand)
		})
	}
}
