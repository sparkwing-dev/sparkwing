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
)

func TestCloudSizePreferenceIsRecordedOnAward(t *testing.T) {
	for _, tc := range []struct{ prefers, want string }{
		{"sparkwing-4cpu", PlacementFallback},
		{"sparkwing-16cpu", PlacementPreferred},
	} {
		t.Run(tc.prefers, func(t *testing.T) {
			s, demand, _ := meteredUnitFixture(t, true)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(fmt.Sprintf(`{"requires":{"value":["sparkwing-16cpu"]},"prefers":{"value":[%q]}}`, tc.prefers)))
			indexFixtureUnit(t, s, "a", *demand.Target)
			rates := CreditRateTable{{Cores: 16, MicroPerSecond: 40}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &rates}); err != nil {
				t.Fatal(err)
			}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			resources, err := s.AsOperator().CloudAwardResources(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
			if err != nil || resources == nil || resources.Cores != 16 {
				t.Fatalf("Cloud allocation = %+v, %v; want 16 CPUs", resources, err)
			}
			var reason string
			if err := s.db.QueryRow(`SELECT placement_reason FROM nodes`).Scan(&reason); err != nil {
				t.Fatal(err)
			}
			if reason != tc.want {
				t.Fatalf("Cloud size preference recorded %q, want %q", reason, tc.want)
			}
		})
	}
}

func TestClaimPlacementOnlyLiveEligibleRunnersCanHoldWorkProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(213, 611))
	for _, problem := range []string{"live", "stale", "stale_activity", "revoked", "expired", "foreign", "repository", "grant", "hard_selector", "deadline", "preferred_claimant", "granted_name"} {
		for i := range 6 {
			t.Run(fmt.Sprintf("%s/%d", problem, i), func(t *testing.T) {
				s, request := unitClaimFixture(t)
				now := s.now()
				s.clock = func() time.Time { return now }
				preferred := fmt.Sprintf("gpu-%x", random.Uint64())
				if problem == "granted_name" {
					preferred = "name=preferred"
					request.Labels = []string{preferred, "team-4cpu"}
				}
				policy := ClaimPlacement{Preferences: []PlacementPreference{{Hold: 3 * time.Minute}, {}}, Liveness: time.Minute}
				execution := runspec.JobExecutionSettings{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"team-4cpu"}}}}
				if i%2 == 0 {
					policy.Preferences[0].Prefers = []string{preferred}
				} else {
					execution.Prefers = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Value: []string{preferred}, Set: true}}
				}
				if problem == "hard_selector" {
					execution.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Value: []string{"tool:git"}, Set: true}}
					request.Labels = []string{"tool:git"}
				}
				if problem == "preferred_claimant" {
					request.Labels = []string{preferred, "team-4cpu"}
				}
				body, err := json.Marshal(execution)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=?,execution_json=?`, now.UnixNano(), body)
				indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
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
				observation := UnitDemand{CapabilityPrefix: root.Prefix, Labels: []string{preferred, "team-4cpu", "name=forged", "pool=forged"}}
				if err := s.AsOperator().RecordRunnerPoll(t.Context(), observation); err != nil {
					t.Fatal(err)
				}
				switch problem {
				case "stale", "stale_activity":
					now = now.Add(policy.Liveness + time.Nanosecond)
					if problem == "stale_activity" {
						rewriteSchemaExec(t, s.db, `UPDATE capabilities SET last_used_at=? WHERE prefix=?`, now.UnixNano(), root.Prefix)
					}
				case "expired":
					now = expires
				case "deadline":
					now = now.Add(policy.Preferences[0].Hold)
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
				before, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
				if err != nil {
					t.Fatal(err)
				}
				ctx := WithClaimPlacement(t.Context(), policy)
				claim, err := s.AsOperator().ClaimUnit(ctx, request)
				if problem == "live" || problem == "granted_name" {
					var denied *apierr.Error
					if !errors.Is(err, ErrNotFound) && (!errors.As(err, &denied) || denied.Code != apierr.LockHeld) {
						t.Fatalf("live preferred runner did not hold work: %+v, %v", claim, err)
					}
					after, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
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

func TestOrderedPlacementPreferencesHoldOnlyForLiveEarlierRunnersProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(318, 910))
	for i := range 96 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, request := unitClaimFixture(t)
			start := s.now()
			now := start
			s.clock = func() time.Time { return now }
			order := []string{"runner", "earlier-a", "earlier-b"}
			random.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
			preferences := make([]PlacementPreference, 0, 4)
			rank := 0
			for j, name := range order {
				preferences = append(preferences, PlacementPreference{Prefers: []string{"name=" + name}, Hold: time.Duration(random.IntN(31)) * time.Second})
				if name == "runner" {
					rank = j
				}
			}
			preferences = append(preferences, PlacementPreference{})
			elapsed := time.Duration(random.IntN(100)) * time.Second
			if i%3 == 0 {
				elapsed = 0
				for j := range rank {
					elapsed += preferences[j].Hold
				}
				if i%2 == 0 && elapsed > 0 {
					elapsed -= time.Nanosecond
				}
			}
			now = start.Add(elapsed)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=?`, start.UnixNano())
			deadline := time.Duration(0)
			wantHeld := false
			for j, name := range order {
				deadline += preferences[j].Hold
				if name == "runner" {
					continue
				}
				root := authorityRoot(name, "a")
				authorityPersist(t, s, root)
				live := random.IntN(2) == 0
				if live {
					observation := UnitDemand{CapabilityPrefix: name, Labels: []string{"team-4cpu", "name=runner", "class=cloud"}}
					if err := s.AsOperator().RecordRunnerPoll(t.Context(), observation); err != nil {
						t.Fatal(err)
					}
				}
				if live && j < rank && elapsed < deadline {
					wantHeld = true
				}
			}
			request.Labels = []string{"team-4cpu", "name=" + order[0], "class=cloud"}
			if i%2 == 0 {
				request.Target, request.Kinds = nil, []capability.UnitKind{capability.UnitNode}
			}
			tenant := &Tenant{s: s, team: "a"}
			before, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := s.AsOperator().ClaimUnit(WithClaimPlacement(t.Context(), ClaimPlacement{Preferences: preferences, Liveness: time.Minute}), request)
			if wantHeld {
				var refused *apierr.Error
				if !errors.Is(err, ErrNotFound) && (!errors.As(err, &refused) || refused.Code != apierr.LockHeld) {
					t.Fatalf("order=%v rank=%d elapsed=%s: %+v %v", order, rank, elapsed, claim, err)
				}
				after, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("held poll changed node: %v", err)
				}
				rewriteSchemaCount(t, s.db, "billing_awards", 0)
			} else if err != nil || claim.Award.Holder != "runner" {
				t.Fatalf("order=%v rank=%d elapsed=%s: %+v %v", order, rank, elapsed, claim, err)
			}
		})
	}
}
