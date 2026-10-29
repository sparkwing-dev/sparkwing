package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestClaimQueueMismatchPreservesPositionProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.4s measured in the short class; the full suite runs it")
	}
	random := rand.New(rand.NewPCG(52, 109))
	for range 32 {
		s, demand := unitClaimFixture(t)
		label := fmt.Sprintf("gpu-%016x", random.Uint64())
		execution := runspec.JobExecutionSettings{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{label}}}}
		body, err := json.Marshal(execution)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=?,execution_json=?`, s.now().UnixNano(), body)
		indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
		demand.Target = nil
		demand.Kinds = []capability.UnitKind{capability.UnitNode}
		demand.Labels = []string{label + "-other", "name=forged", "class=cloud", "pool=prod"}
		tenant := &Tenant{s: s, team: "a"}
		before, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
				t.Fatalf("mismatched queue poll: %v", err)
			}
		}
		after, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("mismatch changed queue: %+v -> %+v, %v", before, after, err)
		}
		rewriteSchemaCount(t, s.db, "billing_awards", 0)
		rewriteSchemaCount(t, s.db, "events", 0)
		demand.Labels = []string{label}
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil || claim.NodeID != "job" || claim.Award.Generation != 1 || !claim.Award.LeaseExpiresAt.After(s.now()) {
			t.Fatalf("eligible immediate claim: %+v, %v", claim, err)
		}
	}
}

func TestClaimQueuePreferenceHasNoOfferWindow(t *testing.T) {
	for _, labels := range [][]string{nil, {"gpu"}} {
		t.Run(fmt.Sprint(labels), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			demand.Target = nil
			demand.Kinds = []capability.UnitKind{capability.UnitNode}
			demand.Labels = labels
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=?,execution_json=?`, s.now().UnixNano(), []byte(`{"prefers":{"set":true,"value":["gpu"]}}`))
			indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.NodeID != "job" {
				t.Fatalf("immediate preferred or fallback claim: %+v, %v", claim, err)
			}
			row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || row.ReadyAt == nil || !row.ReadyAt.Equal(s.now()) {
				t.Fatalf("claim changed arrival: %+v, %v", row, err)
			}
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}
