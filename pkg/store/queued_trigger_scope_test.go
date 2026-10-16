package store_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestQueuedTriggerRepositoryScopeProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(323, 911))
	for trial := range 4 {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			s := storetest.OpenSQLite(t)
			tenant, plan, err := storePlanSealRequest(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			authority := plan.Seal.Request.AuthorityRef
			repositories := []string{"github.com/acme/allowed"}
			if trial%2 != 0 {
				repositories = []string{"github.com/acme/*", "github.com/acme/allowed"}
			}
			root, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{
				Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()),
				RegistrationID: "runner:planner:queued", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "queued"},
				Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: repositories, Grants: []capability.Grant{capability.TriggersClaim},
			})
			if err != nil {
				t.Fatal(err)
			}
			child, err := s.DelegateCapability(t.Context(), capability.DelegateSpec{
				Parent:   capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix},
				Audience: capability.Controller, Grants: []capability.Grant{capability.TriggersClaim},
				Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/allowed"},
			})
			if err != nil {
				t.Fatal(err)
			}
			var blocked [][]any
			var runIDs []string
			for i := range 65 + random.IntN(10) {
				request := plan.Seal.Request.Request
				request.Git.Repository = runspec.Repository{Key: runspec.RepoKey(fmt.Sprintf("gh:%d", i+100)), Display: fmt.Sprintf("https://github.com/acme/blocked-%x", random.Uint64())}
				admitted, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: authority, Request: request})
				if err != nil {
					t.Fatal(err)
				}
				runIDs = append(runIDs, admitted.RunID)
				blocked = append(blocked, queuedOwnerState(t, tenant, s, admitted.RunID))
			}
			var eligible []string
			for range 2 + random.IntN(3) {
				request := plan.Seal.Request.Request
				request.Git.Repository = runspec.Repository{Key: "gh:999", Display: "https://github.com/acme/allowed"}
				admitted, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: authority, Request: request})
				if err != nil {
					t.Fatal(err)
				}
				eligible = append(eligible, admitted.RunID)
			}
			demand := store.UnitDemand{CapabilityPrefix: child.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitTrigger}, Available: match.Resources{Cores: 8}}
			for _, id := range eligible {
				claim, err := s.ClaimUnit(t.Context(), demand)
				if err != nil || claim.RunID != id || claim.Team != tenant.Team() || len(claim.Bearer) == 0 {
					t.Fatalf("eligible tail claim: %s/%s, %v", claim.Team, claim.RunID, err)
				}
			}
			if _, err := s.ClaimUnit(t.Context(), demand); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("excluded queue: %v", err)
			}
			for i, id := range runIDs {
				after := queuedOwnerState(t, tenant, s, id)
				if !reflect.DeepEqual(blocked[i], after) {
					t.Fatalf("queue scan changed excluded run %s", id)
				}
			}
		})
	}
}

func queuedOwnerState(t *testing.T, tenant *store.Tenant, s *store.Store, run string) []any {
	t.Helper()
	snapshot, err := tenant.ReadSnapshot(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.StoreNow = time.Time{}
	var status, prefix string
	var generation int64
	var holder sql.NullString
	var lease, started sql.NullInt64
	if err := s.DB().QueryRowContext(t.Context(), `SELECT status,generation,claimed_by,capability_prefix,lease_expires_at,started_at FROM triggers WHERE team=? AND id=?`, tenant.Team(), run).Scan(&status, &generation, &holder, &prefix, &lease, &started); err != nil {
		t.Fatal(err)
	}
	return []any{snapshot, status, generation, holder, prefix, lease, started}
}

func TestQueuedTriggerClaimsNeverCrossTeamsProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(511, 827))
	s := storetest.OpenSQLite(t)
	planner, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.ResolveSignIn(t.Context(), googleProfile("other", "other@example.test"), store.SignUpConditions{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := s.CreateAccountSession(t.Context(), member.Account, member.PersonalTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(raw))
	authorities := map[store.Team]capability.AuthorityRef{
		planner.Team():      plan.Seal.Request.AuthorityRef,
		member.PersonalTeam: {Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])},
	}
	owners := map[string]store.Team{}
	var roots []capability.Capability
	for team, authority := range authorities {
		tenant, err := s.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		root, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{
			Kind: capability.ProvisionMachine, Authority: &authority, Team: string(team),
			RegistrationID: "runner:" + string(team) + ":queued", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "queued"},
			Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, Grants: []capability.Grant{capability.TriggersClaim},
		})
		if err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root.Capability)
		for range 5 + random.IntN(4) {
			request := plan.Seal.Request.Request
			request.Team = string(team)
			admitted, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: authority, Request: request})
			if err != nil {
				t.Fatal(err)
			}
			owners[admitted.RunID] = team
		}
	}
	results := make(chan store.UnitClaim, len(owners))
	errorsFound := make(chan error, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, root := range roots {
		for range 4 {
			workers.Go(func() {
				<-start
				for {
					claim, err := s.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: root.Prefix, Kinds: []capability.UnitKind{capability.UnitTrigger}, Available: match.Resources{Cores: 8}})
					if errors.Is(err, store.ErrNotFound) {
						return
					}
					if err != nil {
						errorsFound <- err
						return
					}
					if claim.Team != store.Team(root.Team) {
						errorsFound <- fmt.Errorf("claimant in %s acquired %s", root.Team, claim.Team)
						return
					}
					results <- claim
				}
			})
		}
	}
	close(start)
	workers.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	seen := map[string]bool{}
	for claim := range results {
		if owners[claim.RunID] != claim.Team || seen[claim.RunID] {
			t.Fatalf("duplicate or foreign award: %s/%s", claim.Team, claim.RunID)
		}
		seen[claim.RunID] = true
	}
	if len(seen) != len(owners) {
		t.Fatalf("claimed %d of %d eligible owners", len(seen), len(owners))
	}
}
