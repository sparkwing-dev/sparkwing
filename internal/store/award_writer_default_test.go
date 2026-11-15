package store_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// An asserted job's grant reads its own writer's scope on the default branch its
// accepted spec resolved, spelled like its own ref, and writes only its own scope.
func TestAssertedJobAwardReadsItsWritersDefaultBranch(t *testing.T) {
	for _, tc := range []struct {
		name, ref, resolved, want string
	}{
		{name: "branch", ref: "feat", resolved: "main", want: "main"},
		{name: "full branch ref", ref: "refs/heads/feat", resolved: "main", want: "refs/heads/main"},
		{name: "detached head", ref: "", resolved: "main", want: "main"},
		{name: "default branch itself", ref: "main", resolved: "main"},
		{name: "no resolved default branch", ref: "feat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies), fixturePolicies(store.Policies{ClaimProfiles: &matchingEnrollment{profile: match.Profile{}}}))
			tenant, plan, err := storePlanSealRequestWith(ctx, s, func(input *store.IntakeRequest, doc *nodewire.PlanDoc) {
				input.Request.Git.Ref = tc.ref
				doc.Jobs = []nodewire.JobSpec{{ID: "job", Steps: []nodewire.StepSpec{{ID: "body"}}}}
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Seal.Request.Provenance.Trust() != runspec.TrustAsserted {
				t.Fatalf("fixture trust %s", plan.Seal.Request.Provenance.Trust())
			}
			if tc.resolved != "" {
				plan.Seal.Resolved.DefaultBranch = runspec.Optional[string]{Set: true, Value: tc.resolved}
				plan.Seal.Resolved.DefaultBranchSource = runspec.DefaultBranchOriginHEAD
			}
			data := awardJobData(t, s, tenant, plan)
			own := blob.ParseCapabilityScope(data.WriteRef)
			if own.Verified || own.Ref != tc.ref {
				t.Fatalf("write scope %q", data.WriteRef)
			}
			var manual []string
			for _, ref := range data.ReadRefs {
				if strings.HasPrefix(ref, "manual:") && ref != data.WriteRef {
					manual = append(manual, ref)
				}
			}
			var want []string
			if tc.want != "" {
				want = []string{blob.RefScope{Ref: tc.want, Writer: own.Writer}.CapabilityScope()}
			}
			if !slices.Equal(manual, want) || data.ReadRefs[0] != data.WriteRef {
				t.Fatalf("read scopes %v, want own %q then %v", data.ReadRefs, data.WriteRef, want)
			}
		})
	}
}

func awardJobData(t *testing.T, s *store.Store, tenant *store.Tenant, plan store.PlanAcceptance) *capability.DataScope {
	t.Helper()
	ctx := t.Context()
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionNode(ctx, store.ReadyNode{Ref: runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "job"}, Actor: plan.Actor}); err != nil {
		t.Fatal(err)
	}
	authority := plan.Seal.Request.AuthorityRef
	issued, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()), RegistrationID: "runner:" + string(tenant.Team()) + ":vm", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "vm"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}})
	if err != nil {
		t.Fatal(err)
	}
	award, err := s.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil || award.Capability.Data == nil {
		t.Fatalf("award: %+v %v", award.Capability.Data, err)
	}
	return award.Capability.Data
}
