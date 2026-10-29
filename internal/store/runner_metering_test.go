package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func runnerMeteringFixture(t *testing.T) (*Store, UnitDemand, capability.Authorized) {
	t.Helper()
	s, demand, _ := meteredUnitFixture(t, false)
	owner := authorityMember(t, s, "owner", RoleOwner)
	expiry := s.now().Add(time.Hour)
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Authority: &owner.Authority, Team: "a",
		RegistrationID: "runner:a:worker", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "worker"},
		Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone},
		Repositories: []string{"github.com/acme/*"}, ExpiresAt: &expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	demand.CapabilityPrefix = issued.Capability.Prefix
	p, err := s.AsOperator().ResolveAuthority(t.Context(), owner.Authority)
	if err != nil {
		t.Fatal(err)
	}
	p.Class = capability.CredentialConsole
	p.Capability.Grants = []capability.Grant{capability.OperatorConsole}
	route, ok := capability.LookupRoute("POST", "/api/v1/operator/runners/{runner}/metered")
	if !ok {
		t.Fatal("missing metering policy")
	}
	s.consolePolicy = func(p capability.Principal, _ time.Time) bool { return p.Capability.Subject.ID == "owner" }
	return s, demand, capability.Authorized{Principal: p, Access: route.Access[0]}
}

func TestRunnerMeteringChangesOnlyFutureAwards(t *testing.T) {
	s, demand, authorized := runnerMeteringFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET spec_json=?,spec_hash='fixture',plan_json=?,plan_hash='fixture',sealed_at=1 WHERE id='run'`, []byte(`{}`), []byte(`{"jobs":[{"id":"job"}]}`))
	ownerDemand := demand
	ownerDemand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	_, owner := meteredUnitClaim(t, s, ownerDemand)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}
	before, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil || before.Capability.Billing.Metered {
		t.Fatalf("initial award: %+v, %v", before, err)
	}
	if err := s.AsOperator().SetRunnerMetered(t.Context(), authorized, demand.CapabilityPrefix, true); err != nil {
		t.Fatal(err)
	}
	after, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("existing award changed: %+v, %v", after, err)
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Tenant{s: s, team: "a"}).TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: runstate.NodeReady, Cause: runstate.CauseRelease, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	next, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: next.Award.CapabilityPrefix})
	if err != nil || !p.Capability.Billing.Metered {
		t.Fatalf("future award did not use root metering: %+v, %v", p, err)
	}
	if err := s.AsOperator().SetRunnerMetered(t.Context(), authorized, demand.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	current, err := s.AsOperator().ResolveAuthority(t.Context(), p.Authority)
	if err != nil || !reflect.DeepEqual(p, current) {
		t.Fatalf("metered award changed: %+v, %v", current, err)
	}
}

func TestRunnerMeteringRequiresCurrentConsoleAuthorityProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.0s measured in the short class; the full suite runs it")
	}
	random := rand.New(rand.NewPCG(519, 804))
	for i := range 64 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, demand, authorized := runnerMeteringFixture(t)
			prefix := demand.CapabilityPrefix
			before := []byte{}
			if err := s.db.QueryRow(`SELECT authority_json FROM capabilities WHERE prefix=?`, prefix).Scan(&before); err != nil {
				t.Fatal(err)
			}
			switch i % 10 {
			case 0:
				authorized.Principal.Class = capability.CredentialBearer
			case 1:
				authorized.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
			case 2:
				authorized.Principal.Capability.Subject.ID = fmt.Sprint("forged-", random.Uint64())
			case 3:
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=? WHERE hash='owner'`, s.now().Add(-time.Duration(random.IntN(1000))*time.Second).UnixNano())
			case 4:
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET expires_at=? WHERE hash='owner'`, s.now().Add(-time.Duration(random.IntN(1000))*time.Second).UnixNano())
			case 5:
				authorized.Target.Team = fmt.Sprint("foreign-", random.Uint64())
			case 6:
				authorized.Access.Credential = capability.CredentialMember
			case 7:
				s.consolePolicy = func(capability.Principal, time.Time) bool { return false }
			case 8:
				s.consolePolicy = nil
			case 9:
				parent := authorityRoot(fmt.Sprint("parent-", random.Uint64()), "a")
				parent.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}
				parent.Grants = []capability.Grant{capability.TeamAdmin, capability.RunsRead}
				authorityPersist(t, s, parent)
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET capability_prefix=? WHERE hash='owner'`, parent.Prefix)
			}
			if err := s.AsOperator().SetRunnerMetered(t.Context(), authorized, prefix, true); err == nil {
				t.Fatal("forged or ended console changed metering")
			}
			var after []byte
			var metered int
			if err := s.db.QueryRow(`SELECT authority_json,metered FROM capabilities WHERE prefix=?`, prefix).Scan(&after, &metered); err != nil || metered != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused transaction changed root: %d, %v", metered, err)
			}
		})
	}
}

func TestRunnerMeteringRechecksSessionAfterTargetLock(t *testing.T) {
	s, demand, authorized := runnerMeteringFixture(t)
	calls := 0
	s.consolePolicy = func(capability.Principal, time.Time) bool {
		calls++
		return calls == 1
	}
	if err := s.AsOperator().SetRunnerMetered(t.Context(), authorized, demand.CapabilityPrefix, true); err == nil || calls != 2 {
		t.Fatalf("console policy was not rechecked: calls=%d, %v", calls, err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
	if err != nil || p.Capability.RootMetered {
		t.Fatalf("failed console recheck changed metering: %+v, %v", p, err)
	}
}
