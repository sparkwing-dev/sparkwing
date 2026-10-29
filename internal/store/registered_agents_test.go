package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestRegisteredAgentsHonorRotationGraceProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.0s measured in the short class; the full suite runs it")
	}
	random := rand.New(rand.NewPCG(433, 800))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := authorityStore(t)
			start := time.Unix(1700000000+random.Int64N(1000000), random.Int64N(1000000000)).UTC()
			deadline := start.Add(time.Duration(1 + random.Int64N(int64(time.Hour))))
			now := start
			s.clock = func() time.Time { return now }
			root := authorityRoot("runner", "a")
			root.RevokedAt = &deadline
			authorityPersist(t, s, root)
			tenant := &Tenant{s: s, team: "a"}
			for _, at := range []time.Time{start, deadline.Add(-time.Nanosecond), deadline, deadline.Add(time.Duration(random.Int64N(int64(time.Hour))))} {
				now = at
				agents, err := tenant.ListRegisteredAgents(t.Context())
				want := 0
				if now.Before(deadline) {
					want = 1
				}
				if err != nil || len(agents) != want {
					t.Fatalf("at %v deadline %v: %d agents, %v; want %d", now, deadline, len(agents), err, want)
				}
			}
		})
	}
}

func TestRegisteredAgentsUseLiveCapabilityIdentityAndSavedLabels(t *testing.T) {
	s := authorityStore(t)
	now := time.Unix(1700000000, 123).UTC()
	s.clock = func() time.Time { return now }
	root := authorityRoot("runner", "a")
	expiry := now.Add(time.Hour)
	root.ExpiresAt = &expiry
	root.ReservedPools = []string{"prod"}
	authorityPersist(t, s, root)
	authorityPersist(t, s, authorityRoot("foreign", "b"))
	if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Labels: []string{"gpu", "name=forged", "class=cloud", "pool=forged", "os=linux", "arch=arm64"}}); err != nil {
		t.Fatal(err)
	}
	agents, err := (&Tenant{s: s, team: "a"}).ListRegisteredAgents(t.Context())
	if err != nil || len(agents) != 1 {
		t.Fatalf("registered agents: %+v, %v", agents, err)
	}
	got := agents[0]
	if got.Name != root.Subject.ID || got.CapabilityPrefix != root.Prefix || !got.LastSeen.Equal(now) || !slices.Equal(got.Profile.Labels, []string{"gpu", "os=linux", "arch=arm64"}) || !slices.Equal(got.Profile.ReservedPools, root.ReservedPools) || got.Profile.Class != "agent" {
		t.Fatalf("registered identity: %+v", got)
	}
}

func TestRegisteredAgentsRetainEnrollmentWithoutReplacingGrantedIdentity(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("runner", "a")
	root.ReservedPools = []string{"prod"}
	authorityPersist(t, s, root)
	accept, err := match.ParseAccept([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	s.claimProfiles = claimProfileFake{profile: match.Profile{Name: "impostor", Class: match.ClassCloud, ReservedPools: []string{"forged"}, Location: "local", Accept: accept}}
	agents, err := (&Tenant{s: s, team: "a"}).ListRegisteredAgents(t.Context())
	if err != nil || len(agents) != 1 {
		t.Fatalf("enrolled agents: %+v, %v", agents, err)
	}
	got := agents[0].Profile
	if got.Name != root.Subject.ID || got.Class != match.ClassAgent || !slices.Equal(got.ReservedPools, root.ReservedPools) || got.Location != "local" || got.Accept == nil {
		t.Fatalf("enrolled profile: %+v", got)
	}
}

func TestRegisteredAgentsExcludeExpiredRevokedAndNonClaimants(t *testing.T) {
	for _, problem := range []string{"expired", "revoked", "parent_revoked", "parent_narrowed", "reader", "nodehost", "missing"} {
		t.Run(problem, func(t *testing.T) {
			s := authorityStore(t)
			now := time.Unix(1700000000, 123).UTC()
			s.clock = func() time.Time { return now }
			parent := authorityRoot("parent", "a")
			parent.Subject.Kind = capability.SubjectService
			if problem == "parent_revoked" {
				parent.RevokedAt = &now
			}
			if problem == "parent_narrowed" {
				parent.Grants = []capability.Grant{capability.RunsRead}
			}
			authorityPersist(t, s, parent)
			root := authorityRoot("runner", "a")
			root.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}
			switch problem {
			case "expired":
				root.ExpiresAt = &now
			case "revoked":
				root.RevokedAt = &now
			case "reader":
				root.Grants = []capability.Grant{capability.RunsRead}
			case "nodehost":
				root.Audience = capability.NodeHost
			}
			if problem != "missing" {
				authorityPersist(t, s, root)
			}
			if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Labels: []string{"gpu"}}); err == nil {
				t.Fatal("unavailable agent recorded labels")
			}
			agents, err := (&Tenant{s: s, team: "a"}).ListRegisteredAgents(t.Context())
			if err != nil || len(agents) != 0 {
				t.Fatalf("unavailable agents: %+v, %v", agents, err)
			}
		})
	}
}

func TestRegisteredAgentsRejectCorruptCapabilityAndLabels(t *testing.T) {
	for _, column := range []string{"authority_json", "advertised_labels_json"} {
		t.Run(column, func(t *testing.T) {
			s := authorityStore(t)
			authorityPersist(t, s, authorityRoot("runner", "a"))
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET `+column+`=? WHERE prefix='runner'`, []byte(`{`))
			if agents, err := (&Tenant{s: s, team: "a"}).ListRegisteredAgents(t.Context()); err == nil || len(agents) != 0 {
				t.Fatalf("corrupt registration: %+v, %v", agents, err)
			}
		})
	}
}

func TestRecordRunnerPollRejectsExpiredParent(t *testing.T) {
	s := authorityStore(t)
	now := time.Unix(1700000000, 123).UTC()
	s.clock = func() time.Time { return now }
	parent := authorityRoot("parent", "a")
	parent.ExpiresAt = &now
	authorityPersist(t, s, parent)
	root := authorityRoot("runner", "a")
	root.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}
	authorityPersist(t, s, root)
	err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Labels: []string{"gpu"}})
	var denied *apierr.Error
	if !errors.As(err, &denied) || denied.Code != apierr.Unauthenticated {
		t.Fatalf("expired parent: %v", err)
	}
}
