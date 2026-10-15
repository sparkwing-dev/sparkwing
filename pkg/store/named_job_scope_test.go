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
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestNamedJobClaimCannotBypassReadinessOrAuthorityProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(719, 331))
	for trial := range 12 {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			s := storetest.OpenSQLite(t)
			job := fmt.Sprintf("job-%x", random.Uint64())
			label := fmt.Sprintf("tool-%x", random.Uint64())
			cores := float64(1 + random.IntN(4))
			tenant, plan, err := storePlanSealRequestWith(t.Context(), s, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) {
				doc.Jobs = []nodeproto.JobSpec{{ID: job, Requires: []string{label}, Resources: &nodeproto.ResourceSpec{Cores: &cores}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tenant.AcceptPlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: plan.RunID, NodeID: job})
			if err != nil || sealed.SpecHash == "" {
				t.Fatalf("accepted job has no spec hash: %v", err)
			}
			authority := plan.Seal.Request.AuthorityRef
			root, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{
				Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()),
				RegistrationID: "runner:planner:worker", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "worker"},
				Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, Grants: []capability.Grant{capability.NodesClaim},
			})
			if err != nil {
				t.Fatal(err)
			}
			ref := store.NodeRef{RunID: plan.RunID, NodeID: job}
			demand := store.UnitDemand{CapabilityPrefix: root.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Labels: []string{label}, Available: match.Resources{Cores: 8}}
			refuse := func(request store.UnitDemand) {
				t.Helper()
				before := readMutationSnapshot(t, tenant, s, plan.RunID)
				claim, err := s.ClaimUnit(t.Context(), request)
				if err == nil || len(claim.Bearer) != 0 {
					t.Fatalf("refused claim returned authority: %+v, %v", claim, err)
				}
				if after := readMutationSnapshot(t, tenant, s, plan.RunID); !reflect.DeepEqual(before, after) {
					t.Fatal("refused claim changed persisted state")
				}
			}
			refuse(demand)
			if _, err := tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: plan.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
				t.Fatal(err)
			}
			bad := demand
			bad.CapabilityPrefix = fmt.Sprintf("forged-%x", random.Uint64())
			refuse(bad)
			bad = demand
			bad.Labels = []string{"name=worker", "class=cloud"}
			refuse(bad)
			bad = demand
			bad.Available.Cores = cores / 2
			refuse(bad)
			bad = demand
			target := *demand.Target
			target.NodeID = fmt.Sprintf("missing-%x", random.Uint64())
			bad.Target = &target
			refuse(bad)
			foreign := signIn(t, s, "foreign", "foreign@example.test")
			raw, _, _, err := s.CreateAccountSession(t.Context(), foreign.Account, foreign.Account.ActiveTeam, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(raw))
			foreignAuthority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
			foreignRoot, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{
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
			claim, err := s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if claim.Team != tenant.Team() || claim.Award.Holder != "worker" || claim.Award.Generation != 1 || len(claim.Bearer) == 0 {
				t.Fatalf("award lost stored identity: %+v", claim)
			}
			refuse(demand)
			holder := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
			for _, change := range []store.NodeTransition{
				{Ref: ref, Actor: holder, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: acceptanceAttempt(0)},
				{Ref: ref, Actor: holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(1)},
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
			if _, err := tenant.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Unit: plan.Actor.Owner, Transition: store.RunTransition{RunID: ref.RunID, To: store.RunSuccess, Actor: plan.Actor, Cause: store.RunFinish}}); err != nil {
				t.Fatal(err)
			}
			refuse(demand)
		})
	}
}
