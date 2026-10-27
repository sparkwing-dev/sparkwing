package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func readRunnerInventory(t *testing.T, s *Store, team Team) capability.RunnerSnapshot {
	t.Helper()
	reader, ok := any(&Tenant{s: s, team: team}).(interface {
		ReadRunners(context.Context) (capability.RunnerSnapshot, error)
	})
	if !ok {
		t.Fatal("tenant has no runner inventory transaction")
	}
	out, err := reader.ReadRunners(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRunnerInventoryKeepsLabelObservationsAcrossRenewal(t *testing.T) {
	s, demand, claim, now := renewalFixture(t, capability.UnitNode)
	demand.Labels = []string{"gpu", "name=forged", "class=cloud"}
	if err := s.AsOperator().RecordRunnerPoll(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	before := readRunnerInventory(t, s, "a")
	if len(before.Runners) != 1 {
		t.Fatalf("inventory: %+v", before)
	}
	runner := before.Runners[0]
	if runner.Team != "a" || runner.Runner.Name != "runner" || runner.Runner.Type != "runner" || !reflect.DeepEqual(runner.Runner.Labels, []string{"gpu"}) || !reflect.DeepEqual(runner.ActiveJobs, []string{"run/job"}) || runner.Headroom != nil || !runner.LastSeen.Equal(*now) {
		t.Fatalf("runner facts: %+v", runner)
	}
	*now = now.Add(time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	after := readRunnerInventory(t, s, "a")
	if !after.Now.Equal(*now) || !after.Runners[0].LastSeen.Equal(*now) || !reflect.DeepEqual(after.Runners[0].Headroom, runner.Headroom) {
		t.Fatalf("renewal replaced labels or missed liveness: %+v", after)
	}
}

func TestRunnerInventoryAttributesOnlyCurrentSameTenantAuthorityProperty(t *testing.T) {
	property := func(seed uint64, mutation uint8) bool {
		s, demand, claim, now := renewalFixture(t, capability.UnitNode)
		twin := authorityRoot(fmt.Sprintf("twin-%x", seed), "a")
		twin.Subject.ID = "runner"
		authorityPersist(t, s, twin)
		foreign := authorityRoot(fmt.Sprintf("foreign-%x", seed), "b")
		foreign.Subject.ID = "runner"
		authorityPersist(t, s, foreign)
		expectedRunners, expectedActive := 2, 1
		switch mutation % 7 {
		case 1:
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				return tx.operator.revokeAuthorityTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
			}); err != nil {
				t.Fatal(err)
			}
			expectedRunners, expectedActive = 1, 0
		case 2:
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				return tx.operator.revokeAuthorityTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
			}); err != nil {
				t.Fatal(err)
			}
			expectedActive = 0
		case 3:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1 WHERE run_id='run'`)
			expectedActive = 0
		case 4:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET claimed_by=? WHERE run_id='run'`, twin.Subject.ID+fmt.Sprint(seed))
			expectedActive = 0
		case 5:
			*now = claim.Award.LeaseExpiresAt
			expectedActive = 0
		case 6:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET cancel_requested_at=?,cancel_reason='drain' WHERE run_id='run'`, now.UnixNano())
		}
		own := readRunnerInventory(t, s, "a")
		if len(own.Runners) != expectedRunners {
			t.Logf("mutation %d: %+v", mutation%7, own)
			return false
		}
		active := 0
		for _, row := range own.Runners {
			if row.Team != "a" || row.Runner.Name != "runner" {
				return false
			}
			active += len(row.ActiveJobs)
		}
		other := readRunnerInventory(t, s, "b")
		return active == expectedActive && len(other.Runners) == 1 && other.Runners[0].Team == "b" && len(other.Runners[0].ActiveJobs) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 56}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerInventoryCloudAwardsKeepIdentityAcrossExchangeAndRenewal(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, kind)
			before := readRunnerInventory(t, s, "a")
			if len(before.Runners) != 2 {
				t.Fatalf("cloud and idle machine inventory: %+v", before)
			}
			row := before.Runners[1]
			if row.Runner.Type != "cloud" || row.Runner.Name != "launcher" || row.Location != "cloud" || len(row.ActiveJobs) != 1 || row.Headroom != nil {
				t.Fatalf("cloud identity: %+v", row)
			}
			*now = now.Add(time.Second)
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
				t.Fatal(err)
			}
			after := readRunnerInventory(t, s, "a")
			if !reflect.DeepEqual(row.Runner, after.Runners[1].Runner) || !after.Runners[1].LastSeen.Equal(*now) || after.Runners[1].Headroom != nil {
				t.Fatalf("renewed cloud inventory: %+v", after)
			}
			if kind == capability.UnitEval {
				for _, mutate := range []string{
					`UPDATE nodes SET generation=generation+1 WHERE node_id='job'`,
				} {
					rewriteSchemaExec(t, s.db, mutate)
					if got := readRunnerInventory(t, s, "a"); len(got.Runners) != 1 {
						t.Fatalf("stale eval contributes activity: %+v", got)
					}
				}
			}
		})
	}
}

func TestRunnerInventoryRefusesCorruptObservationsWithoutPartialRows(t *testing.T) {
	for _, column := range []string{"advertised_labels_json", "claim_observation_json", "last_used_at"} {
		t.Run(column, func(t *testing.T) {
			s, demand, _, now := renewalFixture(t, capability.UnitNode)
			if err := s.AsOperator().RecordRunnerPoll(t.Context(), demand); err != nil {
				t.Fatal(err)
			}
			var value any = []byte(`{`)
			if column == "last_used_at" {
				value = now.Add(time.Second).UnixNano()
			}
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET `+column+`=? WHERE prefix=?`, value, demand.CapabilityPrefix)
			out, err := (&Tenant{s: s, team: "a"}).ReadRunners(t.Context())
			if err == nil || len(out.Runners) != 0 || !out.Now.IsZero() {
				t.Fatalf("corrupt inventory leaked a partial snapshot: %+v %v", out, err)
			}
		})
	}
}

func TestRefusedRenewalDoesNotChangeRunnerLiveness(t *testing.T) {
	s, _, claim, now := renewalFixture(t, capability.UnitNode)
	before := readRunnerInventory(t, s, "a")
	*now = now.Add(time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
		t.Fatal("unstarted job renewed as executing")
	}
	after := readRunnerInventory(t, s, "a")
	if !reflect.DeepEqual(before.Runners, after.Runners) {
		t.Fatalf("refused renewal changed observations: before=%+v after=%+v", before, after)
	}
}

func TestRunnerObservationFailureRollsBackAwardAndRenewal(t *testing.T) {
	for _, operation := range []string{"award", "renewal"} {
		t.Run(operation, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			var claim UnitClaim
			var err error
			if operation == "renewal" {
				claim, err = s.AsOperator().ClaimUnit(t.Context(), demand)
				if err != nil {
					t.Fatal(err)
				}
			}
			tenant := &Tenant{s: s, team: "a"}
			before, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_observation BEFORE UPDATE OF last_used_at ON capabilities BEGIN SELECT RAISE(ABORT,'observation refused'); END`)
			if operation == "award" {
				out, err := s.AsOperator().ClaimUnit(t.Context(), demand)
				if err == nil || len(out.Bearer) != 0 {
					t.Fatalf("refused award: %+v %v", out, err)
				}
				rewriteSchemaCount(t, s.db, "billing_awards", 0)
				rewriteSchemaCount(t, s.db, "capabilities", 1)
			} else {
				if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err == nil {
					t.Fatal("renewal committed without its observation")
				}
			}
			after, err := tenant.ReadNode(t.Context(), before.Ref)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed observation changed claim: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}
