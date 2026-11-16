package store

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestOwnerBoundFullGroupSearchCostStaysFlat(t *testing.T) {
	var baseline, baselineChild int
	for _, count := range []int{10, 40, 160} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			raw := []byte(`{"concurrency":[{"group":"scan","scope":"global","capacity":1,"cost":1,"on_limit":"queue"}]}`)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE node_id='job'`, raw)
			indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
			first, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			key := runspec.ConcurrencyKey("job", "global", "", "scan")
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES('a',?,1,1)`, key)
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,cost,declared_capacity,generation) VALUES('a',?,'run/job','run','job',1,?,1,1,?)`, key, s.now().Add(time.Hour).UnixNano(), first.Award.Generation)
			demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
			owner, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			for i := range count {
				id := fmt.Sprintf("queued-%03d", i)
				rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), raw)
				indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: runspec.NodeID(id)})
			}
			demand.CapabilityPrefix = owner.Award.CapabilityPrefix
			demand.Target = nil
			demand.Kinds = []capability.UnitKind{capability.UnitNode}
			trace := countClaimVisits(t, s, func(string) {}, nil)
			trace.examine = false
			trace.allStatements = true
			var samples []time.Duration
			for range 3 {
				before := len(trace.statements)
				trace.enabled = true
				start := time.Now()
				_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
				elapsed := time.Since(start)
				trace.enabled = false
				queries := len(trace.statements) - before
				if baseline == 0 {
					baseline = queries
				}
				if queries > baseline+2 {
					t.Errorf("full-group poll work grew with siblings: %d queries, N=10 used %d", queries, baseline)
				}
				t.Logf("jobs=%d empty_poll_queries=%d", count, queries)
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("full group awarded work: %v", err)
				}
				samples = append(samples, elapsed)
			}
			slices.Sort(samples)
			median := samples[1]
			t.Logf("jobs=%d empty_poll_median=%s samples=%v", count, median, samples)

			rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('a','run','stamp','pending',2,?,?)`, []byte(`{"id":"stamp"}`), []byte(`{}`))
			indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "stamp"})
			before := len(trace.statements)
			trace.enabled = true
			start := time.Now()
			child, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			elapsed := time.Since(start)
			if err != nil || child.NodeID != "stamp" {
				t.Fatalf("full group starved child: %s %v", child.NodeID, err)
			}
			trace.enabled = false
			queries := len(trace.statements) - before
			if baselineChild == 0 {
				baselineChild = queries
			}
			t.Logf("jobs=%d child_claim=%s queries=%d", count, elapsed, queries)
			if queries > baselineChild+2 {
				t.Errorf("full group delayed child with repeated query work: %d queries, N=10 used %d", queries, baselineChild)
			}
		})
	}
}
