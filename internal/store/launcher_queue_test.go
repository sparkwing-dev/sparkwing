package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func launcherQueueFixture(t *testing.T, unpaid, funded int) (*Store, UnitDemand) {
	t.Helper()
	s, demand, _ := meteredUnitFixture(t, true)
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'b','older',number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,0,log_retention_class,log_retention_days FROM runs WHERE id='run'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT 'b','older',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`)
	indexFixtureUnit(t, s, "b", UnitRef{Kind: capability.UnitTrigger, RunID: "older"})
	for i := range unpaid {
		node := fmt.Sprintf("unpaid-%03d", i)
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT 'b','older',?,status,ready_at,spec_json,execution_json FROM nodes WHERE team='a' AND node_id='job'`, node)
		indexFixtureUnit(t, s, "b", UnitRef{Kind: capability.UnitNode, RunID: "older", NodeID: runspec.NodeID(node)})
	}
	for i := 1; i < funded; i++ {
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements,claim_context) SELECT team,run_id,?,status,ready_at,spec_json,execution_json,claim_requirements,claim_context FROM nodes WHERE team='a' AND node_id='job'`, fmt.Sprintf("paid-%03d", i))
	}
	demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
	return s, demand
}

func TestLauncherQueueReachesFundedWorkBehindLargeUnpaidQueue(t *testing.T) {
	s, demand := launcherQueueFixture(t, 257, 2)
	for _, expected := range []string{"job", "paid-001"} {
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil || claim.Team != "a" || claim.NodeID != expected {
			t.Fatalf("funded queue: %+v, %v; want %s", claim, err, expected)
		}
	}
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unfunded-only queue: %v", err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 2)
	var claimed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE team='b' AND claimed_by IS NOT NULL`).Scan(&claimed); err != nil || claimed != 0 {
		t.Fatalf("unfunded awards=%d, %v", claimed, err)
	}
}

func TestLauncherConcurrentQueueClaimsEachAwardOnce(t *testing.T) {
	s, demand := launcherQueueFixture(t, 65, 3)
	var group sync.WaitGroup
	results := make(chan UnitClaim, 4)
	for range 4 {
		group.Go(func() {
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if errors.Is(err, ErrNotFound) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			results <- claim
		})
	}
	group.Wait()
	close(results)
	seen := map[string]bool{}
	for claim := range results {
		if claim.Team != "a" || seen[claim.NodeID] || claim.Award.Generation != 1 || len(claim.Bearer) == 0 {
			t.Fatalf("duplicate or unfunded award: %+v", claim)
		}
		seen[claim.NodeID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("awards=%v; want all three funded jobs", seen)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 3)
}

func TestMeteredPlanRenewalExcludesSchedulingWait(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	demand.Target = &UnitRef{Kind: capability.UnitPlan, RunID: "run"}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitPlan, RunID: "run"})
	rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
	claim, actor := meteredUnitClaim(t, s, demand)
	unitBillingTotal(t, s, 120, false)
	rewriteSchemaCount(t, s.db, "billable_intervals", 0)
	*now = now.Add(40 * time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 120, false)
	for range 7 {
		*now = now.Add(10 * time.Second)
		if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err != nil {
			t.Fatal(err)
		}
	}
	finish := runstate.FinishRunRequest{Unit: actor.Owner, Transition: runstate.RunCompletion{RunID: "run", Cause: runstate.RunFinish, Actor: actor}}
	if _, err := (&Tenant{s: s, team: "a"}).FinishRunAndUnit(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 140, true)
	rewriteSchemaCount(t, s.db, "billable_intervals", 1)
}
