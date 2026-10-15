package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestClaimPlacementOnlyLiveEligibleRunnersCanHoldWorkProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(213, 611))
	for _, problem := range []string{"live", "stale", "revoked", "expired", "foreign", "repository", "grant", "capacity", "hard_selector", "deadline", "preferred_claimant", "granted_name", "unknown_capacity"} {
		for i := range 6 {
			t.Run(fmt.Sprintf("%s/%d", problem, i), func(t *testing.T) {
				s, request := unitClaimFixture(t)
				now := s.now()
				s.clock = func() time.Time { return now }
				preferred := fmt.Sprintf("gpu-%x", random.Uint64())
				if problem == "granted_name" {
					preferred = "name=preferred"
					request.Labels = []string{preferred}
				}
				policy := ClaimPlacement{Hold: 3 * time.Minute, Liveness: time.Minute}
				execution := runspec.JobExecutionSettings{Resources: runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Value: runspec.Resources{Cores: runspec.Setting[float64]{Optional: runspec.Optional[float64]{Value: 4, Set: true}}}, Set: true}}}
				if i%2 == 0 {
					policy.DefaultPrefers = []string{preferred}
				} else {
					execution.Prefers = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Value: []string{preferred}, Set: true}}
				}
				if problem == "hard_selector" {
					execution.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Value: []string{"tool:git"}, Set: true}}
					request.Labels = []string{"tool:git"}
				}
				if problem == "preferred_claimant" {
					request.Labels = []string{preferred}
				}
				body, err := json.Marshal(execution)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=?,execution_json=?`, now.UnixNano(), body)
				root := authorityRoot("preferred", "a")
				if problem == "foreign" {
					root.Team = "b"
				}
				if problem == "repository" {
					root.Repositories = []string{"gh:999"}
				}
				if problem == "grant" {
					root.Grants = []capability.Grant{capability.TriggersClaim}
				}
				expires := now.Add(30 * time.Second)
				if problem == "expired" {
					root.ExpiresAt = &expires
				}
				authorityPersist(t, s, root)
				observation := UnitDemand{CapabilityPrefix: root.Prefix, Labels: []string{preferred, "name=forged", "pool=forged"}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}
				if problem == "unknown_capacity" {
					observation.Capacity, observation.Available = match.Resources{}, match.Resources{}
				}
				if problem == "capacity" {
					observation.Available.Cores = 2
				}
				if err := s.RecordRunnerPoll(t.Context(), observation); err != nil {
					t.Fatal(err)
				}
				switch problem {
				case "stale":
					now = now.Add(policy.Liveness + time.Nanosecond)
				case "expired":
					now = expires
				case "deadline":
					now = now.Add(policy.Hold)
				case "revoked":
					member := authorityMember(t, s, "owner", RoleOwner)
					if err := (&Tenant{s: s, team: "a"}).RevokeAuthority(t.Context(), member, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}); err != nil {
						t.Fatal(err)
					}
				}
				if i%3 == 0 {
					request.Target, request.Kinds = nil, []capability.UnitKind{capability.UnitNode}
				}
				tenant := &Tenant{s: s, team: "a"}
				before, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
				if err != nil {
					t.Fatal(err)
				}
				ctx := WithClaimPlacement(t.Context(), policy)
				claim, err := s.ClaimUnit(ctx, request)
				if problem == "live" || problem == "granted_name" {
					var denied *apierr.Error
					if !errors.Is(err, ErrNotFound) && (!errors.As(err, &denied) || denied.Code != apierr.LockHeld) {
						t.Fatalf("live preferred runner did not hold work: %+v, %v", claim, err)
					}
					after, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("held poll changed job: %v", err)
					}
					rewriteSchemaCount(t, s.db, "billing_awards", 0)
					rewriteSchemaCount(t, s.db, "capabilities", 2)
					return
				}
				if err != nil || claim.NodeID != "job" {
					t.Fatalf("eligible fallback was held: %+v, %v", claim, err)
				}
				var reason string
				if err := s.db.QueryRow(`SELECT placement_reason FROM nodes`).Scan(&reason); err != nil {
					t.Fatal(err)
				}
				want := PlacementFallback
				if problem == "preferred_claimant" {
					want = PlacementPreferred
				}
				if reason != want {
					t.Fatalf("placement reason=%q, want %q", reason, want)
				}
			})
		}
	}
}
